package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
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
	mux.Handle("/account", mgr.requireAuth(http.HandlerFunc(mgr.handleAccount)))

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
