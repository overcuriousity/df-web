package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleLegendsIndex lists legends XML exports for the user.
func (m *Manager) handleLegendsIndex(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	// DF Premium writes legends exports to the XDG data dir root,
	// which is the user's bind-mounted data directory.
	dataDir := userDataDir(m.cfg.SavesRoot, uid)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	type legendsFile struct {
		Name    string    `json:"name"`
		ModTime time.Time `json:"mod_time"`
		SizeKB  int64     `json:"size_kb"`
	}
	var files []legendsFile
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "legends") || !strings.HasSuffix(n, ".xml") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, legendsFile{
			Name:    n,
			ModTime: info.ModTime(),
			SizeKB:  info.Size() / 1024,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

// handleLegendsXML serves a specific legends XML file for download or browser rendering.
func (m *Manager) handleLegendsXML(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())
	name := r.URL.Query().Get("file")

	// Validate: must look like a legends*.xml filename, no path separators.
	if name == "" || strings.ContainsAny(name, "/\\") ||
		!strings.HasPrefix(name, "legends") || !strings.HasSuffix(name, ".xml") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	path := filepath.Join(userDataDir(m.cfg.SavesRoot, uid), name)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "legends file not found", http.StatusNotFound)
		} else {
			http.Error(w, "cannot open legends file", http.StatusInternalServerError)
		}
		return
	}
	defer f.Close()

	info, _ := f.Stat()
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}
