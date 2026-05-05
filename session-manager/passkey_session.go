package main

import (
	"encoding/base64"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
)

// inMemorySessionStore holds WebAuthn ceremony sessions in memory.
// Suitable for a single-instance server with a small user count.
type inMemorySessionStore struct {
	mu   sync.Mutex
	data map[string]*webauthn.SessionData
}

func newInMemorySessionStore() *inMemorySessionStore {
	return &inMemorySessionStore{data: make(map[string]*webauthn.SessionData)}
}

func (s *inMemorySessionStore) put(key string, session *webauthn.SessionData) {
	s.mu.Lock()
	s.data[key] = session
	s.mu.Unlock()
}

func (s *inMemorySessionStore) get(key string) (*webauthn.SessionData, bool) {
	s.mu.Lock()
	v, ok := s.data[key]
	s.mu.Unlock()
	return v, ok
}

func (s *inMemorySessionStore) delete(key string) {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
}

func encodeBase64URL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
