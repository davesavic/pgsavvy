package editor

import (
	"context"
	"regexp"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// SchemaSourceName is the stable Name() for the schema-aware
// completion source. Z1 wiring references this string when
// constructing the engine; tests assert on it too.
const SchemaSourceName = "schema"

// SchemaSourcePriority is the default Priority() the schema source
// declares. Higher than keywords/history (C4 wires those lower) so
// schema-driven hits win ties in Engine dedupe. The exact value is
// not load-bearing for C2 — Z1 may rewire from a central registry.
const SchemaSourcePriority = 80

// SessionProvider returns the live drivers.Session backing the
// active query editor. Returns nil when there is no live session —
// SchemaSource then returns an empty slice (epic ADR for "active-
// session-required empty state": Suggestions popup shows "no active
// connection" rather than triggering the source).
type SessionProvider func() drivers.Session

// SchemaProvider returns the current schema name used to resolve
// unqualified `FROM` / `JOIN` table lookups. Empty string is treated
// as "no schema selected" — the source returns empty for table-context
// matches in that case (column-context still works when the user types
// `schema.table.` because ListColumns takes the schema verbatim from
// the matched prefix... v1 does not yet support that — see scope note).
type SchemaProvider func() string

// SchemaSource implements Source by translating cursor-context regex
// matches on the line-up-to-cursor into table / column suggestions
// from the live drivers.Session.
//
// Detection scope (v1):
//   - trailing FROM / JOIN / INNER JOIN / LEFT JOIN / RIGHT JOIN /
//     CROSS JOIN / UPDATE / INTO  (case-insensitive) → tables of the
//     current schema
//   - trailing <ident>.  (no whitespace before the dot)             → columns of <ident>
//
// Stripping (single-line, applied to line-up-to-cursor BEFORE regex
// match so a keyword inside a string literal or comment does NOT
// trigger):
//   - single-quoted strings 'foo' with ” escape
//   - dollar-quoted strings $$...$$ and $tag$...$tag$
//   - line comments  -- ... end of line
//   - block comments /* ... */  (single-line only)
//
// Multi-line constructs (a string or block comment that opens on a
// previous line and is still open at the cursor) are a known v1
// limitation: the regex may false-positive trigger. The tracking
// epic for tree-sitter-grade parsing is dbsavvy-ktt.
//
// Identifier case is preserved verbatim when passed to ListColumns;
// Postgres resolves unquoted identifiers case-folded (lowercase)
// which matches the typical user expectation. Quoted identifiers
// are not handled in v1 — a typed `"MyTable".` will be matched as
// the literal `"MyTable"` and ListColumns will be called with that
// string, which will fail server-side; the source then returns
// empty (no error propagation).
type SchemaSource struct {
	priority int
	session  SessionProvider
	schema   SchemaProvider
}

// NewSchemaSource constructs a SchemaSource. Either provider may be
// nil — a nil provider is treated as if it returned the zero value
// (nil session / empty schema), which causes Suggest to return an
// empty slice. The defaults exist so the source can be unit-tested
// in isolation and wired post-construction by Z1.
func NewSchemaSource(session SessionProvider, schema SchemaProvider) *SchemaSource {
	return &SchemaSource{
		priority: SchemaSourcePriority,
		session:  session,
		schema:   schema,
	}
}

// Name returns the stable source identity.
func (s *SchemaSource) Name() string { return SchemaSourceName }

// Priority returns the source's tiebreak rank for the Engine.
func (s *SchemaSource) Priority() int { return s.priority }

// reKeywordTable matches a trailing keyword that means "next token
// is a table name". Case-insensitive; requires terminal whitespace.
// Word boundary at the start prevents `XFROM ` from matching.
var reKeywordTable = regexp.MustCompile(`(?i)(?:^|\s|[(,;])(?:FROM|JOIN|INNER\s+JOIN|LEFT\s+JOIN|RIGHT\s+JOIN|CROSS\s+JOIN|UPDATE|INTO)\s+$`)

// reIdentDot matches a trailing `<ident>.` with no whitespace
// between the identifier and the dot. Captures the identifier so
// ListColumns can resolve it.
var reIdentDot = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.$`)

// Suggest returns table or column suggestions based on the cursor
// context. Returns an empty slice for any failure (no providers, no
// session, no match, driver error). Never returns nil — callers
// can range freely.
func (s *SchemaSource) Suggest(ctx context.Context, buf *Buffer, pos Position) []Suggestion {
	if buf == nil {
		return []Suggestion{}
	}
	line := lineUpToCursor(buf, pos)
	if line == "" {
		return []Suggestion{}
	}
	stripped := stripNoise(line)

	// Column context wins over keyword context: `users.` matches the
	// dot rule and is unambiguous; a `FROM users.` line is treated
	// as a column lookup on `users` (which will likely return empty
	// — `FROM table.` is not valid SQL anyway).
	if m := reIdentDot.FindStringSubmatch(stripped); m != nil {
		return s.suggestColumns(ctx, m[1])
	}
	if reKeywordTable.MatchString(stripped) {
		return s.suggestTables(ctx)
	}
	return []Suggestion{}
}

// suggestTables fetches the current schema's tables. Returns empty
// when no session, no schema, or the driver call fails.
func (s *SchemaSource) suggestTables(ctx context.Context) []Suggestion {
	sess := s.activeSession()
	if sess == nil {
		return []Suggestion{}
	}
	schema := s.activeSchema()
	if schema == "" {
		return []Suggestion{}
	}
	tables, err := sess.ListTables(ctx, schema)
	if err != nil || len(tables) == 0 {
		return []Suggestion{}
	}
	out := make([]Suggestion, 0, len(tables))
	for _, t := range tables {
		if t == nil || t.Name == "" {
			continue
		}
		out = append(out, Suggestion{
			Text:    t.Name,
			Display: t.Name,
			Source:  SchemaSourceName,
		})
	}
	return out
}

// suggestColumns fetches columns of the named table in the current
// schema. The identifier is passed verbatim to the driver — Postgres
// case-folding semantics apply.
func (s *SchemaSource) suggestColumns(ctx context.Context, table string) []Suggestion {
	sess := s.activeSession()
	if sess == nil {
		return []Suggestion{}
	}
	schema := s.activeSchema()
	if schema == "" {
		return []Suggestion{}
	}
	cols, err := sess.ListColumns(ctx, schema, table)
	if err != nil || len(cols) == 0 {
		return []Suggestion{}
	}
	out := make([]Suggestion, 0, len(cols))
	for _, c := range cols {
		if c.Name == "" {
			continue
		}
		out = append(out, Suggestion{
			Text:    c.Name,
			Display: formatColumnDisplay(c),
			Source:  SchemaSourceName,
		})
	}
	return out
}

// formatColumnDisplay renders a Column as `<name> · <type>` when
// DataType is non-empty, falling back to just `<name>`.
func formatColumnDisplay(c models.Column) string {
	if c.DataType == "" {
		return c.Name
	}
	return c.Name + " · " + c.DataType
}

// activeSession safely calls the SessionProvider; nil provider
// returns nil.
func (s *SchemaSource) activeSession() drivers.Session {
	if s.session == nil {
		return nil
	}
	return s.session()
}

// activeSchema safely calls the SchemaProvider; nil provider
// returns "".
func (s *SchemaSource) activeSchema() string {
	if s.schema == nil {
		return ""
	}
	return s.schema()
}

// lineUpToCursor returns the line text from column 0 up to (but not
// including) pos.Col. Returns empty when pos is out of range.
func lineUpToCursor(buf *Buffer, pos Position) string {
	lines := buf.LinesCopy()
	if pos.Line < 0 || pos.Line >= len(lines) {
		return ""
	}
	runes := lines[pos.Line].Runes
	col := pos.Col
	if col < 0 {
		col = 0
	}
	if col > len(runes) {
		col = len(runes)
	}
	return string(runes[:col])
}

// stripNoise removes string literals, dollar-quoted strings, and
// comments from s by replacing their characters with spaces. Using
// spaces (rather than deleting) preserves column offsets so trailing
// `\s+$` anchors keep working naturally.
//
// Multi-line constructs are NOT supported — a string or block comment
// that opens on a previous line is not detected; v1 limitation.
func stripNoise(s string) string {
	if s == "" {
		return s
	}
	out := []byte(s)
	n := len(out)
	i := 0
	for i < n {
		c := out[i]
		switch {
		case c == '\'':
			// Single-quoted string with '' escape.
			j := i + 1
			for j < n {
				if out[j] == '\'' {
					if j+1 < n && out[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			fillSpaces(out, i, j)
			i = j
		case c == '-' && i+1 < n && out[i+1] == '-':
			// Line comment to end of string.
			fillSpaces(out, i, n)
			i = n
		case c == '/' && i+1 < n && out[i+1] == '*':
			// Block comment; close at first */ (single-line only).
			j := i + 2
			for j+1 < n && (out[j] != '*' || out[j+1] != '/') {
				j++
			}
			if j+1 < n {
				j += 2 // include the closing */
			} else {
				j = n
			}
			fillSpaces(out, i, j)
			i = j
		case c == '$':
			// Dollar-quoted string: $tag$ ... $tag$ (tag may be empty
			// for `$$ ... $$`). Tag is [A-Za-z_][A-Za-z0-9_]*.
			tagEnd := i + 1
			for tagEnd < n && isDollarTagByte(out[tagEnd]) {
				tagEnd++
			}
			if tagEnd >= n || out[tagEnd] != '$' {
				i++
				continue
			}
			tag := string(out[i : tagEnd+1]) // includes leading and trailing $
			j := tagEnd + 1
			closeIdx := indexOf(out, j, tag)
			if closeIdx < 0 {
				fillSpaces(out, i, n)
				i = n
			} else {
				j = closeIdx + len(tag)
				fillSpaces(out, i, j)
				i = j
			}
		default:
			i++
		}
	}
	return string(out)
}

// fillSpaces replaces out[start:end] with spaces in place.
func fillSpaces(out []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(out) {
		end = len(out)
	}
	for k := start; k < end; k++ {
		out[k] = ' '
	}
}

// isDollarTagByte reports whether b is allowed inside a dollar-quote
// tag (letters, digits, underscore — first char must be a letter or
// underscore but we accept digits everywhere; a leading-digit tag is
// invalid SQL and just won't match a closer, falling through safely).
func isDollarTagByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

// indexOf returns the index of needle starting at from in out, or
// -1 when absent. Simple wrapper around strings.Index for byte slices.
func indexOf(out []byte, from int, needle string) int {
	if from >= len(out) {
		return -1
	}
	rel := strings.Index(string(out[from:]), needle)
	if rel < 0 {
		return -1
	}
	return from + rel
}
