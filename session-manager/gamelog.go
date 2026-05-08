package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// gamelogEvent is one classified line from gamelog.txt.
type gamelogEvent struct {
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`
	Raw  string    `json:"raw"`
}

// kindRules maps a line classification to a regex that matches it.
// First matching rule wins; unmatched lines get kind "other".
var kindRules = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"siege", regexp.MustCompile(`(?i)\b(siege|attack|invade|horde|ambush|snatcher)\b`)},
	{"death", regexp.MustCompile(`(?i)\b(has (died|been slain|drowned|starved|bled to death)|struck down|killed)\b`)},
	{"artifact", regexp.MustCompile(`(?i)\b(has created a masterwork|has claimed|legendary artifact|named .* the )\b`)},
	{"mood", regexp.MustCompile(`(?i)\b(fell into a (fey|secretive|possessed|melancholy|berserk|macabre) mood|is taken by a strange mood)\b`)},
	{"migration", regexp.MustCompile(`(?i)\b(migrants have arrived|a group of migrants|caravan)\b`)},
	{"season", regexp.MustCompile(`(?i)\b(spring has arrived|summer has arrived|autumn has arrived|winter has arrived|year \d+)\b`)},
}

func classifyLine(line string) string {
	for _, r := range kindRules {
		if r.re.MatchString(line) {
			return r.kind
		}
	}
	return "other"
}

// sessionLog holds the in-memory event ring and subscriber channels for one
// user's running container.
type sessionLog struct {
	mu        sync.Mutex
	events    []gamelogEvent // ring buffer, capped at maxEvents
	subs      []chan gamelogEvent
	done      chan struct{}
}

const maxEvents = 500
const replayCount = 50 // events replayed to new SSE subscribers on connect

func newSessionLog() *sessionLog {
	return &sessionLog{done: make(chan struct{})}
}

func (sl *sessionLog) stop() {
	select {
	case <-sl.done:
	default:
		close(sl.done)
	}
}

func (sl *sessionLog) append(ev gamelogEvent) {
	sl.mu.Lock()
	sl.events = append(sl.events, ev)
	if len(sl.events) > maxEvents {
		sl.events = sl.events[len(sl.events)-maxEvents:]
	}
	subs := make([]chan gamelogEvent, len(sl.subs))
	copy(subs, sl.subs)
	sl.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // slow subscriber: drop rather than block
		}
	}
}

func (sl *sessionLog) subscribe() chan gamelogEvent {
	ch := make(chan gamelogEvent, 64)
	sl.mu.Lock()
	// replay recent events to the new subscriber
	start := 0
	if len(sl.events) > replayCount {
		start = len(sl.events) - replayCount
	}
	for _, ev := range sl.events[start:] {
		ch <- ev
	}
	sl.subs = append(sl.subs, ch)
	sl.mu.Unlock()
	return ch
}

func (sl *sessionLog) unsubscribe(ch chan gamelogEvent) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	for i, s := range sl.subs {
		if s == ch {
			sl.subs = append(sl.subs[:i], sl.subs[i+1:]...)
			return
		}
	}
}

// startGamelogTailer launches a goroutine that reads new lines appended to
// the gamelog.txt in the user's bind-mounted data dir and broadcasts them
// as classified events on sl.
func startGamelogTailer(sl *sessionLog, savesRoot, uid string) {
	go func() {
		path := filepath.Join(userDataDir(savesRoot, uid), "gamelog.txt")

		// Wait up to 30 s for the file to appear (container takes a moment to start).
		var f *os.File
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var err error
			f, err = os.Open(path)
			if err == nil {
				break
			}
			select {
			case <-sl.done:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		if f == nil {
			log.Printf("gamelog: %s never appeared for user %s", path, uid)
			return
		}
		defer f.Close()

		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			select {
			case <-sl.done:
				return
			case <-time.After(500 * time.Millisecond):
			}

			n, err := f.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				for {
					idx := strings.IndexByte(string(buf), '\n')
					if idx < 0 {
						break
					}
					line := strings.TrimRight(string(buf[:idx]), "\r")
					buf = buf[idx+1:]
					if line == "" {
						continue
					}
					sl.append(gamelogEvent{
						TS:   time.Now(),
						Kind: classifyLine(line),
						Raw:  line,
					})
				}
			}
			if err != nil && err != io.EOF {
				log.Printf("gamelog: read error for user %s: %v", uid, err)
				return
			}
		}
	}()
}

// handleTimeline serves Server-Sent Events for the user's gamelog stream.
func (m *Manager) handleTimeline(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	m.mu.Lock()
	ci, ok := m.containers[uid]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "no active session", http.StatusNotFound)
		return
	}
	_ = ci

	sl := m.getOrCreateSessionLog(uid)
	ch := sl.subscribe()
	defer sl.unsubscribe(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send a keepalive comment every 15 s so proxies don't close idle connections.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sl.done:
			fmt.Fprintf(w, "event: session-ended\ndata: {}\n\n")
			flusher.Flush()
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// sessionLogs holds per-uid session log instances.
var (
	sessionLogsMu sync.Mutex
	sessionLogs   = map[string]*sessionLog{}
)

func (m *Manager) getOrCreateSessionLog(uid string) *sessionLog {
	sessionLogsMu.Lock()
	defer sessionLogsMu.Unlock()
	if sl, ok := sessionLogs[uid]; ok {
		return sl
	}
	sl := newSessionLog()
	sessionLogs[uid] = sl
	startGamelogTailer(sl, m.cfg.SavesRoot, uid)
	return sl
}

func (m *Manager) stopSessionLog(uid string) {
	sessionLogsMu.Lock()
	sl, ok := sessionLogs[uid]
	if ok {
		delete(sessionLogs, uid)
	}
	sessionLogsMu.Unlock()
	if ok {
		sl.stop()
	}
}
