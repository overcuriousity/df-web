package main

import (
	"log"
	"net/http"
	"time"
)

// handleTokenAuth handles POST /auth/token (form field: key).
func (m *Manager) handleTokenAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := r.FormValue("key")
	if raw == "" {
		http.Redirect(w, r, "/?reason=bad_key", http.StatusFound)
		return
	}

	user, ok := m.store.ByToken(raw)
	if !ok {
		log.Printf("auth: bad key attempt from %s (key len=%d)", r.RemoteAddr, len(raw))
		time.Sleep(500 * time.Millisecond)
		http.Redirect(w, r, "/?reason=bad_key", http.StatusFound)
		return
	}

	log.Printf("auth: token login succeeded for uid=%s from %s", user.UID, r.RemoteAddr)
	m.setSession(w, user.UID)
	http.Redirect(w, r, "/play", http.StatusSeeOther)
}
