package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"
)

// ErrUserExists is returned by CreateUser when the uid is already taken.
// Handlers use errors.Is to map this to 409 reliably without string-matching.
var ErrUserExists = fmt.Errorf("user already exists")

// uidRe is the same charset enforced by scripts/provision-user.sh: 1-32 chars,
// lowercase alphanumeric plus _ and -, must start alphanumeric. UIDs flow into
// container names, filesystem paths, and YAML keys, so a shared regex keeps the
// script and the daemon from disagreeing about what's safe.
var uidRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

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
	// IsAdmin grants access to /admin/* routes. Set only by provision-user.sh
	// (--admin / --promote / --demote) — the web UI never toggles this flag,
	// so a compromised admin session cannot self-perpetuate the role.
	IsAdmin bool `yaml:"is_admin,omitempty"`
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
		// Bind-mounted single files (our docker-compose mounts users.yml that
		// way) reject rename-over with EBUSY because the kernel pins the inode
		// at the mount point. Fall back to copying the tmp file's contents over
		// the existing inode via O_TRUNC. We've already fsync'd the tmp, so a
		// crash mid-copy leaves the tmp file intact for manual recovery; the
		// only guarantee we lose vs. rename is the inode-swap atomicity, which
		// is irrelevant here since nothing reads users.yml mid-write.
		if errors.Is(err, syscall.EBUSY) {
			if copyErr := overwriteInPlace(tmpPath, s.path); copyErr != nil {
				cleanup()
				return copyErr
			}
			cleanup()
			return nil
		}
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

// CreateUser appends a new user and returns the freshly-generated raw access
// token. The token is returned exactly once; only its SHA-256 is persisted.
// The caller is responsible for creating the on-disk save directories.
func (s *UserStore) CreateUser(uid, displayName string) (string, error) {
	if !uidRe.MatchString(uid) {
		return "", fmt.Errorf("uid %q invalid: must be 1-32 chars, lowercase alphanumeric / underscore / dash, starting alphanumeric", uid)
	}
	if displayName == "" {
		displayName = uid
	}
	raw, err := newRawToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[uid]; exists {
		return "", fmt.Errorf("%w: %q", ErrUserExists, uid)
	}
	s.users[uid] = &User{
		UID:         uid,
		DisplayName: displayName,
		TokenHash:   sha256hex(raw),
	}
	if err := s.saveLocked(); err != nil {
		// Roll back the in-memory mutation so a failed disk write doesn't
		// silently leave a user that vanishes on the next reload.
		delete(s.users, uid)
		return "", err
	}
	return raw, nil
}

// RotateToken generates a new raw token for an existing user, replacing the
// stored TokenHash. Passkeys, OIDC binding, and display name are preserved.
func (s *UserStore) RotateToken(uid string) (string, error) {
	raw, err := newRawToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[uid]
	if !ok {
		return "", fmt.Errorf("user %q not found", uid)
	}
	prev := u.TokenHash
	u.TokenHash = sha256hex(raw)
	if err := s.saveLocked(); err != nil {
		u.TokenHash = prev
		return "", err
	}
	return raw, nil
}

// DeleteUser removes the in-memory entry and persists. Filesystem and
// container cleanup is the caller's responsibility.
func (s *UserStore) DeleteUser(uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[uid]
	if !ok {
		return fmt.Errorf("user %q not found", uid)
	}
	delete(s.users, uid)
	if err := s.saveLocked(); err != nil {
		s.users[uid] = u
		return err
	}
	return nil
}

// IsAdmin reports whether the user exists and has the admin flag set.
func (s *UserStore) IsAdmin(uid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[uid]
	return ok && u.IsAdmin
}

// All returns a snapshot copy of all users, sorted-by-uid order is not
// guaranteed (caller can sort). Useful for the admin listing.
func (s *UserStore) All() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		// Copy so callers can't mutate the live map entries.
		cp := *u
		out = append(out, &cp)
	}
	return out
}

// newRawToken produces a 40-char base64 token, matching the entropy and
// alphabet of provision-user.sh so any tooling expecting that shape works.
func newRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base64.StdEncoding.EncodeToString(b)
	// Strip pad/+/ to mirror the script's `tr -d '=+/'` then trim to 40 chars.
	cleaned := make([]byte, 0, len(enc))
	for i := 0; i < len(enc); i++ {
		c := enc[i]
		if c == '=' || c == '+' || c == '/' {
			continue
		}
		cleaned = append(cleaned, c)
	}
	if len(cleaned) < 40 {
		// Extremely unlikely with 32 bytes of randomness, but be defensive.
		return "", fmt.Errorf("token generation: only %d usable chars", len(cleaned))
	}
	return string(cleaned[:40]), nil
}

// overwriteInPlace copies src's contents over dst using O_TRUNC, preserving
// dst's inode (required for bind-mounted single files). Caller must have
// already fsync'd src so the data is durable on disk.
func overwriteInPlace(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
