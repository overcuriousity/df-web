package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"

	sqlite "modernc.org/sqlite"
	"gopkg.in/yaml.v3"
)

// ErrUserExists is returned by CreateUser when the uid is already taken.
var ErrUserExists = fmt.Errorf("user already exists")

// uidRe is the same charset enforced by scripts/provision-user.sh.
var uidRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

type PasskeyCredential struct {
	ID        string `yaml:"id"`
	PublicKey string `yaml:"public_key"`
	AAGUID    string `yaml:"aaguid"`
	SignCount uint32 `yaml:"sign_count"`
}

type User struct {
	UID           string             `yaml:"uid"`
	DisplayName   string             `yaml:"display_name"`
	TokenHash     string             `yaml:"token_hash"`
	OIDCSub       string             `yaml:"oidc_sub"`
	Passkeys      []PasskeyCredential `yaml:"passkeys"`
	DefaultMode   string             `yaml:"default_mode"`
	ActiveTileset string             `yaml:"active_tileset,omitempty"`
	IsAdmin       bool               `yaml:"is_admin,omitempty"`
}

type UserStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
	uid            TEXT PRIMARY KEY,
	display_name   TEXT NOT NULL DEFAULT '',
	token_hash     TEXT NOT NULL DEFAULT '',
	oidc_sub       TEXT NOT NULL DEFAULT '',
	is_admin       INTEGER NOT NULL DEFAULT 0,
	active_tileset TEXT NOT NULL DEFAULT '',
	default_mode   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS passkeys (
	uid        TEXT NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
	cred_id    TEXT NOT NULL,
	public_key TEXT NOT NULL,
	aaguid     TEXT NOT NULL DEFAULT '',
	sign_count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (uid, cred_id)
);
`

func openDB(path string) (*UserStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Single writer connection avoids SQLITE_BUSY on concurrent mutations.
	db.SetMaxOpenConns(1)

	// Use DELETE journal mode (the SQLite default): each committed write lands
	// directly in the main DB file with no WAL sidecar files. WAL mode creates
	// users.db-wal/-shm alongside the database; those files are NOT
	// bind-mounted into the container, so they live on the overlay FS and
	// vanish on every container recreate — losing all data since the last
	// checkpoint even on a clean restart.
	for _, pragma := range []string{
		"PRAGMA journal_mode=DELETE",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite pragma: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite schema: %w", err)
	}

	s := &UserStore{db: db}

	// One-time import: if users.yml sits next to the DB and the users table is
	// empty, migrate data automatically so the first deploy is seamless.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err == nil && count == 0 {
		yamlPath := filepath.Join(filepath.Dir(path), "users.yml")
		if _, statErr := os.Stat(yamlPath); statErr == nil {
			if err := importFromYAML(db, yamlPath); err != nil {
				log.Printf("users: YAML import failed: %v", err)
			}
		}
	}
	return s, nil
}

// importFromYAML reads a legacy users.yml and inserts all users + passkeys in
// a single transaction. Called once on first startup after the DB migration.
func importFromYAML(db *sql.DB, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var list []*User
	if err := yaml.Unmarshal(data, &list); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, u := range list {
		isAdmin := 0
		if u.IsAdmin {
			isAdmin = 1
		}
		_, err := tx.Exec(
			`INSERT INTO users(uid, display_name, token_hash, oidc_sub, is_admin, active_tileset, default_mode)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			u.UID, u.DisplayName, u.TokenHash, u.OIDCSub, isAdmin, u.ActiveTileset, u.DefaultMode,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("import user %q: %w", u.UID, err)
		}
		for _, c := range u.Passkeys {
			_, err := tx.Exec(
				`INSERT INTO passkeys(uid, cred_id, public_key, aaguid, sign_count) VALUES (?, ?, ?, ?, ?)`,
				u.UID, c.ID, c.PublicKey, c.AAGUID, c.SignCount,
			)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("import passkey for %q: %w", u.UID, err)
			}
		}
		log.Printf("users: imported %s from users.yml", u.UID)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("users: imported %d user(s) from %s", len(list), path)
	return nil
}

