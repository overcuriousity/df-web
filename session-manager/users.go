package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

// save serialises the in-memory user map to disk atomically. Callers must
// already hold s.mu (write lock); see saveLocked / the *Locked write helpers.
// The caller-holds-lock contract means concurrent writers can't lose updates
// (a previous version released the lock before marshalling, letting two
// in-flight UpdatePasskeys clobber each other).
//
// The temp-file + rename + dir-fsync sequence ensures users.yml is never seen
// truncated, even on crash or power loss between truncate and write — which
// would otherwise lock every user out of the service.
func (s *UserStore) saveLocked() error {
	list := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		list = append(list, u)
	}
	data, err := yaml.Marshal(list)
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".users-*.yml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		cleanup()
		return err
	}
	// fsync the parent directory so the rename is durable.
	d, err := os.Open(dir)
	if err != nil {
		log.Printf("users.save: open parent dir %s for fsync: %v", dir, err)
		return nil
	}
	if err := d.Sync(); err != nil {
		log.Printf("users.save: fsync parent dir %s: %v", dir, err)
	}
	if err := d.Close(); err != nil {
		log.Printf("users.save: close parent dir %s: %v", dir, err)
	}
	return nil
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
	defer s.mu.Unlock()
	u, ok := s.users[uid]
	if !ok {
		return fmt.Errorf("user %q not found", uid)
	}
	u.Passkeys = creds
	return s.saveLocked()
}

func (s *UserStore) SetActiveTileset(uid, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[uid]
	if !ok {
		return fmt.Errorf("user %q not found", uid)
	}
	u.ActiveTileset = name
	return s.saveLocked()
}

// UpdatePasskeySignCount atomically writes a new SignCount for the credential
// matching credID under uid. Used after a successful WebAuthn assertion to
// keep the clone-detection counter monotonic. Returns nil if the user or
// credential is not found (the assertion already validated the credential
// itself; a missing entry here means a concurrent delete won the race).
func (s *UserStore) UpdatePasskeySignCount(uid string, credID []byte, signCount uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[uid]
	if !ok {
		return nil
	}
	for i, c := range u.Passkeys {
		id, _ := decodeBase64URL(c.ID)
		if subtle.ConstantTimeCompare(id, credID) == 1 {
			if u.Passkeys[i].SignCount == signCount {
				return nil // no change, skip the disk write
			}
			u.Passkeys[i].SignCount = signCount
			return s.saveLocked()
		}
	}
	return nil
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
