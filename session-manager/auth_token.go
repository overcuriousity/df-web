package main

import (
	"net/http"
	"time"
)

// handleTokenAuth handles GET /auth/token?t=<raw_token>
// Validates the token, sets a session cookie, and redirects to /play.
func (m *Manager) handleTokenAuth(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("t")
	if raw == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// Rate-limit: one failed attempt per 500ms to slow enumeration.
	user, ok := m.store.ByToken(raw)
	if !ok {
		time.Sleep(500 * time.Millisecond)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	m.setSession(w, user.UID)
	http.Redirect(w, r, "/play", http.StatusFound)
}
