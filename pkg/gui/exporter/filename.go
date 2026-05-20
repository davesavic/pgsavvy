package exporter

import (
	"path/filepath"
	"strings"
	"time"
)

const maxFilenameComponentBytes = 100

// SanitizeComponent cleans one filename segment per AD-15:
//   - replaces any char outside [A-Za-z0-9._-] with '_'
//   - strips leading '.' (no hidden files)
//   - truncates to 100 bytes
//
// Returns "_" if the input would sanitize to the empty string.
func SanitizeComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if len(out) > maxFilenameComponentBytes {
		out = out[:maxFilenameComponentBytes]
	}
	if out == "" {
		return "_"
	}
	return out
}

// DefaultFilename builds the auto-suggested export filename:
//
//	<connection>_<tableOrFingerprint>_<utc-ts>.<ext>
//
// Each component is sanitized via SanitizeComponent.
// ts format: 20060102T150405Z (UTC, RFC3339-ish, filesystem-safe).
func DefaultFilename(connection, tableOrFingerprint, ext string, now time.Time) string {
	conn := SanitizeComponent(connection)
	base := SanitizeComponent(tableOrFingerprint)
	ts := now.UTC().Format("20060102T150405Z")
	extClean := SanitizeComponent(ext)
	return conn + "_" + base + "_" + ts + "." + extClean
}

// ContainedUnder reports whether candidate is path-contained inside dir.
// Used by file destination to reject path-traversal attempts.
func ContainedUnder(dir, candidate string) bool {
	absDir, err1 := filepath.Abs(dir)
	absCand, err2 := filepath.Abs(candidate)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absCand)
	if err != nil {
		return false
	}
	if rel == "." {
		return false // candidate == dir; we want a child path
	}
	// rel must not start with ".."
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}
