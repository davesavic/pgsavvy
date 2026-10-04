package utils

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// AtomicWriteYAML marshals v as YAML and atomically replaces path on POSIX
// file systems. It exclusively creates a temporary file in the destination
// directory with the supplied mode, closes it, then renames it onto path.
// On failure only that writer's temporary file is removed best-effort.
//
// The parent directory of path is created with mode 0700 if it does not
// already exist. AtomicWriteYAML does NOT chmod an existing parent dir.
//
// Concurrent saves publish complete snapshots; they do not merge updates.
func AtomicWriteYAML(fs afero.Fs, path string, v any, mode os.FileMode) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("atomic_yaml: marshal: %w", err)
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("atomic_yaml: mkdir %s: %w", filepath.Dir(path), err)
	}
	var file afero.File
	var tmp string
	// Bound retries for custom filesystems that always report a collision.
	for range 100 {
		tmp = filepath.Join(filepath.Dir(path), ".pgsavvy-yaml-"+rand.Text()+".tmp")
		file, err = fs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("atomic_yaml: create tmp %s: %w", tmp, err)
	}
	published := false
	defer func() {
		if !published {
			_ = fs.Remove(tmp)
		}
	}()
	n, writeErr := file.Write(data)
	if writeErr == nil && n < len(data) {
		writeErr = io.ErrShortWrite
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("atomic_yaml: write tmp %s: %w", tmp, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("atomic_yaml: close tmp %s: %w", tmp, closeErr)
	}
	if renameErr := fs.Rename(tmp, path); renameErr != nil {
		return fmt.Errorf("atomic_yaml: rename %s → %s: %w (tmp removed best-effort)", tmp, path, renameErr)
	}
	published = true
	return nil
}
