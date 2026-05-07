package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type PasskeyCredential struct {
	ID        string `yaml:"id"`
	PublicKey string `yaml:"public_key"`
	AAGUID    string `yaml:"aaguid"`
	SignCount uint32 `yaml:"sign_count"`
}

type User struct {
	UID         string              `yaml:"uid"`
	DisplayName string              `yaml:"display_name"`
	TokenHash   string              `yaml:"token_hash"` // hex SHA-256 of the raw token
	OIDCSub     string              `yaml:"oidc_sub"`
	Passkeys    []PasskeyCredential `yaml:"passkeys"`
	DefaultMode string              `yaml:"default_mode"` // "sdl" or "text"
	// ActiveTileset is the filename (e.g. "curses_640x300.png") of the user's
	// chosen tileset under <savesRoot>/<uid>/tilesets/. Applied to init.txt
	// at container spawn. Empty means "use the image default".
	ActiveTileset string `yaml:"active_tileset,omitempty"`
}

type UserStore struct {
	mu    sync.RWMutex
	path  string
	users map[string]*User
}

func loadUsers(path string) (*UserStore, error) {
	s := &UserStore{path: path, users: make(map[string]*User)}
	return s, s.reload()
}

func (s *UserStore) reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var list []*User
	if err := yaml.Unmarshal(data, &list); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = make(map[string]*User, len(list))
	for _, u := range list {
		s.users[u.UID] = u
	}
	log.Printf("users: loaded %d user(s) from %s", len(list), s.path)
	return nil
}

func (s *UserStore) save() error {
	s.mu.RLock()
	list := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	s.mu.RUnlock()
	data, err := yaml.Marshal(list)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func (s *UserStore) ByToken(raw string) (*User, bool) {
	want := sha256hex(raw)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.TokenHash != "" && subtle.ConstantTimeCompare([]byte(u.TokenHash), []byte(want)) == 1 {
			return u, true
		}
	}
	return nil, false
}

func (s *UserStore) ByOIDCSub(sub string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.OIDCSub == sub {
			return u, true
		}
	}
	return nil, false
}

func (s *UserStore) ByUID(uid string) (*User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[uid]
	return u, ok
}

func (s *UserStore) UpdatePasskeys(uid string, creds []PasskeyCredential) error {
	s.mu.Lock()
	u, ok := s.users[uid]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("user %q not found", uid)
	}
	u.Passkeys = creds
	s.mu.Unlock()
	return s.save()
}

func (s *UserStore) SetActiveTileset(uid, name string) error {
	s.mu.Lock()
	u, ok := s.users[uid]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("user %q not found", uid)
	}
	u.ActiveTileset = name
	s.mu.Unlock()
	return s.save()
}

func (s *UserStore) AllPasskeyUsers() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*User
	for _, u := range s.users {
		if len(u.Passkeys) > 0 {
			out = append(out, u)
		}
	}
	return out
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
