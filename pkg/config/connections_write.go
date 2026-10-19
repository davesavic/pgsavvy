package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/davesavic/pgsavvy/pkg/utils"
)

// ErrDuplicateConnectionName is returned by AppendConnection when the new
// profile's Name collides with an existing entry. Callers use errors.Is to
// detect the condition and prompt the user for a different name.
var ErrDuplicateConnectionName = errors.New("config: duplicate connection name")

// ErrConnectionNotFound is returned by UpdateConnection and DeleteConnection
// when no existing profile matches the supplied name.
var ErrConnectionNotFound = errors.New("config: connection not found")

// ErrConnectionsChanged means an edit cannot safely replace the loaded snapshot.
var ErrConnectionsChanged = errors.New("config: connections changed during edit; reload and retry")

// SaveConnections atomically writes conns to the YAML file at path using the
// wrapper form {connections: [...]} that LoadConnections (with KnownFields)
// expects. Publication uses an exclusive sibling temporary, mode 0600,
// and parent dir 0700. This is intentional whole-file replacement, not a merge.
//
// Pre-write safety: if any profile in conns has a non-empty inline Password
// field, a single WARN line is emitted to warnWriter mirroring the LOAD-time
// pattern in connections.go. The write proceeds regardless.
//
// NOTE: re-encoding via yaml.Marshal LOSES inline YAML comments present in
// the original file. Callers writing back user-authored connections.yml
// should expect comments to disappear after SaveConnections.
//
// On any error the returned message is run through session.RedactDSN as a
// defense-in-depth scrub for credentials that may have leaked into wrapped
// error strings.
func SaveConnections(fs afero.Fs, path string, conns []models.Connection) error {
	return withConnectionsWriteLock(fs, path, func(path string) error {
		return saveConnectionsLocked(fs, path, conns)
	})
}

func saveConnectionsLocked(fs afero.Fs, path string, conns []models.Connection) error {
	for _, conn := range conns {
		if err := conn.CLIAccess.Validate(); err != nil {
			return err
		}
	}
	if IsInlinePasswordPresent(conns) {
		_, _ = fmt.Fprintf(warnWriter,
			"config: dropping inline plaintext password from %d profile(s) on write to %s — migrate to password_command\n",
			countInlinePassword(conns), path,
		)
	}
	wrapper := connectionsFile{Connections: normalizeForWrite(conns)}
	if err := utils.AtomicWriteYAML(fs, path, wrapper, 0o600); err != nil {
		if errors.Is(err, ErrConnectionsChanged) {
			return ErrConnectionsChanged
		}
		return errors.New(session.RedactDSN(err.Error()))
	}
	return nil
}

// normalizeForWrite returns a COPY of conns with save-time hygiene applied, so
// the caller's in-memory slice/structs are never mutated:
//
//   - A5: inline Password is unconditionally cleared — it must never persist.
//   - A2: when ANY discrete field is set (Host/Port/User/Database/SSLMode), the
//     raw DSN is cleared, because the discrete fields are now the source of
//     truth (BuildPgxConfig assembles from them only when DSN is empty).
//
// DATA-LOSS (user-accepted, intentionally visible): A2 drops the ENTIRE DSN,
// including exotic libpq params it carried (application_name, search_path,
// connect_timeout, target_session_attrs, etc.). When a legacy dsn-only entry
// is edited to add discrete fields and saved, those params are lost — they are
// not reconstructed from the discrete fields.
func normalizeForWrite(conns []models.Connection) []models.Connection {
	out := make([]models.Connection, len(conns))
	copy(out, conns)
	for i := range out {
		out[i].Password = ""
		if hasDiscreteFields(out[i]) {
			out[i].DSN = ""
		}
	}
	return out
}

// hasDiscreteFields reports whether any discrete connection field is set.
func hasDiscreteFields(c models.Connection) bool {
	return c.Host != "" || c.Port != 0 || c.User != "" || c.Database != "" || c.SSLMode != ""
}

// AppendConnection loads the existing connections.yml at path (treating a
// missing file as an empty list), rejects duplicate Names with
// ErrDuplicateConnectionName, appends c, and writes the result via
// SaveConnections.
//
// On LoadConnections failure the error is returned unwrapped (per M10f): the
// caller is expected to surface it without silently overwriting a broken
// file.
func AppendConnection(fs afero.Fs, path string, c models.Connection) error {
	return withConnectionsWriteLock(fs, path, func(path string) error {
		return appendConnectionLocked(fs, path, c)
	})
}

func appendConnectionLocked(fs afero.Fs, path string, c models.Connection) error {
	existing, err := LoadConnections(fs, path)
	if err != nil {
		return err
	}
	for i := range existing {
		if existing[i].Name == c.Name {
			return ErrDuplicateConnectionName
		}
	}
	existing = append(existing, c)
	return saveConnectionsLocked(fs, path, existing)
}

