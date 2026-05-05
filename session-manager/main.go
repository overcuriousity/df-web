package main

import (
	"log"
	"net/http"
	"os"
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
	go mgr.idleReaper()

	mux := http.NewServeMux()

	// Auth routes
	mux.HandleFunc("/auth/token", mgr.handleTokenAuth)
	mux.HandleFunc("/auth/passkey/", mgr.handlePasskey)
	mux.HandleFunc("/auth/oidc/", mgr.handleOIDC)
	mux.HandleFunc("/auth/logout", mgr.handleLogout)

	// App routes (require session cookie)
	mux.Handle("/play", mgr.requireAuth(http.HandlerFunc(mgr.handlePlay)))
	mux.Handle("/", mgr.requireAuth(http.HandlerFunc(mgr.handleIndex)))

	// Static web assets (index.html, play-sdl.html, play-text.html)
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
