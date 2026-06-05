package exporter

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// fileDest writes to a .partial file and atomically renames to the final
// path on successful Close. Aborted exports are cleaned up via Abort.
type fileDest struct {
	downloadDir string
	filename    string
	final       string
	partial     string
	file        *os.File
	// saveAs selects the Save-As path: final is taken verbatim (no
	// downloadDir join, no ContainedUnder confinement), with its own validation.
	saveAs bool
}

// NewFileDest returns a Destination that writes into downloadDir/filename.
// filename must already be sanitized via SanitizeComponent on each component.
func NewFileDest(downloadDir, filename string) Destination {
	return &fileDest{
		downloadDir: downloadDir,
		filename:    filename,
	}
}

func (d *fileDest) Open() (io.WriteCloser, string, error) {
	if d.saveAs {
		return d.openSaveAs()
	}

	final := filepath.Join(d.downloadDir, d.filename)
	if !ContainedUnder(d.downloadDir, final) {
		return nil, "", fmt.Errorf("exporter: refusing to write outside downloadDir: %s", final)
	}
	partial := final + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", err
	}
	d.final = final
	d.partial = partial
	d.file = f
	return &fileWriteCloser{dest: d}, final, nil
}

// openSaveAs validates the operator-chosen final path, then opens the .partial
// temp the same way as the confined path (O_EXCL on the temp only; mode 0o600).
func (d *fileDest) openSaveAs() (io.WriteCloser, string, error) {
	if err := d.validateSaveAsPath(); err != nil {
		return nil, "", err
	}
	partial := d.final + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("cannot create export file %s: %w", d.final, err)
	}
	d.partial = partial
	d.file = f
	return &fileWriteCloser{dest: d}, d.final, nil
}

// NewFileDestPath returns a "Save-As" Destination that writes to fullPath
// verbatim. Unlike NewFileDest it does NOT confine the path under a download
// dir (no ContainedUnder) and does NOT sanitize components (no
// SanitizeComponent) — the operator chose the path. It still writes atomically
// via a .partial temp + rename and creates the final file mode 0o600.
//
// Validation (empty/dir filename, control chars, and parent-dir existence as a
// directory) runs in Open and returns human-readable errors suitable for a
// toast. Writability is NOT probed here; it is enforced lazily when the temp
// file open fails.
func NewFileDestPath(fullPath string) Destination {
	dir, file := filepath.Split(fullPath)
	return &fileDest{
		downloadDir: dir,
		filename:    file,
		final:       fullPath,
		saveAs:      true,
	}
}

// validateSaveAsPath enforces the Save-As path rules: a non-empty filename that
// is not "." or "..", no control characters anywhere in the path, and a parent
// path that exists and is a directory. It does NOT probe writability; that is
// enforced lazily when the temp file open fails in openSaveAs.
func (d *fileDest) validateSaveAsPath() error {
	if d.filename == "" || d.filename == "." || d.filename == ".." {
		return fmt.Errorf("export path must include a file name: %s", d.final)
	}
	if hasControlChar(d.final) {
		return fmt.Errorf("export path must not contain control characters")
	}
	dir := filepath.Dir(d.final)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("export directory does not exist: %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("export directory is not a directory: %s", dir)
	}
	return nil
}

// hasControlChar reports whether s contains any control character (matching the
// connection-form path rule: tab is allowed, all other C0 controls plus CR/LF
// are rejected).
func hasControlChar(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') {
			return true
		}
	}
	return false
}

type fileWriteCloser struct {
	dest *fileDest
}

func (w *fileWriteCloser) Write(p []byte) (int, error) {
	return w.dest.file.Write(p)
}

// Close finalizes the export: flushes, then renames .partial → final.
// On error here, the caller should still invoke Abort to clean up.
func (w *fileWriteCloser) Close() error {
	if err := w.dest.file.Close(); err != nil {
		return err
	}
	return os.Rename(w.dest.partial, w.dest.final)
}

// Abort removes the .partial file. Safe to call even if Open failed
// (no-op when partial path is empty).
func (d *fileDest) Abort() {
	if d.partial == "" {
		return
	}
	if d.file != nil {
		_ = d.file.Close()
	}
	_ = os.Remove(d.partial)
}
