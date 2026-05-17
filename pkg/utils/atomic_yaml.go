package utils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// AtomicWriteYAML marshals v as YAML and atomically replaces path on POSIX
// file systems. It writes the marshaled bytes to path+".tmp" with the
// supplied mode, then renames the temp file onto path. On rename failure the
// temp file is removed best-effort and a wrapped error is returned.
//
// The parent directory of path is created with mode 0700 if it does not
// already exist. AtomicWriteYAML does NOT chmod an existing parent dir.
//
// This helper consolidates the atomic tmp+rename pattern shared by the
// AppState save path and SaveConnections in the config package.
func AtomicWriteYAML(fs afero.Fs, path string, v any, mode os.FileMode) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("atomic_yaml: marshal: %w", err)
	}
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("atomic_yaml: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := afero.WriteFile(fs, tmp, data, mode); err != nil {
		return fmt.Errorf("atomic_yaml: write tmp %s: %w", tmp, err)
	}
	if renameErr := fs.Rename(tmp, path); renameErr != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("atomic_yaml: rename %s → %s: %w (tmp removed best-effort)", tmp, path, renameErr)
	}
	return nil
}
