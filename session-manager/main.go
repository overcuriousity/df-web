package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	cfg, err := loadConfig("config.yml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := loadUsers("users.yml")
	if err != nil {
		log.Fatalf("users: %v", err)
	}

	// Verify saves root exists on persistent host storage.
	if _, err := os.Stat(cfg.SavesRoot); err != nil {
		log.Fatalf("saves_root %q not found — create it on the host before starting: %v", cfg.SavesRoot, err)
	}
	log.Printf("saves root: %s", cfg.SavesRoot)

	// Ensure every known user has a correctly-owned save directory. This is
	// idempotent and corrects root:root auto-creates from Docker's bind-mount
	// path-creation behaviour.
	store.mu.RLock()
	for uid := range store.users {
		saveDir := filepath.Join(cfg.SavesRoot, uid, "save")
		if err := ensureSaveDir(saveDir); err != nil {
			log.Printf("warn: could not ensure save dir for user %s: %v", uid, err)
		}
	}
	store.mu.RUnlock()

	mgr := newManager(cfg, store)
	mgr.reconcile()
	go mgr.idleReaper()

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP)
		for range ch {
			if err := store.reload(); err != nil {
				log.Printf("reload users: %v", err)
			} else {
				log.Printf("users.yml reloaded")
			}
		}
	}()

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
	mux.Handle("/account", mgr.requireAuth(http.HandlerFunc(mgr.handleAccount)))
	mux.Handle("/account/export", mgr.requireAuth(http.HandlerFunc(mgr.handleAccountExport)))
	mux.Handle("/account/snapshot", mgr.requireAuth(http.HandlerFunc(mgr.handleAccountSnapshot)))

	// Login page is public; authenticated users are redirected to /play.
	mux.HandleFunc("/", mgr.handleIndex)

	// Static web assets
	webDir := cfg.WebDir
	if webDir == "" {
		webDir = "../web"
	}
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

	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