func fetchPasskeys(db *sql.DB, uid string) ([]PasskeyCredential, error) {
	rows, err := db.Query(`SELECT cred_id, public_key, aaguid, sign_count FROM passkeys WHERE uid=?`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var creds []PasskeyCredential
	for rows.Next() {
		var c PasskeyCredential
		if err := rows.Scan(&c.ID, &c.PublicKey, &c.AAGUID, &c.SignCount); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

func fetchUserRow(db *sql.DB, uid string) (*User, bool, error) {
	u := &User{}
	var isAdmin int
	err := db.QueryRow(
		`SELECT uid, display_name, token_hash, oidc_sub, is_admin, active_tileset, default_mode FROM users WHERE uid=?`, uid,
	).Scan(&u.UID, &u.DisplayName, &u.TokenHash, &u.OIDCSub, &isAdmin, &u.ActiveTileset, &u.DefaultMode)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	u.IsAdmin = isAdmin != 0
	var pkErr error
	u.Passkeys, pkErr = fetchPasskeys(db, uid)
	if pkErr != nil {
		return nil, false, pkErr
	}
	return u, true, nil
}

func (s *UserStore) ByToken(raw string) (*User, bool) {
	want := sha256hex(raw)
	u := &User{}
	var isAdmin int
	err := s.db.QueryRow(
		`SELECT uid, display_name, token_hash, oidc_sub, is_admin, active_tileset, default_mode FROM users WHERE token_hash=?`, want,
	).Scan(&u.UID, &u.DisplayName, &u.TokenHash, &u.OIDCSub, &isAdmin, &u.ActiveTileset, &u.DefaultMode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		log.Printf("users.ByToken: %v", err)
		return nil, false
	}
	u.IsAdmin = isAdmin != 0
	u.Passkeys, err = fetchPasskeys(s.db, u.UID)
	if err != nil {
		log.Printf("users.ByToken: fetchPasskeys: %v", err)
		return nil, false
	}
	return u, true
}

func (s *UserStore) ByOIDCSub(sub string) (*User, bool) {
	var uid string
	err := s.db.QueryRow(`SELECT uid FROM users WHERE oidc_sub=?`, sub).Scan(&uid)
	if err != nil {
		return nil, false
	}
	u, ok, err := fetchUserRow(s.db, uid)
	if err != nil {
		log.Printf("users.ByOIDCSub: %v", err)
		return nil, false
	}
	return u, ok
}

func (s *UserStore) ByUID(uid string) (*User, bool) {
	u, ok, err := fetchUserRow(s.db, uid)
	if err != nil {
		log.Printf("users.ByUID: %v", err)
		return nil, false
	}
	return u, ok
}

func (s *UserStore) UpdatePasskeys(uid string, creds []PasskeyCredential) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE uid=?`, uid).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("user %q not found", uid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM passkeys WHERE uid=?`, uid); err != nil {
		tx.Rollback()
		return err
	}
	for _, c := range creds {
		if _, err := tx.Exec(
			`INSERT INTO passkeys(uid, cred_id, public_key, aaguid, sign_count) VALUES (?, ?, ?, ?, ?)`,
			uid, c.ID, c.PublicKey, c.AAGUID, c.SignCount,
		); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *UserStore) SetActiveTileset(uid, name string) error {
	res, err := s.db.Exec(`UPDATE users SET active_tileset=? WHERE uid=?`, name, uid)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", uid)
	}
	return nil
}

// UpdatePasskeySignCount updates the clone-detection counter for a credential.
// Returns nil if the user or credential is not found (concurrent delete wins).
func (s *UserStore) UpdatePasskeySignCount(uid string, credID []byte, signCount uint32) error {
	rows, err := s.db.Query(`SELECT cred_id, sign_count FROM passkeys WHERE uid=?`, uid)
	if err != nil {
		return err
	}
	// Find the matching credential; close rows before issuing the UPDATE to
	// avoid deadlocking on the single connection.
	var matchCredID string
	var currentCount uint32
	for rows.Next() {
		var credIDStr string
		var cnt uint32
		if err := rows.Scan(&credIDStr, &cnt); err != nil {
			rows.Close()
			return err
		}
		id, _ := decodeBase64URL(credIDStr)
		if subtle.ConstantTimeCompare(id, credID) == 1 {
			matchCredID = credIDStr
			currentCount = cnt
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if matchCredID == "" || currentCount == signCount {
		return nil
	}
	_, err = s.db.Exec(`UPDATE passkeys SET sign_count=? WHERE uid=? AND cred_id=?`, signCount, uid, matchCredID)
	return err
}

func (s *UserStore) AllPasskeyUsers() []*User {
	rows, err := s.db.Query(
		`SELECT DISTINCT u.uid FROM users u JOIN passkeys p ON p.uid=u.uid`,
	)
	if err != nil {
		log.Printf("users.AllPasskeyUsers: %v", err)
		return nil
	}
	// Collect UIDs before closing the cursor; fetchUserRow opens its own queries
	// and would deadlock on the single connection if we called it inside the loop.
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			log.Printf("users.AllPasskeyUsers: scan: %v", err)
			continue
		}
		uids = append(uids, uid)
	}
	rows.Close()
	var out []*User
	for _, uid := range uids {
		u, ok, err := fetchUserRow(s.db, uid)
		if err != nil {
			log.Printf("users.AllPasskeyUsers: fetch %s: %v", uid, err)
			continue
		}
		if ok {
			out = append(out, u)
		}
	}
	return out
}

// CreateUser inserts a new user and returns the freshly-generated raw access
// token. The token is returned exactly once; only its SHA-256 is persisted.
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
	_, err = s.db.Exec(
		`INSERT INTO users(uid, display_name, token_hash, oidc_sub, is_admin, active_tileset, default_mode) VALUES (?, ?, ?, '', 0, '', '')`,
		uid, displayName, sha256hex(raw),
	)
	if err != nil {
		// SQLite UNIQUE constraint violation means the uid is taken.
		if isConstraintErr(err) {
			return "", fmt.Errorf("%w: %q", ErrUserExists, uid)
		}
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
	res, err := s.db.Exec(`UPDATE users SET token_hash=? WHERE uid=?`, sha256hex(raw), uid)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", fmt.Errorf("user %q not found", uid)
	}
	return raw, nil
}

