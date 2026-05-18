package editor

import "strings"

// SplitStatements splits buf on ';' and returns the non-empty segments
// without trimming surrounding whitespace beyond the semicolon itself.
// The split is intentionally naive — it has no awareness of string
// literals, line comments, dollar-quoted blocks, or escaped
// semicolons. The SQL-aware splitter ships with the vim editor in
// epic dbsavvy-wwd (E9).
//
// Documented limitation: a SQL string literal containing ';' is split
// at the ';' inside the literal. Until E9 ships, users typing
// statements like `SELECT ';'` will see two segments rather than one;
// the QueryEditorController surfaces this caveat in <leader>r's help
// text.
//
// An empty input or an input consisting entirely of whitespace and
// semicolons returns a nil slice. A trailing empty segment after the
// final ';' is dropped; intermediate empty segments are preserved
// (callers may treat them as no-ops, but SplitStatements does not
// pre-filter them).
func SplitStatements(buf string) []string {
	if strings.TrimSpace(buf) == "" {
		return nil
	}
	parts := strings.Split(buf, ";")
	// Drop a single trailing empty segment from a buffer that ended in
	// ';' (the common case). The non-empty-only filter below would
	// also drop it, but keeping that filter narrow lets us preserve
	// genuinely-empty intermediate segments for callers that want to
	// flag them.
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// StatementAt returns the statement under the byte offset off inside
// buf. The result is the substring between the nearest ';' on the
// left of off (exclusive) and the nearest ';' on the right (also
// exclusive). off is clamped into [0, len(buf)].
//
// Returns "" when buf is empty or when the byte under off is itself a
// ';' AND there is no non-empty preceding segment — callers should
// treat "" as "no statement under cursor".
func StatementAt(buf string, off int) string {
	if buf == "" {
		return ""
	}
	if off < 0 {
		off = 0
	}
	if off > len(buf) {
		off = len(buf)
	}
	left := strings.LastIndexByte(buf[:off], ';')
	right := strings.IndexByte(buf[off:], ';')

	start := 0
	if left >= 0 {
		start = left + 1
	}
	end := len(buf)
	if right >= 0 {
		end = off + right
	}
	if start >= end {
		// Cursor sits on a ';' with no content immediately preceding.
		// Try the segment ending at the ';' on the left (if any) so
		// "SELECT 1;|" reports "SELECT 1".
		if left >= 0 {
			prevStart := strings.LastIndexByte(buf[:left], ';') + 1
			candidate := strings.TrimSpace(buf[prevStart:left])
			if candidate != "" {
				return candidate
			}
		}
		return ""
	}
	out := strings.TrimSpace(buf[start:end])
	return out
}
