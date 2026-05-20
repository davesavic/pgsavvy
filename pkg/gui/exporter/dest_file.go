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
