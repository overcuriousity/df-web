package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// xmlPrologRe matches an XML declaration so we can rewrite the encoding
// attribute. DF writes single-quoted CP437; we transcode to UTF-8 and need
// the prolog to agree, otherwise strict parsers may reject the document.
var xmlPrologRe = regexp.MustCompile(`(?i)^<\?xml[^?]*\?>`)

// cp437OEMTable is a per-byte CP437 → Unicode lookup. DF's font and the
// in-game text engine treat the C0 control range (0x01..0x1F, 0x7F) as
// printable OEM glyphs (smileys, hearts, arrows, suns) and embeds those
// bytes inside artifact, poetic-form, and musical-form names. Decoding
// them as raw control chars produces XML 1.0 garbage that DOMParser
// rejects, so we map them to the canonical OEM-glyph code points.
//
// TAB (0x09), LF (0x0A), and CR (0x0D) are preserved verbatim because DF
// uses them structurally (indentation, line breaks) — overriding them
// would corrupt the document layout.
var cp437OEMTable [256]rune

func init() {
	dec := charmap.CodePage437.NewDecoder()
	for i := 0; i < 256; i++ {
		out, err := dec.Bytes([]byte{byte(i)})
		if err != nil || len(out) == 0 {
			cp437OEMTable[i] = utf8.RuneError
			continue
		}
		r, _ := utf8.DecodeRune(out)
		cp437OEMTable[i] = r
	}
	// Override the C0 control range with OEM glyphs. These are the standard
	// IBM PC/CP437 hardware-font interpretations of the control bytes.
	oem := map[byte]rune{
		0x01: '☺', 0x02: '☻', 0x03: '♥', 0x04: '♦', 0x05: '♣', 0x06: '♠',
		0x07: '•', 0x08: '◘', 0x0B: '♂', 0x0C: '♀', 0x0E: '♫', 0x0F: '☼',
		0x10: '►', 0x11: '◄', 0x12: '↕', 0x13: '‼', 0x14: '¶', 0x15: '§',
		0x16: '▬', 0x17: '↨', 0x18: '↑', 0x19: '↓', 0x1A: '→', 0x1B: '←',
		0x1C: '∟', 0x1D: '↔', 0x1E: '▲', 0x1F: '▼', 0x7F: '⌂',
	}
	for b, r := range oem {
		cp437OEMTable[b] = r
	}
}

// cp437OEMReader streams a CP437/OEM byte source as UTF-8, applying the
// table above. Ten-line implementation rather than another transform
// chain because every input byte maps to exactly one rune; no state, no
// look-ahead.
type cp437OEMReader struct {
	src io.Reader
	in  [4096]byte
	buf []byte
}

func (r *cp437OEMReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		n, err := r.src.Read(r.in[:])
		if n == 0 {
			return 0, err
		}
		// Worst case 1 input byte → 3 UTF-8 bytes (no rune > U+FFFF here).
		out := make([]byte, 0, n*3)
		var enc [utf8.UTFMax]byte
		for i := 0; i < n; i++ {
			w := utf8.EncodeRune(enc[:], cp437OEMTable[r.in[i]])
			out = append(out, enc[:w]...)
		}
		r.buf = out
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

// legendsNameRe matches DF's legends export filenames. DF 50+/53.x writes
// "<savename>-<datestamp>-legends.xml" (vanilla) and, when DFHack is loaded,
// the extended "<savename>-<datestamp>-legends_plus.xml" — never a bare
// "legends.xml". The character class is restrictive on purpose: only chars
// DF itself produces in save/region names, no path separators or spaces.
var legendsNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+-legends(_plus)?\.xml$`)

// handleLegendsIndex lists legends XML exports for the user.
func (m *Manager) handleLegendsIndex(w http.ResponseWriter, r *http.Request) {
	uid := uidFromContext(r.Context())

	// DF Premium writes legends exports to the XDG data dir root,
	// which is the user's bind-mounted data directory.
	dataDir := userDataDir(m.cfg.SavesRoot, uid)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("legends: ReadDir %s: %v", dataDir, err)
		}
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
		if e.IsDir() || !legendsNameRe.MatchString(n) || strings.Contains(n, "..") {
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

	// Validate: must look like a legends*.xml filename, no path separators
	// or "..", no other shenanigans.
	if !legendsNameRe.MatchString(name) || strings.Contains(name, "..") {
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

	// Download keeps the raw bytes (CP437) so external tools that already
	// understand DF's native encoding stay happy.
	if r.URL.Query().Get("download") == "1" {
		modTime := time.Time{}
		if info, err := f.Stat(); err == nil {
			modTime = info.ModTime()
		}
		w.Header().Set("Content-Type", "application/xml")
		disp := mime.FormatMediaType("attachment", map[string]string{"filename": name})
		w.Header().Set("Content-Disposition", disp)
		http.ServeContent(w, r, name, modTime, f)
		return
	}

	// Inline view: transcode CP437 → UTF-8 and rewrite the prolog. DF emits
	// `<?xml version="1.0" encoding='CP437'?>` and then uses CP437 control
	// chars (0x01..0x1F have visible glyphs in DF) inside name strings. If
	// served as UTF-8, those bytes are either replaced with U+FFFD or fail
	// XML well-formedness — DOMParser bails partway through, leaving
	// historical_figure / entity / historical_event empty.
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	br := bufio.NewReader(&cp437OEMReader{src: f})

	// Pull the first 200 bytes to inspect the prolog. CP437 0x01..0x1F map
	// to printable Unicode glyphs that ARE legal in XML content (smileys,
	// suits, arrows, etc.), so once transcoded the body becomes valid.
	head, _ := br.Peek(200)
	if loc := xmlPrologRe.FindIndex(head); loc != nil {
		if _, err := br.Discard(loc[1]); err != nil {
			http.Error(w, "transcode error", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	}
	if _, err := io.Copy(w, br); err != nil {
		log.Printf("legends: copy %s: %v", name, err)
	}
}
