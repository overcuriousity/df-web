package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// requireAdmin wraps requireAuth and additionally checks that the resolved uid
// is flagged as admin. Non-admins receive 404 (rather than 403) so the
// existence of the admin surface isn't trivially probeable from a regular
// account. Page requests get redirected back to /play to match how the rest
// of the app handles soft-denies.
func (m *Manager) requireAdmin(next http.Handler) http.Handler {
	return m.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := uidFromContext(r.Context())
		if !m.store.IsAdmin(uid) {
			log.Printf("admin: non-admin uid=%s attempted %s %s", uid, r.Method, r.URL.Path)
			isAPI := strings.HasPrefix(r.URL.Path, "/admin/users")
			if isAPI {
				http.NotFound(w, r)
			} else {
				http.Redirect(w, r, "/play", http.StatusFound)
			}
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// adminUserView is the JSON shape returned by GET /admin/users.
type adminUserView struct {
	UID                string `json:"uid"`
	DisplayName        string `json:"display_name"`
	IsAdmin            bool   `json:"is_admin"`
	HasToken           bool   `json:"has_token"`
	HasOIDC            bool   `json:"has_oidc"`
	PasskeyCount       int    `json:"passkey_count"`
	HasActiveContainer bool   `json:"has_active_container"`
}

// handleAdminUsers serves GET (list) and POST (create) at /admin/users.
func (m *Manager) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.adminListUsers(w, r)
	case http.MethodPost:
		m.adminCreateUser(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) adminListUsers(w http.ResponseWriter, _ *http.Request) {
	users := m.store.All()

	m.mu.Lock()
	active := make(map[string]bool, len(m.containers))
	for uid := range m.containers {
		active[uid] = true
	}
	m.mu.Unlock()

	views := make([]adminUserView, 0, len(users))
	for _, u := range users {
		views = append(views, adminUserView{
			UID:                u.UID,
			DisplayName:        u.DisplayName,
			IsAdmin:            u.IsAdmin,
			HasToken:           u.TokenHash != "",
			HasOIDC:            u.OIDCSub != "",
			PasskeyCount:       len(u.Passkeys),
			HasActiveContainer: active[u.UID],
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].UID < views[j].UID })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(views)
}

func (m *Manager) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID         string `json:"uid"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.UID = strings.TrimSpace(body.UID)
	body.DisplayName = strings.TrimSpace(body.DisplayName)

	// Pre-create on-disk dirs *before* the YAML write, so a successful response
	// implies a usable account. If this fails we never touched users.yml.
	if !uidRe.MatchString(body.UID) {
		http.Error(w, "uid must be 1-32 chars, lowercase alphanumeric / underscore / dash, starting alphanumeric", http.StatusBadRequest)
		return
	}
	if err := ensureUserDirs(m.cfg.SavesRoot, body.UID); err != nil {
		log.Printf("admin: ensureUserDirs %s: %v", body.UID, err)
		http.Error(w, "could not create user directories", http.StatusInternalServerError)
		return
	}

	rawToken, err := m.store.CreateUser(body.UID, body.DisplayName)
	if err != nil {
		log.Printf("admin: CreateUser %s: %v", body.UID, err)
		// User-facing errors stay informative; internal failures (token
		// generation, disk write) get a generic 500 so we don't leak details
		// like file paths or syscall errors back to the browser.
		switch {
		case errors.Is(err, ErrUserExists):
			http.Error(w, "user already exists", http.StatusConflict)
		default:
			// CreateUser only returns ErrUserExists or internal errors —
			// validation (uid charset) is checked before this call, so any
			// other error here is server-side.
			http.Error(w, "internal error creating user", http.StatusInternalServerError)
		}
		return
	}

	log.Printf("admin: created user %s (by %s)", body.UID, uidFromContext(r.Context()))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"uid":       body.UID,
		"raw_token": rawToken,
	})
}

// handleAdminUserItem dispatches operations on a single user identified by the
// trailing path segment(s) under /admin/users/.
//
// Routes handled:
//   - POST   /admin/users/{uid}/rotate
//   - DELETE /admin/users/{uid}
func (m *Manager) handleAdminUserItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/users/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(rest, "/")
	uid := parts[0]
	if !uidRe.MatchString(uid) {
		http.Error(w, "invalid uid", http.StatusBadRequest)
		return
	}

	switch {
	case len(parts) == 2 && parts[1] == "rotate" && r.Method == http.MethodPost:
		m.adminRotateToken(w, r, uid)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		m.adminDeleteUser(w, r, uid)
	default:
		http.NotFound(w, r)
	}
}

func (m *Manager) adminRotateToken(w http.ResponseWriter, r *http.Request, uid string) {
	if _, ok := m.store.ByUID(uid); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	raw, err := m.store.RotateToken(uid)
	if err != nil {
		log.Printf("admin: RotateToken %s: %v", uid, err)
		http.Error(w, "could not rotate token", http.StatusInternalServerError)
		return
	}
	log.Printf("admin: rotated token for %s (by %s)", uid, uidFromContext(r.Context()))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"uid":       uid,
		"raw_token": raw,
	})
}

func (m *Manager) adminDeleteUser(w http.ResponseWriter, r *http.Request, uid string) {
	caller := uidFromContext(r.Context())
	if uid == caller {
		http.Error(w, "cannot delete your own account", http.StatusBadRequest)
		return
	}
	if _, ok := m.store.ByUID(uid); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Step 1: stop and clear any running container. Mirror the flag-then-stop
	// sequence in handleSessionStop so a parallel /play cannot rerun docker rm
	// against a half-removed user. Verify the container is actually running
	// first — a stale entry left by a DF crash should be cleared, not stopped
	// (dockerStop on a vanished container would otherwise wedge the delete).
	m.mu.Lock()
	ci, hasContainer := m.containers[uid]
	if hasContainer {
		ci.stopping = true
	}
	m.mu.Unlock()
	if hasContainer && !dockerIsRunning(ci.id) {
		log.Printf("admin: clearing stale container entry %s for %s", ci.id[:12], uid)
		m.mu.Lock()
		if cur, ok := m.containers[uid]; ok && cur.id == ci.id {
			delete(m.containers, uid)
		}
		m.mu.Unlock()
		hasContainer = false
	}
	if hasContainer {
		log.Printf("admin: stopping container %s for delete of %s", ci.id[:12], uid)
		if err := dockerStop(ci.id); err != nil {
			log.Printf("admin: dockerStop %s: %v", uid, err)
			// We're aborting the delete, so reset stopping=false: leaving it set
			// would wedge /play (errSessionEnding) for an account that is
			// otherwise still valid. Operator can retry the delete later.
			m.mu.Lock()
			if cur, ok := m.containers[uid]; ok && cur.id == ci.id {
				cur.stopping = false
			}
			m.mu.Unlock()
			http.Error(w, "could not stop user's container — try again in a moment", http.StatusServiceUnavailable)
			return
		}
		m.stopSessionLog(uid)
		m.mu.Lock()
		if cur, ok := m.containers[uid]; ok && cur.id == ci.id {
			delete(m.containers, uid)
		}
		m.mu.Unlock()
	}

	// Step 2: drop from users.yml *before* removing data on disk. This order
	// matters for two reasons:
	//   1. Once the user is gone from the store, requireAuth rejects further
	//      requests for this uid and ensureContainer can't spawn a fresh
	//      container under us — closing the race where /play recreates the
	//      data dir we're about to delete.
	//   2. If DeleteUser fails (EBUSY, disk full, …) the user still has their
	//      data dir intact and the operator can retry. The reverse order would
	//      have already nuked their saves before we knew the YAML write had
	//      failed — irreversible data loss while reporting an error.
	if err := m.store.DeleteUser(uid); err != nil {
		log.Printf("admin: DeleteUser %s: %v", uid, err)
		http.Error(w, "could not remove user record", http.StatusInternalServerError)
		return
	}

	// Step 3: remove on-disk save directory. If this fails the user is already
	// gone from the system; log and surface a soft warning rather than a hard
	// error so the operator can clean up the orphan dir manually.
	userRoot := filepath.Join(m.cfg.SavesRoot, uid)
	if err := os.RemoveAll(userRoot); err != nil {
		log.Printf("admin: RemoveAll %s after user record removed: %v", userRoot, err)
	}

	log.Printf("admin: deleted user %s (by %s)", uid, caller)
	w.WriteHeader(http.StatusNoContent)
}
