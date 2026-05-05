package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

const cookieName = "dfsess"
const cookieTTL = 7 * 24 * time.Hour

// sessionPayload is stored in the cookie as base64(uid:issued:hmac).
type sessionPayload struct {
	UID    string
	Issued time.Time
}

func (m *Manager) setSession(w http.ResponseWriter, uid string) {
	raw := uid + ":" + time.Now().UTC().Format(time.RFC3339)
	sig := m.sign(raw)
	val := base64.RawURLEncoding.EncodeToString([]byte(raw + ":" + sig))
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cookieTTL.Seconds()),
	})
}

func (m *Manager) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:    cookieName,
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Secure:  true,
		HttpOnly: true,
	})
}

func (m *Manager) sessionUID(r *http.Request) (string, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", errors.New("no session cookie")
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", errors.New("malformed cookie")
	}
	// Format: uid:issued:sig
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 {
		return "", errors.New("malformed cookie")
	}
	uid, issued, sig := parts[0], parts[1], parts[2]
	want := m.sign(uid + ":" + issued)
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", errors.New("invalid signature")
	}
	t, err := time.Parse(time.RFC3339, issued)
	if err != nil || time.Since(t) > cookieTTL {
		return "", errors.New("session expired")
	}
	return uid, nil
}

func (m *Manager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.cookieKey)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
