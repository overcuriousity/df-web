package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type containerInfo struct {
	id       string
	uid      string
	mode     string // "sdl" or "text"
	port     int
	lastSeen time.Time
}

type Manager struct {
	cfg       *Config
	store     *UserStore
	cookieKey []byte

	mu         sync.Mutex
	containers map[string]*containerInfo // uid → container
}

func newManager(cfg *Config, store *UserStore) *Manager {
	key, err := hex.DecodeString(cfg.CookieKey)
	if err != nil || len(key) < 32 {
		log.Fatalf("cookie_key must be a 64+ hex character string (32+ bytes)")
	}
	return &Manager{
		cfg:        cfg,
		store:      store,
		cookieKey:  key,
		containers: make(map[string]*containerInfo),
	}
}

// requireAuth is middleware that checks for a valid session cookie.
func (m *Manager) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := m.sessionUID(r)
		if err != nil {
			http.Redirect(w, r, "/?reason=auth", http.StatusFound)
			return
		}
		if _, ok := m.store.ByUID(uid); !ok {
			m.clearSession(w)
			http.Redirect(w, r, "/?reason=unknown_user", http.StatusFound)
			return
		}
		// Attach uid to the request context via a simple header trick (this is
		// a single-binary server, no external middleware, so we use a context key).
		r = r.WithContext(withUID(r.Context(), uid))
		next.ServeHTTP(w, r)
	})
}

// handleIndex serves the landing page.
func (m *Manager) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, fmt.Sprintf("%s/index.html", m.cfg.WebDir))
}

// handleLogout clears the session cookie and redirects home.
func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	m.clearSession(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// handlePlay ensures a container is running for the user, then proxies the websocket.
func (m *Manager) handlePlay(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	user, _ := m.store.ByUID(uid)

	mode := r.URL.Query().Get("mode")
	if mode != "sdl" && mode != "text" {
		mode = user.DefaultMode
	}
	if mode != "sdl" && mode != "text" {
		mode = "sdl"
	}

	// For non-websocket requests (first page load) serve the appropriate client HTML.
	if r.Header.Get("Upgrade") != "websocket" {
		page := fmt.Sprintf("%s/play-%s.html", m.cfg.WebDir, mode)
		http.ServeFile(w, r, page)
		return
	}

	ci, err := m.ensureContainer(uid, mode)
	if err != nil {
		http.Error(w, "could not start game session: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Proxy the websocket to the container's port.
	proxyWebsocket(w, r, ci.port, func() {
		m.mu.Lock()
		if c, ok := m.containers[uid]; ok && c.id == ci.id {
			c.lastSeen = time.Now()
		}
		m.mu.Unlock()
	})
}

// ensureContainer returns an existing container for the user or starts a new one.
func (m *Manager) ensureContainer(uid, mode string) (*containerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ci, ok := m.containers[uid]; ok {
		ci.lastSeen = time.Now()
		return ci, nil
	}

	// Enforce concurrency cap.
	if len(m.containers) >= m.cfg.MaxSessions {
		return nil, fmt.Errorf("server busy — max %d concurrent sessions", m.cfg.MaxSessions)
	}

	image := m.cfg.ImageSDL
	port := 6080
	if mode == "text" {
		image = m.cfg.ImageText
		port = 7681
	}

	hostPort, id, err := dockerRun(m.cfg, uid, image, port)
	if err != nil {
		return nil, err
	}

	ci := &containerInfo{
		id:       id,
		uid:      uid,
		mode:     mode,
		port:     hostPort,
		lastSeen: time.Now(),
	}
	m.containers[uid] = ci
	log.Printf("started container %s for user %s (mode=%s port=%d)", id[:12], uid, mode, hostPort)
	return ci, nil
}

// idleReaper periodically stops containers that have been idle too long.
func (m *Manager) idleReaper() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		var toReap []*containerInfo
		for _, ci := range m.containers {
			if now.Sub(ci.lastSeen) > m.cfg.IdleTimeout {
				toReap = append(toReap, ci)
			}
		}
		for _, ci := range toReap {
			delete(m.containers, ci.uid)
		}
		m.mu.Unlock()

		for _, ci := range toReap {
			log.Printf("idle reap: stopping container %s (user %s)", ci.id[:12], ci.uid)
			if err := dockerStop(ci.id); err != nil {
				log.Printf("idle reap: stop %s: %v", ci.id[:12], err)
			}
		}
	}
}