// UpdateConnection loads the existing connections.yml at path, replaces the
// single entry whose Name == oldName with newConn, and writes the result via
// SaveConnections. All other entries round-trip untouched (only the matched
// index is swapped).
//
// Rename collisions are rejected: if newConn.Name differs from oldName and
// collides with a DIFFERENT existing entry, ErrDuplicateConnectionName is
// returned. Rename-to-self (newConn.Name == oldName) is permitted. If oldName
// is not found, ErrConnectionNotFound is returned.
//
// On LoadConnections failure the error is returned unwrapped (per M10f).
// Both CLI policies are preserved from disk, ignoring captured form values.
// Detected changes before publication return ErrConnectionsChanged without writing.
func UpdateConnection(fs afero.Fs, path, oldName string, newConn models.Connection) error {
	return UpdateConnectionWithCLISettings(fs, path, oldName, newConn, nil, nil)
}

// UpdateConnectionWithCLIAccess explicitly changes the CLI policy along with
// the other profile fields. A stale expected policy is rejected, not overwritten.
func UpdateConnectionWithCLIAccess(fs afero.Fs, path, oldName string, newConn models.Connection, expected models.CLIAccess) error {
	return UpdateConnectionWithCLISettings(fs, path, oldName, newConn, &expected, nil)
}

func updateConnectionLocked(fs afero.Fs, path, oldName string, newConn models.Connection, expectedPolicy *models.CLIAccess, expectedHistory *bool) error {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrConnectionNotFound
		}
		return err
	}
	info, err := fs.Stat(path)
	if err != nil {
		return err
	}
	existing, err := decodeConnections(data, info, path)
	if err != nil {
		return err
	}
	idx := -1
	for i := range existing {
		if existing[i].Name == oldName {
			if idx != -1 {
				return ErrDuplicateConnectionName
			}
			idx = i
			continue
		}
		if existing[i].Name == newConn.Name {
			return ErrDuplicateConnectionName
		}
	}
	if idx == -1 {
		return ErrConnectionNotFound
	}
	if expectedPolicy == nil {
		// Ordinary edits must not restore a revoked policy from a stale form.
		newConn.CLIAccess = existing[idx].CLIAccess
	} else if existing[idx].CLIAccess != *expectedPolicy {
		return ErrConnectionsChanged
	}
	if expectedHistory == nil {
		newConn.CLIHistory = existing[idx].CLIHistory
	} else if existing[idx].CLIHistory != *expectedHistory {
		return ErrConnectionsChanged
	}
	existing[idx] = newConn
	return saveConnectionsLocked(&connectionSnapshotFs{Fs: fs, path: path, data: data}, path, existing)
}

type connectionSnapshotFs struct {
	afero.Fs
	path string
	data []byte
}

func (fs *connectionSnapshotFs) Rename(oldname, newname string) error {
	if newname == fs.path {
		current, err := afero.ReadFile(fs.Fs, fs.path)
		if err != nil || !bytes.Equal(current, fs.data) {
			return ErrConnectionsChanged
		}
	}
	// An uncooperative external writer can still race this final check.
	return fs.Fs.Rename(oldname, newname)
}

// DeleteConnection loads the existing connections.yml at path and removes the
// single entry whose Name == name, writing the result via SaveConnections. If
// name is not found, ErrConnectionNotFound is returned. Deleting the last
// entry yields a valid {connections: []} file.
//
// On LoadConnections failure the error is returned unwrapped (per M10f).
func DeleteConnection(fs afero.Fs, path, name string) error {
	return withConnectionsWriteLock(fs, path, func(path string) error {
		return deleteConnectionLocked(fs, path, name)
	})
}

func deleteConnectionLocked(fs afero.Fs, path, name string) error {
	existing, err := LoadConnections(fs, path)
	if err != nil {
		return err
	}
	for i := range existing {
		if existing[i].Name != name {
			continue
		}
		existing = append(existing[:i], existing[i+1:]...)
		return saveConnectionsLocked(fs, path, existing)
	}
	return ErrConnectionNotFound
}

// IsInlinePasswordPresent reports whether any profile in conns carries a
// non-empty inline Password field. Exposed so the first-run UI can decide
// whether to surface a remediation hint, and used internally by
// SaveConnections to gate the WARN line.
func IsInlinePasswordPresent(conns []models.Connection) bool {
	for i := range conns {
		if conns[i].Password != "" {
			return true
		}
	}
	return false
}

// countInlinePassword returns the number of profiles carrying an inline
// Password. Used only for the WARN line in SaveConnections.
func countInlinePassword(conns []models.Connection) int {
	n := 0
	for i := range conns {
		if conns[i].Password != "" {
			n++
		}
	}
	return n
}
