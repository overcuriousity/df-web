package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type containerInfo struct {
	id       string
	uid      string
	mode     string // "sdl" or "text"
	host     string // container hostname on df_internal, e.g. "df-alice"
	port     int    // internal port (websockify, typically 6080)
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
		isWS := r.Header.Get("Upgrade") == "websocket"
		uid, err := m.sessionUID(r)
		if err != nil {
			log.Printf("requireAuth: rejected %s %s from %s: %v", r.Method, r.URL.Path, r.RemoteAddr, err)
			if isWS {
				http.Error(w, "session required", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/?reason=auth", http.StatusFound)
			}
			return
		}
		if _, ok := m.store.ByUID(uid); !ok {
			log.Printf("requireAuth: unknown uid=%s from %s", uid, r.RemoteAddr)
			m.clearSession(w)
			if isWS {
				http.Error(w, "unknown user", http.StatusForbidden)
			} else {
				http.Redirect(w, r, "/?reason=unknown_user", http.StatusFound)
			}
			return
		}
		// Attach uid to the request context via a simple header trick (this is
		// a single-binary server, no external middleware, so we use a context key).
		r = r.WithContext(withUID(r.Context(), uid))
		next.ServeHTTP(w, r)
	})
}

var reasonText = map[string]string{
	"auth":         "Session expired — please log in again.",
	"unknown_user": "Account not on the allowlist.",
	"bad_key":      "Invalid key — try again.",
}

// handleIndex serves the login page, or redirects to /play if already authenticated.
func (m *Manager) handleIndex(w http.ResponseWriter, r *http.Request) {
	if uid, err := m.sessionUID(r); err == nil {
		if _, ok := m.store.ByUID(uid); ok {
			http.Redirect(w, r, "/play", http.StatusFound)
			return
		}
	}
	tmpl, err := template.ParseFiles(filepath.Join(m.cfg.WebDir, "index.html"))
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	data := struct {
		OIDCEnabled bool
		Reason      string
	}{
		OIDCEnabled: m.cfg.OIDCIssuer != "",
		Reason:      reasonText[r.URL.Query().Get("reason")],
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, data)
}

// handleAccount serves the passkey self-enrollment page.
func (m *Manager) handleAccount(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	user, _ := m.store.ByUID(uid)
	tmpl, err := template.ParseFiles(filepath.Join(m.cfg.WebDir, "account.html"))
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	data := struct {
		DisplayName string
		Passkeys    []PasskeyCredential
	}{
		DisplayName: user.DisplayName,
		Passkeys:    user.Passkeys,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, data)
}

