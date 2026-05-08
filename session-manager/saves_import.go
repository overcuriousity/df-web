package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxImportUploadBytes  = 200 << 20 // 200 MiB upload cap
	maxImportExtractBytes = 1 << 30   // 1 GiB uncompressed cap (zip-bomb guard)
)

// regionDirRe matches existing on-disk region directories so we can pick the
// next free regionN slot.
var regionDirRe = regexp.MustCompile(`^region(\d+)$`)

// handleAccountImport accepts a zip or tar.gz containing exactly one DF
// region folder and installs it as a new region under the user's data/save/.
//
// Refuses while the user has a running container (would race DF's open save
// handles). Validates that the archive contains a single top-level directory
// with world.dat and world.sav, no path traversal, and an uncompressed total
// under maxImportExtractBytes.
func (m *Manager) handleAccountImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uid := uidFromContext(r.Context())

	// Refuse with the container still up. DF holds open file handles on the
	// active world; quietly merging files underneath it would corrupt the save.
	// Verify against the actual runtime, not just the map: stale entries left by
	// a DF crash or manual docker rm would otherwise wedge import indefinitely
	// until the next /play triggered ensureContainer cleanup.
	m.mu.Lock()
	ci, hasContainer := m.containers[uid]
	m.mu.Unlock()
	if hasContainer {
		if dockerIsRunning(ci.id) {
			http.Error(w, "stop your active session before importing a save", http.StatusConflict)
			return
		}
		// Stale entry: the container exited out from under us. Clear it so the
		// next /play spawns a fresh one, and let the import proceed.
		log.Printf("import: clearing stale container entry %s for %s (no longer running)", ci.id[:12], uid)
		m.mu.Lock()
		if cur, ok := m.containers[uid]; ok && cur.id == ci.id {
			delete(m.containers, uid)
		}
		m.mu.Unlock()
	}

	// 64 KiB headroom for multipart framing — boundary, headers, and the small
	// trailing CRLFs add up to a few KiB at most.
	r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes+64<<10)

	// Stream the multipart file part directly to disk. We avoid
	// ParseMultipartForm because it spools >memMax bytes to os.TempDir() —
	// which inside this container is /tmp, a 64 MiB tmpfs that's far smaller
	// than the 200 MiB upload cap. MultipartReader gives us a raw stream of
	// parts so we can copy straight into staging on the user's volume.
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "malformed upload", http.StatusBadRequest)
		return
	}

	// Per-request staging dir under the user's saves root. Two concurrent
	// imports for the same uid (double-click, two tabs) must not race on a
	// shared path or a defer-RemoveAll could unlink the other request's tmp
	// files mid-extract.
	userRoot := filepath.Join(m.cfg.SavesRoot, uid)
	if err := os.MkdirAll(userRoot, 0o700); err != nil {
		log.Printf("import: mkdir user root for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	stagingDir, err := os.MkdirTemp(userRoot, ".import-")
	if err != nil {
		log.Printf("import: staging tempdir for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	var (
		tmpUploadPath string
		uploadName    string
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "malformed upload", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		uploadName = part.FileName()
		tmpUpload, err := os.CreateTemp(stagingDir, "upload-*.bin")
		if err != nil {
			_ = part.Close()
			log.Printf("import: tempfile for %s: %v", uid, err)
			http.Error(w, "storage error", http.StatusInternalServerError)
			return
		}
		tmpUploadPath = tmpUpload.Name()
		if _, err := io.Copy(tmpUpload, part); err != nil {
			_ = part.Close()
			tmpUpload.Close()
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
				return
			}
			log.Printf("import: copy upload for %s: %v", uid, err)
			http.Error(w, "upload failed", http.StatusBadRequest)
			return
		}
		_ = part.Close()
		if err := tmpUpload.Close(); err != nil {
			log.Printf("import: close upload for %s: %v", uid, err)
			http.Error(w, "upload failed", http.StatusInternalServerError)
			return
		}
		break
	}
	if tmpUploadPath == "" {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}

	// Sniff format: PK\x03\x04 → zip; 1F 8B → gzip → assume tar.gz.
	format, err := detectArchiveFormat(tmpUploadPath)
	if err != nil {
		log.Printf("import: detect format for %s (%s): %v", uid, uploadName, err)
		http.Error(w, "unrecognised archive (expected .zip or .tar.gz)", http.StatusBadRequest)
		return
	}

	extractDir, err := os.MkdirTemp(stagingDir, "extract-*")
	if err != nil {
		log.Printf("import: extract tempdir for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	var (
		topLevel  string
		fileCount int
		byteCount int64
	)
	switch format {
	case "zip":
		topLevel, fileCount, byteCount, err = extractZip(tmpUploadPath, extractDir)
	case "tgz":
		topLevel, fileCount, byteCount, err = extractTarGz(tmpUploadPath, extractDir)
	default:
		http.Error(w, "unrecognised archive format", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("import: extract %s for %s: %v", format, uid, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate the extracted layout: top-level dir must contain world.dat and world.sav.
	regionSrc := filepath.Join(extractDir, topLevel)
	if !hasRegionFiles(regionSrc) {
		http.Error(w, "archive does not look like a DF save (missing world.dat / world.sav)", http.StatusBadRequest)
		return
	}

	// Pick the next free regionN slot under data/save/. ensureUserDirs covers
	// data/, config/, and tilesets/ at 1000:1000; we still need to chown the
	// save/ subdir explicitly because MkdirAll will create it root-owned for a
	// brand-new user importing before their first /play.
	if err := ensureUserDirs(m.cfg.SavesRoot, uid); err != nil {
		log.Printf("import: ensureUserDirs for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	saveRoot := filepath.Join(userDataDir(m.cfg.SavesRoot, uid), "save")
	if err := os.MkdirAll(saveRoot, 0o700); err != nil {
		log.Printf("import: mkdir save dir for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	if err := os.Chown(saveRoot, 1000, 1000); err != nil {
		log.Printf("import: chown save dir for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	target, err := nextFreeRegion(saveRoot)
	if err != nil {
		log.Printf("import: nextFreeRegion for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	targetPath := filepath.Join(saveRoot, target)

	// Move into place. os.Rename works because the staging dir is on the same
	// filesystem (under cfg.SavesRoot/<uid>/.import-staging).
	if err := os.Rename(regionSrc, targetPath); err != nil {
		log.Printf("import: rename %s -> %s: %v", regionSrc, targetPath, err)
		http.Error(w, "could not install save", http.StatusInternalServerError)
		return
	}

	// Match the ownership/permissions used by ensureUserDirs so DF (uid 1000
	// in-container) can read+write the new region. Treat failure as fatal and
	// roll back the install: a 200 here would leave the user with a region
	// they can't actually load.
	if err := chownTree(targetPath, 1000, 1000); err != nil {
		log.Printf("import: chown tree %s: %v", targetPath, err)
		if rmErr := os.RemoveAll(targetPath); rmErr != nil {
			log.Printf("import: rollback %s after chown failure: %v", targetPath, rmErr)
		}
		http.Error(w, "could not finalise save permissions", http.StatusInternalServerError)
		return
	}

	log.Printf("import: %s installed %s (%d files, %d bytes)", uid, target, fileCount, byteCount)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"region": target,
		"files":  fileCount,
		"bytes":  byteCount,
	})
}

// detectArchiveFormat sniffs the first 4 bytes of path to identify the archive.
func detectArchiveFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var head [4]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return "", err
	}
	switch {
	case head[0] == 'P' && head[1] == 'K' && head[2] == 0x03 && head[3] == 0x04:
		return "zip", nil
	case head[0] == 0x1f && head[1] == 0x8b:
		return "tgz", nil
	}
	return "", fmt.Errorf("unknown magic %02x %02x %02x %02x", head[0], head[1], head[2], head[3])
}

// validateEntryName rejects path traversal, absolute paths, and other
// shenanigans. Returns the cleaned forward-slash relative path on success.
func validateEntryName(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty entry name")
	}
	// Normalise to forward slashes (zip) and strip a single leading "./".
	// We reject leading "/" outright rather than stripping it — silently
	// turning "/region1/world.dat" into a relative path would be a surprising
	// rewrite and Copilot flagged it as inconsistent with this function's
	// "rejects absolute paths" claim.
	name = strings.ReplaceAll(name, `\`, `/`)
	name = strings.TrimPrefix(name, "./")
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute path in entry %q", name)
	}
	clean := path.Clean(name)
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("absolute path in entry %q", name)
	}
	// Reject ".." as a path *element* (so foo..bar is fine, foo/../bar is not).
	for _, comp := range strings.Split(clean, "/") {
		if comp == ".." {
			return "", fmt.Errorf("path traversal in entry %q", name)
		}
	}
	return clean, nil
}

// isMetadataEntry skips noise that filesystem GUIs add to archives — currently
// macOS Finder's __MACOSX/ resource forks and .DS_Store sentinels. Treating
// these as if they weren't in the archive lets us still enforce
// "exactly one top-level region folder" without rejecting macOS-zipped saves.
func isMetadataEntry(p string) bool {
	if strings.HasPrefix(p, "__MACOSX/") || p == "__MACOSX" {
		return true
	}
	base := path.Base(p)
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

// topLevelComponent returns the first path component of a forward-slash path.
func topLevelComponent(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// extractZip pulls path entries out of zipPath into destDir, returning the
// single top-level directory name plus counts. Errors include zip-bomb and
// path-traversal failures.
func extractZip(zipPath, destDir string) (string, int, int64, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("not a valid zip: %w", err)
	}
	defer zr.Close()

	var (
		topLevel  string
		fileCount int
		byteCount int64
	)
	for _, f := range zr.File {
		clean, err := validateEntryName(f.Name)
		if err != nil {
			return "", 0, 0, err
		}
		// Skip pure directory entries that resolve to "." after cleaning.
		if clean == "." || clean == "" {
			continue
		}
		// Skip macOS Finder noise so a zip that was built on a Mac doesn't
		// trip the "multiple top-level entries" check below.
		if isMetadataEntry(clean) {
			continue
		}
		root := topLevelComponent(clean)
		if topLevel == "" {
			topLevel = root
		} else if root != topLevel {
			return "", 0, 0, fmt.Errorf("archive has multiple top-level entries (%q and %q); expected one region folder", topLevel, root)
		}

		full := filepath.Join(destDir, filepath.FromSlash(clean))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(full, 0o700); err != nil {
				return "", 0, 0, err
			}
			continue
		}
		// Symlinks etc. — DF saves are plain files; reject anything else.
		if !f.FileInfo().Mode().IsRegular() {
			return "", 0, 0, fmt.Errorf("unsupported entry type for %q", clean)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return "", 0, 0, err
		}
		written, err := writeLimited(f.Open, full, maxImportExtractBytes-byteCount)
		if err != nil {
			return "", 0, 0, err
		}
		byteCount += written
		fileCount++
	}
	if topLevel == "" {
		return "", 0, 0, errors.New("archive is empty")
	}
	return topLevel, fileCount, byteCount, nil
}

// extractTarGz pulls entries out of a gzip+tar archive into destDir.
func extractTarGz(tgzPath, destDir string) (string, int, int64, error) {
	f, err := os.Open(tgzPath)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", 0, 0, fmt.Errorf("not a valid gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var (
		topLevel  string
		fileCount int
		byteCount int64
	)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, 0, fmt.Errorf("malformed tar: %w", err)
		}
		// PAX/GNU metadata records carry no file data we want; the headers
		// they describe will appear as the next entry. Skip without
		// validating the name (PAX records use synthetic ./PaxHeader paths).
		switch hdr.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		}
		clean, err := validateEntryName(hdr.Name)
		if err != nil {
			return "", 0, 0, err
		}
		if clean == "." || clean == "" {
			continue
		}
		if isMetadataEntry(clean) {
			continue
		}
		root := topLevelComponent(clean)
		if topLevel == "" {
			topLevel = root
		} else if root != topLevel {
			return "", 0, 0, fmt.Errorf("archive has multiple top-level entries (%q and %q); expected one region folder", topLevel, root)
		}
		full := filepath.Join(destDir, filepath.FromSlash(clean))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(full, 0o700); err != nil {
				return "", 0, 0, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return "", 0, 0, err
			}
			written, err := writeLimitedReader(tr, full, maxImportExtractBytes-byteCount)
			if err != nil {
				return "", 0, 0, err
			}
			byteCount += written
			fileCount++
		default:
			return "", 0, 0, fmt.Errorf("unsupported tar entry type %c for %q", hdr.Typeflag, clean)
		}
	}
	if topLevel == "" {
		return "", 0, 0, errors.New("archive is empty")
	}
	return topLevel, fileCount, byteCount, nil
}

// writeLimited opens a fresh reader via opener and copies up to limit bytes
// into dst. Returns ErrTooLarge if the source would exceed the budget.
func writeLimited(opener func() (io.ReadCloser, error), dst string, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("archive exceeds %d-byte uncompressed cap", maxImportExtractBytes)
	}
	rc, err := opener()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return writeLimitedReader(rc, dst, limit)
}

func writeLimitedReader(r io.Reader, dst string, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("archive exceeds %d-byte uncompressed cap", maxImportExtractBytes)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	// LimitReader+1 so we can detect overflow rather than silently truncating.
	n, err := io.Copy(out, io.LimitReader(r, limit+1))
	closeErr := out.Close()
	if err != nil {
		return 0, err
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if n > limit {
		_ = os.Remove(dst)
		return 0, fmt.Errorf("archive exceeds %d-byte uncompressed cap", maxImportExtractBytes)
	}
	return n, nil
}

// hasRegionFiles checks that the directory contains both world.dat and
// world.sav, the two files DF Premium writes for every region.
func hasRegionFiles(dir string) bool {
	for _, name := range []string{"world.dat", "world.sav"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// nextFreeRegion returns the lowest regionN that doesn't already exist under
// saveRoot. Callers don't need it to be the smallest free integer in absolute
// terms — DF tolerates gaps — but choosing min-free keeps the listing tidy.
func nextFreeRegion(saveRoot string) (string, error) {
	entries, err := os.ReadDir(saveRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "region1", nil
		}
		return "", err
	}
	used := make(map[int]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if mm := regionDirRe.FindStringSubmatch(e.Name()); mm != nil {
			n, _ := strconv.Atoi(mm[1])
			used[n] = true
		}
	}
	for n := 1; n < 10000; n++ {
		if !used[n] {
			return "region" + strconv.Itoa(n), nil
		}
	}
	return "", errors.New("no free region slot under 10000")
}

// chownTree applies uid:gid recursively. Best-effort — failures are returned
// but the caller may choose to log-and-continue.
func chownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, uid, gid)
	})
}
