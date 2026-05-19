package editor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/afero"
)

// uuidRE matches the canonical hyphenated 8-4-4-4-12 hex layout used by
// RFC 4122 UUIDs. Strict matching is the boundary against path-traversal:
// AppState.GetOrCreateBufferUUID produces a v4 UUID; any persisted file
// whose name fails this regex is silently skipped by ListBuffers, and any
// caller-supplied uuid that fails the regex makes bufferPathFor return ""
// so the caller (Save / Load) can short-circuit.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// bufferDirFor returns the per-connection directory rooted at stateDir
// (no UUID suffix). Empty stateDir or empty connID returns "" so callers
// short-circuit. The connection-ID is hashed (sha256, first 8 bytes →
// 16 hex chars) so reading the directory tree leaks neither the raw
// connection name nor the user's profile labels.
func bufferDirFor(stateDir, connID string) string {
	if stateDir == "" || connID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(connID))
	return filepath.Join(stateDir, "buffers", hex.EncodeToString(sum[:8]))
}

// bufferPathFor returns the on-disk path for one (connID, uuid) buffer.
// Returns "" when connID is empty or uuid fails the strict UUIDv4 regex
// — the empty-string sentinel keeps both Save and Load nil-safe without
// duplicating the validation at every call site.
func bufferPathFor(stateDir, connID, uuid string) string {
	if !uuidRE.MatchString(uuid) {
		return ""
	}
	dir := bufferDirFor(stateDir, connID)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, uuid+".sql")
}

// SaveBufferLines writes the supplied Lines snapshot to the per-connID
// buffer file as raw `.sql` text (no struct serialization — Cursor /
// Selection / History / Marks / Jumps are NOT persisted). The write is
// atomic: a sibling `.tmp` is written at mode 0o600, then renamed to
// the final path; a failed rename removes the tmp best-effort.
//
// Empty connID or invalid uuid is a no-op (returns nil). lines may be
// nil; the resulting file is empty in that case.
func SaveBufferLines(fs afero.Fs, stateDir, connID, uuid string, lines []Line) error {
	return SaveBufferContent(fs, stateDir, connID, uuid, joinLines(lines))
}

// SaveBufferContent is the string-content variant of SaveBufferLines.
// QueryEditorContext.HandleFocusLost takes a Buffer.String() snapshot on
// the MainLoop, so the worker-dispatched save can skip the Lines→string
// round-trip and write the string directly. Semantics otherwise identical
// to SaveBufferLines (atomic write, 0o600 file, 0o700 dir, raw `.sql`).
func SaveBufferContent(fs afero.Fs, stateDir, connID, uuid, content string) error {
	if fs == nil {
		return errors.New("editor: SaveBufferContent: nil fs")
	}
	path := bufferPathFor(stateDir, connID, uuid)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("editor: mkdir buffer dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := afero.WriteFile(fs, tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("editor: write buffer tmp: %w", err)
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("editor: rename buffer tmp: %w (tmp removed best-effort)", err)
	}
	return nil
}

// LoadBuffer reads the buffer file for (connID, uuid) and returns a
// hydrated *Buffer. A missing file is NOT an error — a fresh empty
// Buffer is returned so HandleFocus can hand the same handle to wwd
// motion code without distinguishing "no prior buffer" from "buffer
// exists, body empty". ConnectionID / UUID / Path are always set;
// Cursor lands at (0,0) and Dirty stays false.
//
// Empty connID or invalid uuid returns a fresh empty Buffer (no path
// to read) so callers don't need to pre-validate.
func LoadBuffer(fs afero.Fs, stateDir, connID, uuid string) (*Buffer, error) {
	buf := NewBuffer()
	buf.ConnectionID = connID
	buf.UUID = uuid
	if fs == nil {
		return buf, nil
	}
	path := bufferPathFor(stateDir, connID, uuid)
	if path == "" {
		return buf, nil
	}
	buf.Path = path
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return buf, nil
		}
		return buf, fmt.Errorf("editor: read buffer: %w", err)
	}
	buf.Lines = splitContentLines(string(data))
	buf.Cursor = Position{Line: 0, Col: 0}
	buf.Dirty = false
	return buf, nil
}

// ListBuffers returns the UUIDs of every buffer file persisted for
// connID. Files whose name fails the UUIDv4 regex are filtered out
// (the strict-validation invariant). Returns (nil, nil) when connID
// is empty or the directory is missing — both are first-run conditions
// rather than errors.
func ListBuffers(fs afero.Fs, stateDir, connID string) ([]string, error) {
	if fs == nil {
		return nil, nil
	}
	dir := bufferDirFor(stateDir, connID)
	if dir == "" {
		return nil, nil
	}
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("editor: list buffers: %w", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		uuid := strings.TrimSuffix(name, ".sql")
		if !uuidRE.MatchString(uuid) {
			continue
		}
		out = append(out, uuid)
	}
	return out, nil
}

// joinLines flattens a Lines snapshot to a single string for raw-`.sql`
// on-disk format. Lines are joined with `\n`. A nil or empty slice
// returns "".
func joinLines(lines []Line) string {
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(string(l.Runes))
	}
	return sb.String()
}

// splitContentLines splits raw on-disk content into Lines, mirroring
// the inverse of joinLines. An empty input returns nil so the Buffer
// starts truly empty (NewBuffer's Lines field stays nil) rather than
// containing a single empty line. A trailing `\n` produces a trailing
// blank line.
func splitContentLines(s string) []Line {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	out := make([]Line, len(parts))
	for i, p := range parts {
		out[i] = Line{Runes: []rune(p)}
	}
	return out
}