// DeleteUser removes the user record (passkeys cascade). Filesystem and
// container cleanup is the caller's responsibility.
func (s *UserStore) DeleteUser(uid string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE uid=?`, uid)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", uid)
	}
	return nil
}

// IsAdmin reports whether the user exists and has the admin flag set.
func (s *UserStore) IsAdmin(uid string) bool {
	var isAdmin int
	err := s.db.QueryRow(`SELECT is_admin FROM users WHERE uid=?`, uid).Scan(&isAdmin)
	return err == nil && isAdmin != 0
}

// All returns a snapshot of all users with their passkeys.
func (s *UserStore) All() []*User {
	rows, err := s.db.Query(
		`SELECT uid, display_name, token_hash, oidc_sub, is_admin, active_tileset, default_mode FROM users ORDER BY uid`,
	)
	if err != nil {
		log.Printf("users.All: %v", err)
		return nil
	}
	// Collect user rows before closing cursor; fetchPasskeys opens its own query
	// and would deadlock on the single connection if called inside the loop.
	var out []*User
	for rows.Next() {
		u := &User{}
		var isAdmin int
		if err := rows.Scan(&u.UID, &u.DisplayName, &u.TokenHash, &u.OIDCSub, &isAdmin, &u.ActiveTileset, &u.DefaultMode); err != nil {
			log.Printf("users.All: scan: %v", err)
			continue
		}
		u.IsAdmin = isAdmin != 0
		out = append(out, u)
	}
	rows.Close()
	for _, u := range out {
		var pkErr error
		u.Passkeys, pkErr = fetchPasskeys(s.db, u.UID)
		if pkErr != nil {
			log.Printf("users.All: fetchPasskeys for %s: %v", u.UID, pkErr)
		}
	}
	return out
}

// newRawToken produces a 40-char base64 token, matching the entropy and
// alphabet of provision-user.sh.
func newRawToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	enc := base64.StdEncoding.EncodeToString(b)
	cleaned := make([]byte, 0, len(enc))
	for i := 0; i < len(enc); i++ {
		c := enc[i]
		if c == '=' || c == '+' || c == '/' {
			continue
		}
		cleaned = append(cleaned, c)
	}
	if len(cleaned) < 40 {
		return "", fmt.Errorf("token generation: only %d usable chars", len(cleaned))
	}
	return string(cleaned[:40]), nil
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (s *UserStore) Close() error { return s.db.Close() }

// isConstraintErr detects SQLite UNIQUE/PRIMARY KEY constraint violations
// (SQLITE_CONSTRAINT = 19, or any extended code with the same base).
func isConstraintErr(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xFF == 19
}
