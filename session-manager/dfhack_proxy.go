package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	dfhackTimeout      = 10 * time.Second // small/lightweight scripts (web-units, web-setlabor)
	dfhackTimeoutHeavy = 45 * time.Second // web-units-full, web-commit on big fortresses
)

// ansiEscapeRe matches CSI sequences (`\x1b[...m` etc). dfhack-run prefixes
// stdout with a color reset (`\x1b[0m`) regardless of TTY, which would break
// JSON.parse on the browser side.
var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// dfhackRun runs a DFHack command in the user's container and returns stdout.
// dfhack-run sends the command over the DFHack command socket (FIFO in the container).
//
// The absolute path /opt/df/dfhack-run matters: dfhack-run lives in the DFHack
// install tree which is /opt/df, and `docker exec` does NOT inherit the
// container's WORKDIR-relative PATH — it uses the default PATH only. Calling
// the bare name fails with "executable file not found", which surfaces as a
// 503 and the misleading "DFHack unavailable" message in the UI.
//
// Salvage rule for non-zero exits: dfhack-run can exit non-zero with only a
// benign glibc locale warning on stderr ("locale::facet::_S_create_c_locale
// name not valid") even when the script printed valid JSON to stdout. If the
// trimmed stdout starts with `{` or `[`, treat it as the answer; otherwise
// surface the stderr detail. LANG=C / LC_ALL=C in the exec environment
// suppress the glibc warning for images without full locale data.
func dfhackRun(uid string, args ...string) (string, error) {
	return dfhackRunWithTimeout(dfhackTimeout, uid, args...)
}

func dfhackRunWithTimeout(d time.Duration, uid string, args ...string) (string, error) {
	containerName := fmt.Sprintf("df-%s", uid)
	rt := containerRuntime()
	cmdArgs := append([]string{"exec", "-e", "LANG=C", "-e", "LC_ALL=C", containerName, "/opt/df/dfhack-run"}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, rt, cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	cleaned := strings.TrimSpace(ansiEscapeRe.ReplaceAllString(string(out), ""))
	if err != nil {
		// Salvage: if stdout looks like JSON, the script succeeded; the
		// non-zero exit was cosmetic.
		if len(cleaned) > 0 && (cleaned[0] == '{' || cleaned[0] == '[') {
			return cleaned, nil
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("dfhack-run %v: %s", args, detail)
	}
	return cleaned, nil
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

// handleDFHackUnitsFull calls web-units-full and proxies its JSON output.
// This is the rich payload powering /therapist (enums + roles + per-unit
// skills/attrs/labors/traits/needs/health/squad).
func (m *Manager) handleDFHackUnitsFull(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	_, ok := m.containers[uid]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "no active session", http.StatusNotFound)
		return
	}

	out, err := dfhackRunWithTimeout(dfhackTimeoutHeavy, uid, "web-units-full")
	if err != nil {
		http.Error(w, "dfhack unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, out)
}

// handleDFHackAnimals calls web-animals and proxies its JSON output.
func (m *Manager) handleDFHackAnimals(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	_, ok := m.containers[uid]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "no active session", http.StatusNotFound)
		return
	}

	out, err := dfhackRun(uid, "web-animals")
	if err != nil {
		http.Error(w, "dfhack unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, out)
}

// handleDFHackCommit forwards the request body (JSON) to web-commit.lua as a
// single positional argument. The Lua script returns {"applied": N, "errors": [...]}.
//
// Body is bounded at 1 MiB — far above any realistic batch (a thousand cell
// toggles is well under 100 KB).
func (m *Manager) handleDFHackCommit(w http.ResponseWriter, r *http.Request) {
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

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusBadRequest)
		return
	}
	// Validate JSON shape before invoking DFHack so we fail fast on garbage
	// rather than spending an exec round-trip.
	var probe any
	if err := json.Unmarshal(body, &probe); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	out, err := dfhackRunWithTimeout(dfhackTimeoutHeavy, uid, "web-commit", string(body))
	if err != nil {
		http.Error(w, "dfhack error: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
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
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
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