// handleLogout clears the session cookie and redirects home.
func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	m.clearSession(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

// handlePlay ensures a container is running for the user, then proxies the websocket.
func (m *Manager) handlePlay(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	// DF 53.x is SDL2-only (no terminal/text mode). Mode is always "sdl".
	mode := "sdl"

	// For non-websocket requests (first page load) serve the SDL client HTML.
	if r.Header.Get("Upgrade") != "websocket" {
		http.ServeFile(w, r, filepath.Join(m.cfg.WebDir, "play-sdl.html"))
		return
	}

	ci, err := m.ensureContainer(uid, mode)
	if err != nil {
		http.Error(w, "could not start game session: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Proxy the websocket to the container's port via df_internal.
	proxyWebsocket(w, r, ci.host, ci.port, func() {
		m.mu.Lock()
		if c, ok := m.containers[uid]; ok && c.id == ci.id {
			c.lastSeen = time.Now()
		}
		m.mu.Unlock()
	})
}

// handlePlayAudio proxies the audio stream from the user's running container.
// Returns 404 if no container is active (the user must load /play first).
func (m *Manager) handlePlayAudio(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	ci, ok := m.containers[uid]
	m.mu.Unlock()

	if !ok {
		http.Error(w, "no active session", http.StatusNotFound)
		return
	}
	const audioPort = 6081
	proxyHTTPStream(w, r, ci.host, audioPort)
}

// ensureContainer returns an existing container for the user or starts a new one.
func (m *Manager) ensureContainer(uid, mode string) (*containerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ci, ok := m.containers[uid]; ok {
		if dockerIsRunning(ci.id) {
			ci.lastSeen = time.Now()
			return ci, nil
		}
		// Cached container has died (DF crash, OOM, manual docker rm, …).
		// Drop the stale entry and fall through to spawn a fresh one.
		log.Printf("ensureContainer: stale entry for user %s (container %s no longer running) — respawning", uid, ci.id[:12])
		delete(m.containers, uid)
	}

	// Enforce concurrency cap.
	if len(m.containers) >= m.cfg.MaxSessions {
		return nil, fmt.Errorf("server busy — max %d concurrent sessions", m.cfg.MaxSessions)
	}

	image := m.cfg.ImageSDL
	const vncPort = 6080

	id, err := dockerRun(m.cfg, uid, image)
	if err != nil {
		log.Printf("dockerRun failed for user %s: %v", uid, err)
		return nil, err
	}

	containerName := fmt.Sprintf("df-%s", uid)
	ci := &containerInfo{
		id:       id,
		uid:      uid,
		mode:     mode,
		host:     containerName,
		port:     vncPort,
		lastSeen: time.Now(),
	}
	m.containers[uid] = ci
	log.Printf("started container %s for user %s (mode=%s addr=%s:%d)", id[:12], uid, mode, containerName, vncPort)
	return ci, nil
}

// reconcile adopts running df-* containers from a previous session-manager
// instance into m.containers, and removes any stopped orphans. Call once at startup.
func (m *Manager) reconcile() {
	running, err := dockerListRunning()
	if err != nil {
		log.Printf("reconcile: list containers: %v", err)
		return
	}
	const vncPort = 6080
	m.mu.Lock()
	for _, c := range running {
		if !strings.HasPrefix(c.name, "df-") {
			continue
		}
		uid := strings.TrimPrefix(c.name, "df-")
		if uid == "" {
			continue
		}
		// Docker's name filter is a substring match, so containers like
		// "df-web-session-manager-1" leak through. Only adopt names that
		// correspond to a known user.
		if _, ok := m.store.ByUID(uid); !ok {
			continue
		}
		m.containers[uid] = &containerInfo{
			id:       c.id,
			uid:      uid,
			mode:     "sdl",
			host:     c.name,
			port:     vncPort,
			lastSeen: time.Now(),
		}
		log.Printf("reconcile: adopted container %s for user %s", c.id[:12], uid)
	}
	m.mu.Unlock()
	dockerRemoveExited()
}

// streamSavesTarball writes the user's data + config directories to w as a
// gzipped tar. Layout in the archive:
//
//	data/...    (DF saves; XDG_DATA_HOME/Bay 12 Games/Dwarf Fortress)
//	config/...  (DF settings; XDG_CONFIG_HOME/Bay 12 Games/Dwarf Fortress)
//
// Caller is responsible for setting Content-Type / Content-Disposition before
// the first byte is written. If neither directory exists or both are empty, a
// 404 is written and the caller can return.
func (m *Manager) streamSavesTarball(w http.ResponseWriter, uid string) {
	userRoot := filepath.Join(m.cfg.SavesRoot, uid)
	if !hasAnyContent(userRoot) {
		http.Error(w, "Nothing to export yet — play a game first.", http.StatusNotFound)
		return
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	for _, sub := range []string{"data", "config"} {
		root := filepath.Join(userRoot, sub)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(userRoot, path)
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if walkErr != nil {
			log.Printf("export: walk %s for user %s: %v", sub, uid, walkErr)
		}
	}
	if err := tw.Close(); err != nil {
		log.Printf("export: tar close for user %s: %v", uid, err)
	}
	if err := gz.Close(); err != nil {
		log.Printf("export: gzip close for user %s: %v", uid, err)
	}
}

// hasAnyContent returns true if userRoot/data or userRoot/config exist and
// contain at least one entry.
func hasAnyContent(userRoot string) bool {
	for _, sub := range []string{"data", "config"} {
		entries, err := os.ReadDir(filepath.Join(userRoot, sub))
		if err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

// handleAccountExport stops the user's active container (flushing saves via the
// quit-save sequence) and streams their entire save directory as a tar.gz download.
func (m *Manager) handleAccountExport(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	// Quiesce the active container so DF flushes saves before we tarball.
	m.mu.Lock()
	ci, hasContainer := m.containers[uid]
	if hasContainer {
		delete(m.containers, uid)
	}
	m.mu.Unlock()

	if hasContainer {
		log.Printf("export: stopping container %s for user %s (save flush)", ci.id[:12], uid)
		if err := dockerStop(ci.id); err != nil {
			log.Printf("export: stop container for user %s: %v", uid, err)
		}
	}

	filename := fmt.Sprintf("df-%s-%s.tar.gz", uid, time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	m.streamSavesTarball(w, uid)
}

// handleAccountSnapshot streams the user's save directory as a tar.gz without
// stopping the running container. DF writes saves atomically (temp dir +
// rename), so a snapshot taken mid-play captures the most recent completed
// save; the small window during a save's rename is the only risk and the
// user can simply re-snapshot.
func (m *Manager) handleAccountSnapshot(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	ci, hasContainer := m.containers[uid]
	m.mu.Unlock()
	if hasContainer {
		log.Printf("snapshot: live snapshot for user %s (container %s still running)", uid, ci.id[:12])
	}

	filename := fmt.Sprintf("df-%s-%s-snapshot.tar.gz", uid, time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	m.streamSavesTarball(w, uid)
}

// handleSessionStatus returns idle / timeout info for the caller's container
// as JSON. Read-only — never bumps lastSeen, so polling is safe.
func (m *Manager) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	type resp struct {
		Active             bool  `json:"active"`
		IdleSeconds        int64 `json:"idle_seconds"`
		IdleTimeoutSeconds int64 `json:"idle_timeout_seconds"`
		SecondsUntilReap   int64 `json:"seconds_until_reap"`
	}

	timeout := int64(m.cfg.IdleTimeout / time.Second)

	m.mu.Lock()
	ci, ok := m.containers[uid]
	var idle int64
	if ok {
		idle = int64(time.Since(ci.lastSeen) / time.Second)
	}
	m.mu.Unlock()

	out := resp{
		Active:             ok,
		IdleTimeoutSeconds: timeout,
	}
	if ok {
		out.IdleSeconds = idle
		out.SecondsUntilReap = timeout - idle
		if out.SecondsUntilReap < 0 {
			out.SecondsUntilReap = 0
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleSessionKeepalive bumps lastSeen for the caller's container, extending
// the idle window by IdleTimeout. Returns 204 on success, 409 if the user
// has no active container so the frontend can show "session ended" UX.
func (m *Manager) handleSessionKeepalive(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	c, ok := m.containers[uid]
	if ok {
		c.lastSeen = time.Now()
	}
	m.mu.Unlock()

	if !ok {
		http.Error(w, "no active session", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
