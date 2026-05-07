package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// tilesetNameRe restricts uploaded filenames to a safe charset. First
// character must be alphanumeric (so no leading dot — i.e. no hidden files);
// remaining characters allow letters, digits, dot, underscore, dash; must
// end in .png (case-insensitive). Matched against the basename only — never
// against a full path.
var tilesetNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.[Pp][Nn][Gg]$`)

const (
	maxTilesetBytes = 4 << 20 // 4 MiB per file
	maxTilesetCount = 20      // per user
)

// pngMagic is the 8-byte PNG signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type tilesetEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"` // unix seconds
}

type tilesetListResp struct {
	Tilesets []tilesetEntry `json:"tilesets"`
	Active   string         `json:"active"`
}

func (m *Manager) listTilesets(uid string) ([]tilesetEntry, error) {
	dir := userTilesetDir(m.cfg.SavesRoot, uid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]tilesetEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !tilesetNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, tilesetEntry{
			Name:     e.Name(),
			Size:     info.Size(),
			Modified: info.ModTime().Unix(),
		})
	}
	return out, nil
}

// handleTilesets dispatches GET (list) and POST (upload) for /account/tilesets.
func (m *Manager) handleTilesets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		m.handleTilesetList(w, r)
	case http.MethodPost:
		m.handleTilesetUpload(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) handleTilesetList(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	entries, err := m.listTilesets(uid)
	if err != nil {
		log.Printf("tilesets: list for %s: %v", uid, err)
		http.Error(w, "list failed", http.StatusInternalServerError)
		return
	}
	active := ""
	if u, ok := m.store.ByUID(uid); ok {
		active = u.ActiveTileset
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tilesetListResp{Tilesets: entries, Active: active})
}

func (m *Manager) handleTilesetUpload(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	// Cap request body. +1 KiB headroom for multipart framing.
	r.Body = http.MaxBytesReader(w, r.Body, maxTilesetBytes+1024)
	if err := r.ParseMultipartForm(maxTilesetBytes + 1024); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	name := filepath.Base(hdr.Filename)
	if !tilesetNameRe.MatchString(name) {
		http.Error(w, "filename must match [A-Za-z0-9._-]+.png", http.StatusBadRequest)
		return
	}

	dir := userTilesetDir(m.cfg.SavesRoot, uid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("tilesets: mkdir for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	// Per-user count cap. Skip the check if this upload overwrites an
	// existing file (net-new count unchanged).
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		existing, err := m.listTilesets(uid)
		if err == nil && len(existing) >= maxTilesetCount {
			http.Error(w, fmt.Sprintf("tileset limit reached (%d)", maxTilesetCount), http.StatusBadRequest)
			return
		}
	}

	// Read into memory (≤4 MiB) so we can validate magic bytes before write.
	buf, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	if len(buf) < len(pngMagic) || !bytes.Equal(buf[:len(pngMagic)], pngMagic) {
		http.Error(w, "not a PNG file", http.StatusBadRequest)
		return
	}

	// Atomic write: tmp + rename.
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		log.Printf("tilesets: tempfile for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		cleanup()
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		log.Printf("tilesets: chmod for %s: %v", uid, err)
	}
	// Best-effort chown to the in-container DF user. Will fail without
	// CAP_CHOWN; that's fine because the parent dir is already 1000:1000
	// and new files inherit no specific owner requirement (DF only reads).
	_ = os.Chown(tmpPath, 1000, 1000)
	if err := os.Rename(tmpPath, target); err != nil {
		cleanup()
		log.Printf("tilesets: rename for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	info, _ := os.Stat(target)
	resp := tilesetEntry{Name: name}
	if info != nil {
		resp.Size = info.Size()
		resp.Modified = info.ModTime().Unix()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleTilesetItem dispatches /account/tilesets/<rest>:
//
//	POST   /account/tilesets/active     → set or clear active tileset
//	DELETE /account/tilesets/<name>     → delete one tileset
func (m *Manager) handleTilesetItem(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	rest := strings.TrimPrefix(r.URL.Path, "/account/tilesets/")
	if rest == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if rest == "active" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m.handleTilesetSetActive(w, r, uid)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m.handleTilesetDelete(w, r, uid, rest)
}

func (m *Manager) handleTilesetSetActive(w http.ResponseWriter, r *http.Request, uid string) {
	var body struct {
		Name string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if body.Name != "" {
		if !tilesetNameRe.MatchString(body.Name) {
			http.Error(w, "invalid name", http.StatusBadRequest)
			return
		}
		path := filepath.Join(userTilesetDir(m.cfg.SavesRoot, uid), body.Name)
		if _, err := os.Stat(path); err != nil {
			http.Error(w, "tileset not found", http.StatusNotFound)
			return
		}
	}

	if err := m.store.SetActiveTileset(uid, body.Name); err != nil {
		log.Printf("tilesets: set active for %s: %v", uid, err)
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleTilesetDelete(w http.ResponseWriter, r *http.Request, uid, name string) {
	if !tilesetNameRe.MatchString(name) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(userTilesetDir(m.cfg.SavesRoot, uid), name)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		log.Printf("tilesets: delete for %s: %v", uid, err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	// If we just deleted the active one, clear the selection.
	if u, ok := m.store.ByUID(uid); ok && u.ActiveTileset == name {
		if err := m.store.SetActiveTileset(uid, ""); err != nil {
			log.Printf("tilesets: clear active for %s: %v", uid, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
