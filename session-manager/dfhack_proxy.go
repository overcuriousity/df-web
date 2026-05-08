package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// dfhackRun runs a DFHack command in the user's container and returns stdout.
// dfhack-run sends the command over the DFHack command socket (FIFO in the container).
func dfhackRun(uid string, args ...string) (string, error) {
	containerName := fmt.Sprintf("df-%s", uid)
	// Build: docker exec df-<uid> dfhack-run <args...>
	rt := containerRuntime()
	cmdArgs := append([]string{"exec", containerName, "dfhack-run"}, args...)
	out, err := exec.Command(rt, cmdArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("dfhack-run %v: %w", args, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// handleDFHackUnits calls the web-units DFHack script and proxies its JSON output.
func (m *Manager) handleDFHackUnits(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	_, ok := m.containers[uid]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "no active session", http.StatusNotFound)
		return
	}

	out, err := dfhackRun(uid, "web-units")
	if err != nil {
		http.Error(w, "dfhack unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	// The script emits valid JSON; pass it straight through.
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, out)
}

// handleDFHackSetLabor calls web-setlabor to toggle one dwarf labor on/off.
// POST body: {"unit_id": 42, "labor": 0, "enabled": true}
func (m *Manager) handleDFHackSetLabor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	_, ok := m.containers[uid]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "no active session", http.StatusNotFound)
		return
	}

	var req struct {
		UnitID  int  `json:"unit_id"`
		Labor   int  `json:"labor"`
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	enabled := "0"
	if req.Enabled {
		enabled = "1"
	}
	_, err := dfhackRun(uid, "web-setlabor",
		strconv.Itoa(req.UnitID),
		strconv.Itoa(req.Labor),
		enabled,
	)
	if err != nil {
		http.Error(w, "dfhack error: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
