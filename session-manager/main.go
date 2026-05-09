package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	cfg, err := loadConfig("config.yml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := openDB("users.db")
	if err != nil {
		log.Fatalf("users: %v", err)
	}

	// Verify saves root exists on persistent host storage.
	if _, err := os.Stat(cfg.SavesRoot); err != nil {
		log.Fatalf("saves_root %q not found — create it on the host before starting: %v", cfg.SavesRoot, err)
	}
	log.Printf("saves root: %s", cfg.SavesRoot)

	// Ensure every known user has correctly-owned data + config directories.
	// Idempotent; corrects root:root auto-creates from Docker's bind-mount
	// path-creation behaviour.
	for _, u := range store.All() {
		if err := ensureUserDirs(cfg.SavesRoot, u.UID); err != nil {
			log.Printf("warn: could not ensure user dirs for %s: %v", u.UID, err)
		}
	}

	mgr := newManager(cfg, store)
	mgr.reconcile()
	go mgr.idleReaper()

	webDir := cfg.WebDir
	if webDir == "" {
		webDir = "../web"
	}

	mux := http.NewServeMux()

	// Auth routes
	mux.HandleFunc("/auth/token", mgr.handleTokenAuth)
	mux.HandleFunc("/auth/passkey/", mgr.handlePasskey)
	if cfg.OIDCIssuer != "" {
		mux.HandleFunc("/auth/oidc/", mgr.handleOIDC)
	}
	mux.HandleFunc("/auth/logout", mgr.handleLogout)

	// App routes (require session cookie)
	mux.Handle("/play", mgr.requireAuth(http.HandlerFunc(mgr.handlePlay)))
	mux.Handle("/play/audio", mgr.requireAuth(http.HandlerFunc(mgr.handlePlayAudio)))
	mux.Handle("/session/status", mgr.requireAuth(http.HandlerFunc(mgr.handleSessionStatus)))
	mux.Handle("/session/keepalive", mgr.requireAuth(http.HandlerFunc(mgr.handleSessionKeepalive)))
	mux.Handle("/session/stop", mgr.requireAuth(http.HandlerFunc(mgr.handleSessionStop)))
	mux.Handle("/account", mgr.requireAuth(http.HandlerFunc(mgr.handleAccount)))
	mux.Handle("/account/export", mgr.requireAuth(http.HandlerFunc(mgr.handleAccountExport)))
	mux.Handle("/account/snapshot", mgr.requireAuth(http.HandlerFunc(mgr.handleAccountSnapshot)))
	mux.Handle("/account/import", mgr.requireAuth(http.HandlerFunc(mgr.handleAccountImport)))
	mux.Handle("/account/tilesets", mgr.requireAuth(http.HandlerFunc(mgr.handleTilesets)))
	mux.Handle("/account/tilesets/", mgr.requireAuth(http.HandlerFunc(mgr.handleTilesetItem)))

	// Storyteller bundle (journal + timeline)
	mux.Handle("/play/saves", mgr.requireAuth(http.HandlerFunc(mgr.handleSaves)))
	mux.Handle("/play/journal", mgr.requireAuth(http.HandlerFunc(mgr.handleJournal)))
	mux.Handle("/play/timeline", mgr.requireAuth(http.HandlerFunc(mgr.handleTimeline)))
	// DFHack endpoints — only registered when dfhack_enabled: true in config.yml
	if cfg.DFHackEnabled {
		mux.Handle("/play/dfhack/units", mgr.requireAuth(http.HandlerFunc(mgr.handleDFHackUnits)))
		mux.Handle("/play/dfhack/labor", mgr.requireAuth(http.HandlerFunc(mgr.handleDFHackSetLabor)))
	}
	// Capabilities endpoint: lets the frontend discover which optional features are active.
	mux.Handle("/session/capabilities", mgr.requireAuth(http.HandlerFunc(mgr.handleCapabilities)))

	// Admin routes (require IsAdmin in addition to a valid session).
	mux.Handle("/admin", mgr.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(webDir, "admin.html"))
	})))
	mux.Handle("/admin/users", mgr.requireAdmin(http.HandlerFunc(mgr.handleAdminUsers)))
	mux.Handle("/admin/users/", mgr.requireAdmin(http.HandlerFunc(mgr.handleAdminUserItem)))

	// Login page is public; authenticated users are redirected to /play.
	mux.HandleFunc("/", mgr.handleIndex)

	// Static web assets
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(webDir))))

	// noVNC static files for the SDL client (served from the host installation).
	novncDir := cfg.NoVNCDir
	if novncDir == "" {
		novncDir = "/usr/share/novnc"
	}
	mux.Handle("/novnc/", http.StripPrefix("/novnc/", http.FileServer(http.Dir(novncDir))))

	addr := cfg.Listen
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if v := os.Getenv("LISTEN"); v != "" {
		addr = v
	}

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		log.Printf("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		store.Close()
	}()

	log.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
