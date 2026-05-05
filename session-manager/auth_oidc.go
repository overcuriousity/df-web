package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// handleOIDC routes /auth/oidc/* requests.
// Routes:
//   GET /auth/oidc/login    → redirect to Nextcloud authorization endpoint
//   GET /auth/oidc/callback → handle code exchange, set session
func (m *Manager) handleOIDC(w http.ResponseWriter, r *http.Request) {
	if m.cfg.OIDCIssuer == "" {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/auth/oidc/")
	switch {
	case path == "login" && r.Method == http.MethodGet:
		m.oidcLogin(w, r)
	case path == "callback" && r.Method == http.MethodGet:
		m.oidcCallback(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *Manager) oidcProvider(ctx context.Context) (*gooidc.Provider, *oauth2.Config, error) {
	provider, err := gooidc.NewProvider(ctx, m.cfg.OIDCIssuer)
	if err != nil {
		return nil, nil, err
	}
	oc := &oauth2.Config{
		ClientID:     m.cfg.OIDCClient,
		ClientSecret: m.cfg.OIDCSecret,
		RedirectURL:  m.cfg.OIDCRedirect,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{gooidc.ScopeOpenID, "profile"},
	}
	return provider, oc, nil
}

func (m *Manager) oidcLogin(w http.ResponseWriter, r *http.Request) {
	_, oc, err := m.oidcProvider(r.Context())
	if err != nil {
		log.Printf("OIDC provider: %v", err)
		http.Error(w, "OIDC unavailable", http.StatusBadGateway)
		return
	}
	state := randomNonce()
	http.SetCookie(w, &http.Cookie{
		Name:     "oidc_state",
		Value:    state,
		Path:     "/auth/oidc/",
		HttpOnly: true,
		Secure:   true,
		MaxAge:   int((5 * time.Minute).Seconds()),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oc.AuthCodeURL(state), http.StatusFound)
}

func (m *Manager) oidcCallback(w http.ResponseWriter, r *http.Request) {
	// Validate state cookie.
	stateCookie, err := r.Cookie("oidc_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "oidc_state", MaxAge: -1, Path: "/auth/oidc/"})

	provider, oc, err := m.oidcProvider(r.Context())
	if err != nil {
		http.Error(w, "OIDC unavailable", http.StatusBadGateway)
		return
	}

	token, err := oc.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusUnauthorized)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token", http.StatusUnauthorized)
		return
	}
	verifier := provider.Verifier(&gooidc.Config{ClientID: m.cfg.OIDCClient})
	idToken, err := verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "id_token invalid", http.StatusUnauthorized)
		return
	}

	var claims struct {
		Sub string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "claims error", http.StatusInternalServerError)
		return
	}

	user, ok := m.store.ByOIDCSub(claims.Sub)
	if !ok {
		http.Error(w, "account not on allowlist", http.StatusForbidden)
		return
	}

	m.setSession(w, user.UID)
	http.Redirect(w, r, "/play", http.StatusFound)
}

func randomNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
