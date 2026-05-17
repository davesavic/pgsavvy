package config

import (
	"errors"
	"fmt"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
	"github.com/davesavic/dbsavvy/pkg/utils"
)

// ErrDuplicateConnectionName is returned by AppendConnection when the new
// profile's Name collides with an existing entry. Callers use errors.Is to
// detect the condition and prompt the user for a different name.
var ErrDuplicateConnectionName = errors.New("config: duplicate connection name")

// SaveConnections atomically writes conns to the YAML file at path using the
// wrapper form {connections: [...]} that LoadConnections (with KnownFields)
// expects. The file is written via pkg/utils.AtomicWriteYAML — temp file at
// path+".tmp", rename onto path, mode 0600, parent dir 0700.
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
	if IsInlinePasswordPresent(conns) {
		_, _ = fmt.Fprintf(warnWriter,
			"config: rewriting %s; %d profile(s) still carry plaintext password — migrate to password_command\n",
			path, countInlinePassword(conns),
		)
	}
	wrapper := connectionsFile{Connections: conns}
	if err := utils.AtomicWriteYAML(fs, path, wrapper, 0o600); err != nil {
		return errors.New(session.RedactDSN(err.Error()))
	}
	return nil
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
	return SaveConnections(fs, path, existing)
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
