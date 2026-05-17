package common

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

// AppState is NOT safe for concurrent mutation. Callers MUST serialize writes
// externally for v1 (per-process single-writer invariant). Save takes a
// defensive snapshot but cannot protect against a mutator racing with the
// marshal — see Save's godoc for the contract.
type AppState struct {
	LastConnectionID         string                         `yaml:"last_connection_id"`
	RecentConnectionIDs      []string                       `yaml:"recent_connection_ids"`
	LastBufferUUIDs          map[string]string              `yaml:"last_buffer_uuids"`
	LastTheme                string                         `yaml:"last_theme"`
	LastResultViewMode       string                         `yaml:"last_result_view_mode"`
	StartupTipsSeenAt        time.Time                      `yaml:"startup_tips_seen_at"`
	Version                  string                         `yaml:"version"`
	StatementTimeoutOverride map[string]string              `yaml:"statement_timeout_override"`
	HiddenSchemas            map[string][]string            `yaml:"hidden_schemas"`
	HiddenColumns            map[string]map[string][]string `yaml:"hidden_columns"`
	LastSessionSettings      map[string]map[string]string   `yaml:"last_session_settings"`
}

// Save serializes the receiver to YAML and atomically replaces 'path' on POSIX
// file systems. The temp file is written at mode 0600. On Rename failure the
// temp file is removed best-effort. CONCURRENT WRITES TO THE RECEIVER DURING
// SAVE ARE NOT SAFE — yaml.Marshal iterates AppState's map fields; concurrent
// map writes will panic. Callers must hold an external lock or pass a
// defensive copy.
func (a *AppState) Save(fs afero.Fs, path string) error {
	data, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := afero.WriteFile(fs, tmp, data, 0o600); err != nil {
		return err
	}
	if renameErr := fs.Rename(tmp, path); renameErr != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("rename %s → %s: %w (tmp removed best-effort)", tmp, path, renameErr)
	}
	return nil
}

// Load reads the YAML file at 'path' into the receiver. A missing file is
// treated as a first-run condition: the receiver is left at zero value and
// nil is returned. Any other read error, or an unmarshal error on a present
// file, is returned to the caller.
func (a *AppState) Load(fs afero.Fs, path string) error {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil
		}
		return err
	}
	return yaml.Unmarshal(data, a)
}
