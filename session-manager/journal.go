package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// savesDir returns the host-side path to the user's DF save directory.
func (m *Manager) savesDir(uid string) string {
	return filepath.Join(userDataDir(m.cfg.SavesRoot, uid), "save")
}

// validSave returns true if region is a real save directory name and not a path traversal attempt.
func (m *Manager) validSave(uid, region string) bool {
	if region == "" || strings.ContainsAny(region, "/\\") {
		return false
	}
	if !strings.HasPrefix(region, "region") {
		return false
	}
	info, err := os.Stat(filepath.Join(m.savesDir(uid), region))
	return err == nil && info.IsDir()
}

// handleSaves lists the user's save directories with mtime and journal status.
func (m *Manager) handleSaves(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	dir := m.savesDir(uid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
			return
		}
		http.Error(w, "cannot read saves", http.StatusInternalServerError)
		return
	}

	type saveInfo struct {
		Name       string    `json:"name"`
		ModTime    time.Time `json:"mod_time"`
		HasJournal bool      `json:"has_journal"`
	}
	var saves []saveInfo
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "region") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		_, journalErr := os.Stat(filepath.Join(dir, e.Name(), "journal.md"))
		saves = append(saves, saveInfo{
			Name:       e.Name(),
			ModTime:    info.ModTime(),
			HasJournal: journalErr == nil,
		})
	}

	// Sort most-recently-modified first so the frontend auto-selects the active fortress.
	sort.Slice(saves, func(i, j int) bool { return saves[i].ModTime.After(saves[j].ModTime) })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(saves)
}

// handleJournal handles GET and PUT for per-save markdown journals.
func (m *Manager) handleJournal(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	region := r.URL.Query().Get("save")
	if !m.validSave(uid, region) {
		http.Error(w, "invalid save", http.StatusBadRequest)
		return
	}
	path := filepath.Join(m.savesDir(uid), region, "journal.md")

	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		if err != nil {
			http.Error(w, "cannot read journal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write(data)

	case http.MethodPut:
		const maxSize = 1 << 20 // 1 MiB
		body := http.MaxBytesReader(w, r.Body, maxSize)
		content, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, "journal too large (max 1 MiB)", http.StatusRequestEntityTooLarge)
			return
		}

		// Per-request temp file: a fixed path+".tmp" lets two concurrent PUTs
		// (autosave + manual save, two open tabs) race on the same name and
		// silently lose one writer's content.
		tmp, err := os.CreateTemp(filepath.Dir(path), ".journal-*.tmp")
		if err != nil {
			http.Error(w, "cannot write journal", http.StatusInternalServerError)
			return
		}
		tmpPath := tmp.Name()
		if _, err := tmp.Write(content); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			http.Error(w, "cannot write journal", http.StatusInternalServerError)
			return
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			http.Error(w, "cannot write journal", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmpPath, path); err != nil {
			os.Remove(tmpPath)
			http.Error(w, "cannot save journal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
