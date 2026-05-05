package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// dfUser adapts our User to the go-webauthn User interface.
type dfUser struct {
	user *User
}

func (u *dfUser) WebAuthnID() []byte         { return []byte(u.user.UID) }
func (u *dfUser) WebAuthnName() string        { return u.user.UID }
func (u *dfUser) WebAuthnDisplayName() string { return u.user.DisplayName }
func (u *dfUser) WebAuthnIcon() string        { return "" }
func (u *dfUser) WebAuthnCredentials() []webauthn.Credential {
	var out []webauthn.Credential
	for _, c := range u.user.Passkeys {
		id, _ := decodeBase64URL(c.ID)
		pk, _ := decodeBase64URL(c.PublicKey)
		aaguid, _ := decodeBase64URL(c.AAGUID)
		out = append(out, webauthn.Credential{
			ID:        id,
			PublicKey: pk,
			Authenticator: webauthn.Authenticator{
				AAGUID:    aaguid,
				SignCount: c.SignCount,
			},
		})
	}
	return out
}

// handlePasskey routes /auth/passkey/* requests.
// Routes:
//
//	GET  /auth/passkey/register/begin   → begin attestation (requires session)
//	POST /auth/passkey/register/finish  → finish attestation (requires session)
//	GET  /auth/passkey/login/begin      → begin assertion (discoverable, public)
//	POST /auth/passkey/login/finish     → finish assertion (public)
func (m *Manager) handlePasskey(w http.ResponseWriter, r *http.Request) {
	wa, err := m.webauthn()
	if err != nil {
		http.Error(w, "webauthn not configured", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/auth/passkey/")
	switch {
	case path == "register/begin" && r.Method == http.MethodGet:
		uid, err := m.sessionUID(r)
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		m.passkeyRegisterBegin(w, r, wa, uid)
	case path == "register/finish" && r.Method == http.MethodPost:
		uid, err := m.sessionUID(r)
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		m.passkeyRegisterFinish(w, r, wa, uid)
	case path == "login/begin" && r.Method == http.MethodGet:
		m.passkeyLoginBegin(w, r, wa)
	case path == "login/finish" && r.Method == http.MethodPost:
		m.passkeyLoginFinish(w, r, wa)
	default:
		http.NotFound(w, r)
	}
}

var passkeySessionStore = newInMemorySessionStore()

const passkeyNonceCookie = "pkn"

func (m *Manager) webauthn() (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPDisplayName: m.cfg.RPName,
		RPID:          m.cfg.RPID,
		RPOrigins:     m.cfg.RPOrigins,
	})
}

func (m *Manager) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request, wa *webauthn.WebAuthn, uid string) {
	user, ok := m.store.ByUID(uid)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	// Require a resident (discoverable) credential. Login uses
	// BeginDiscoverableLogin, which only finds credentials the authenticator
	// stores locally — a YubiKey enrolled without this flag would succeed at
	// registration but be unusable for login.
	requireResident := true
	sel := protocol.AuthenticatorSelection{
		RequireResidentKey: &requireResident,
		ResidentKey:        protocol.ResidentKeyRequirementRequired,
		UserVerification:   protocol.VerificationPreferred,
	}
	options, session, err := wa.BeginRegistration(&dfUser{user}, webauthn.WithAuthenticatorSelection(sel))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	passkeySessionStore.put("reg:"+uid, session)
	writeJSON(w, options)
}

func (m *Manager) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request, wa *webauthn.WebAuthn, uid string) {
	user, ok := m.store.ByUID(uid)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	session, ok := passkeySessionStore.get("reg:" + uid)
	if !ok {
		http.Error(w, "no registration in progress", http.StatusBadRequest)
		return
	}
	parsedResponse, err := protocol.ParseCredentialCreationResponseBody(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	credential, err := wa.CreateCredential(&dfUser{user}, *session, parsedResponse)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	passkeySessionStore.delete("reg:" + uid)

	newCred := PasskeyCredential{
		ID:        encodeBase64URL(credential.ID),
		PublicKey: encodeBase64URL(credential.PublicKey),
		AAGUID:    encodeBase64URL(credential.Authenticator.AAGUID),
		SignCount: credential.Authenticator.SignCount,
	}
	if err := m.store.UpdatePasskeys(uid, append(user.Passkeys, newCred)); err != nil {
		log.Printf("save passkey: %v", err)
		http.Error(w, "save error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (m *Manager) passkeyLoginBegin(w http.ResponseWriter, r *http.Request, wa *webauthn.WebAuthn) {
	options, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nonce := randomToken()
	passkeySessionStore.put("login:"+nonce, session)
	http.SetCookie(w, &http.Cookie{
		Name:     passkeyNonceCookie,
		Value:    nonce,
		Path:     "/auth/passkey/",
		HttpOnly: true,
		Secure:   !m.cfg.InsecureCookie,
		MaxAge:   5 * 60,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, options)
}

func (m *Manager) passkeyLoginFinish(w http.ResponseWriter, r *http.Request, wa *webauthn.WebAuthn) {
	nonceCookie, err := r.Cookie(passkeyNonceCookie)
	if err != nil {
		http.Error(w, "no login in progress", http.StatusBadRequest)
		return
	}
	key := "login:" + nonceCookie.Value
	http.SetCookie(w, &http.Cookie{Name: passkeyNonceCookie, MaxAge: -1, Path: "/auth/passkey/"})

	session, ok := passkeySessionStore.get(key)
	if !ok {
		http.Error(w, "no login in progress", http.StatusBadRequest)
		return
	}
	parsedResponse, err := protocol.ParseCredentialRequestResponseBody(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		uid := string(userHandle)
		user, ok := m.store.ByUID(uid)
		if !ok {
			return nil, protocol.ErrBadRequest.WithDetails("user not found")
		}
		return &dfUser{user}, nil
	}

	credential, err := wa.ValidateDiscoverableLogin(handler, *session, parsedResponse)
	if err != nil {
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}
	passkeySessionStore.delete(key)

	uid := string(parsedResponse.Response.UserHandle)
	user, _ := m.store.ByUID(uid)
	for i, c := range user.Passkeys {
		id, _ := decodeBase64URL(c.ID)
		if string(id) == string(credential.ID) {
			user.Passkeys[i].SignCount = credential.Authenticator.SignCount
			break
		}
	}
	_ = m.store.UpdatePasskeys(uid, user.Passkeys)

	m.setSession(w, uid)
	writeJSON(w, map[string]string{"status": "ok", "redirect": "/play"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
