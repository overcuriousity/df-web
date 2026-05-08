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
	m.mu.Lock()
	_, hasContainer := m.containers[uid]
	m.mu.Unlock()
	if hasContainer {
		http.Error(w, "stop your active session before importing a save", http.StatusConflict)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		// MaxBytesReader returns an *http.MaxBytesError — surface 413 in that case.
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "malformed upload", http.StatusBadRequest)
		return
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Stage the upload to disk under the user's directory. /tmp on this
	// container is a 64 MiB tmpfs — too small for a 200 MiB save.
	stagingDir := filepath.Join(m.cfg.SavesRoot, uid, ".import-staging")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		log.Printf("import: mkdir staging for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	// Best-effort cleanup of any previous staging artifacts.
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	tmpUpload, err := os.CreateTemp(stagingDir, "upload-*.bin")
	if err != nil {
		log.Printf("import: tempfile for %s: %v", uid, err)
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	tmpUploadPath := tmpUpload.Name()
	if _, err := io.Copy(tmpUpload, file); err != nil {
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
	if err := tmpUpload.Close(); err != nil {
		log.Printf("import: close upload for %s: %v", uid, err)
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}

	// Sniff format: PK\x03\x04 → zip; 1F 8B → gzip → assume tar.gz.
	format, err := detectArchiveFormat(tmpUploadPath)
	if err != nil {
		log.Printf("import: detect format for %s (%s): %v", uid, hdr.Filename, err)
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

	// Pick the next free regionN slot under data/save/.
	saveRoot := filepath.Join(userDataDir(m.cfg.SavesRoot, uid), "save")
	if err := os.MkdirAll(saveRoot, 0o700); err != nil {
		log.Printf("import: mkdir save dir for %s: %v", uid, err)
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
	// in-container) can read+write the new region.
	if err := chownTree(targetPath, 1000, 1000); err != nil {
		log.Printf("import: chown tree %s: %v", targetPath, err)
		// Non-fatal: the data is in place. Surface a warning but still 200.
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
	// Normalise to forward slashes (zip) and strip leading "./" / "/".
	name = strings.ReplaceAll(name, `\`, `/`)
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimPrefix(name, "/")
	if strings.Contains(name, "..") {
		return "", fmt.Errorf("path traversal in entry %q", name)
	}
	clean := path.Clean(name)
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unsafe entry path %q", name)
	}
	return clean, nil
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
		clean, err := validateEntryName(hdr.Name)
		if err != nil {
			return "", 0, 0, err
		}
		if clean == "." || clean == "" {
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
