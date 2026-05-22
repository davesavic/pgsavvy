package common

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// recentConnectionsCap is the maximum number of entries retained in
// AppState.RecentConnectionIDs. Older entries fall off the tail on each
// push. dbsavvy-56u.1.
const recentConnectionsCap = 10

// PushRecentConnectionID inserts connID at the front of recent, dedupes
// any prior occurrence, and caps the result at recentConnectionsCap.
// Empty connID returns recent unchanged so callers can pipe Connect
// failures through without a guard. The returned slice is always a
// freshly allocated value (never aliases recent) so callers may assign
// it directly into AppState.RecentConnectionIDs under MutateAndSave
// without further copying. dbsavvy-56u.1.
func PushRecentConnectionID(recent []string, connID string) []string {
	if connID == "" {
		out := make([]string, len(recent))
		copy(out, recent)
		return out
	}
	out := make([]string, 0, len(recent)+1)
	out = append(out, connID)
	for _, id := range recent {
		if id == connID {
			continue
		}
		out = append(out, id)
		if len(out) >= recentConnectionsCap {
			break
		}
	}
	return out
}

// connIDHashKey returns the 16-hex-char key used to index LastBufferUUIDs:
// hex(sha256(connID)[:8]). Empty connID returns "".
func connIDHashKey(connID string) string {
	if connID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(connID))
	return hex.EncodeToString(sum[:8])
}

// GetOrCreateBufferUUID returns the persistent buffer UUID associated with
// connID, generating a fresh v4 UUID and storing it on first call. Empty
// connID returns "" without mutating state. Caller is responsible for
// serialising mutation (AppState is not safe for concurrent writes).
func (a *AppState) GetOrCreateBufferUUID(connID string) string {
	key := connIDHashKey(connID)
	if key == "" {
		return ""
	}
	if a.LastBufferUUIDs == nil {
		a.LastBufferUUIDs = make(map[string]string)
	}
	if u, ok := a.LastBufferUUIDs[key]; ok && u != "" {
		return u
	}
	u, err := newUUIDv4()
	if err != nil {
		return ""
	}
	a.LastBufferUUIDs[key] = u
	return u
}

// newUUIDv4 generates a canonical RFC 4122 v4 UUID using crypto/rand. The
// version (0x40) and variant (0x80) bits are set on bytes 6 and 8.
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
