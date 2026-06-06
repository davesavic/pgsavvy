package editor

import (
	"context"
	"regexp"
	"strings"
	"sync"

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

// SchemaSourceScore is the Score every schema table/column suggestion
// carries. Engine.Trigger sorts by Score DESCENDING (priority is only a
// within-equal-score tiebreak), so this must sit ABOVE the keyword /
// history fixed Score of 1 — otherwise schema hits, the most relevant
// completion in a FROM / `<ident>.` context, sort below every keyword
// and fall outside the visible window. dbsavvy-ybi.
const SchemaSourceScore = 3

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

	mu         sync.Mutex
	cachedSess drivers.Session // identity the caches were built against

	tablesCache    []Suggestion // full (unfiltered) table list, per (session,schema)
	tablesCacheKey string       // schema the table cache was built for
	hasTables      bool
	columnsCache   map[string][]Suggestion // full column lists keyed by schema\x00table
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
// is a table name", optionally followed by a partial identifier the
// user has begun typing. Case-insensitive; requires terminal
// whitespace after the keyword. Word boundary at the start prevents
// `XFROM ` from matching. The trailing capture is the partial (empty
// when the user has only typed the keyword + space); a COMPLETE table
// name followed by a space (`FROM users `) does NOT match because the
// keyword no longer abuts the cursor token.
var reKeywordTable = regexp.MustCompile(`(?i)(?:^|\s|[(,;])(?:FROM|JOIN|INNER\s+JOIN|LEFT\s+JOIN|RIGHT\s+JOIN|CROSS\s+JOIN|UPDATE|INTO)\s+([A-Za-z_][A-Za-z0-9_]*)?$`)

// reIdentDot matches a trailing `<ident>.` with no whitespace
// between the identifier and the dot, optionally followed by a partial
// column identifier. Captures the identifier (group 1) and the partial
// column prefix (group 2, possibly empty).
var reIdentDot = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)?$`)

// reColumnContext matches a trailing position where an UNQUALIFIED
// column is expected: right after SELECT / WHERE / AND / OR (each
// requiring a following space and a word boundary before it), or right
// after a comparison operator (=, <>, !=, <=, >=, <, >). The single
// capture group is the partial column identifier the user has begun
// typing (empty when only the keyword/operator + space was typed). A
// completed token followed by a space does NOT match, mirroring
// reKeywordTable.
var reColumnContext = regexp.MustCompile(`(?i)(?:(?:^|[\s(,])(?:SELECT|WHERE|AND|OR)\s+|(?:<>|!=|<=|>=|=|<|>)\s*)([A-Za-z_][A-Za-z0-9_]*)?$`)

// reJoinCondition matches a trailing join-condition position: right
// after `ON` or `USING` (case-insensitive, requiring following
// whitespace and a word boundary before). The single capture is the
// partial identifier the user has begun typing (empty when only the
// keyword + space was typed). This is where a join predicate like
// `posts.id = posts_summary.post_id` goes, so the source offers the
// in-scope tables (to qualify a column via `posts.`) plus their columns.
var reJoinCondition = regexp.MustCompile(`(?i)(?:^|\s|[(,])(?:ON|USING)\s+([A-Za-z_][A-Za-z0-9_]*)?$`)

// reFromJoinTables matches every `FROM <table>` / `JOIN <table>` in a
// statement (global), capturing the table identifier. Word boundary at
// the start prevents matching inside larger identifiers. Schema-qualified
// names and aliases are not resolved (v1) — the first identifier after
// the keyword is captured verbatim.
var reFromJoinTables = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([A-Za-z_][A-Za-z0-9_]*)`)

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
	stripped, open := stripNoiseEx(line)
	if open {
		// Cursor sits inside an unterminated string / comment — no
		// completion (the trailing keyword/operator the regexes would
		// match is blanked content, not a real clause position).
		return []Suggestion{}
	}

	// Column context wins over keyword context: `users.` matches the
	// dot rule and is unambiguous; a `FROM users.` line is treated
	// as a column lookup on `users` (which will likely return empty
	// — `FROM table.` is not valid SQL anyway).
	if m := reIdentDot.FindStringSubmatch(stripped); m != nil {
		return s.suggestColumns(ctx, m[1], m[2])
	}
	if m := reKeywordTable.FindStringSubmatch(stripped); m != nil {
		return s.suggestTables(ctx, m[1])
	}
	if m := reJoinCondition.FindStringSubmatch(stripped); m != nil {
		return s.suggestJoinCondition(ctx, buf, m[1])
	}
	if m := reColumnContext.FindStringSubmatch(stripped); m != nil {
		tables := s.scopeTables(buf)
		if len(tables) == 0 {
			return []Suggestion{}
		}
		return s.suggestColumnsMulti(ctx, tables, m[1])
	}
	return []Suggestion{}
}

// scopeTables returns the distinct table names referenced by FROM /
// JOIN clauses anywhere in the buffer, in first-seen order. The whole
// buffer is scanned (not just text up to the cursor) so a column
// position before the FROM clause — e.g. inside the SELECT list — still
// resolves its tables. Each line is noise-stripped independently before
// matching. Multi-statement buffers may over-collect (v1 limitation).
func (s *SchemaSource) scopeTables(buf *Buffer) []string {
	var sb strings.Builder
	for _, ln := range buf.LinesCopy() {
		clean, _ := stripNoiseEx(string(ln.Runes))
		sb.WriteString(clean)
		sb.WriteByte(' ')
	}
	matches := reFromJoinTables.FindAllStringSubmatch(sb.String(), -1)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// suggestJoinCondition serves the JOIN ... ON / USING position. It offers
// the tables already referenced by FROM/JOIN (so the user can qualify a
// column via `posts.`) followed by those tables' columns, all filtered by
// the typed prefix. Returns empty when no tables are in scope. Duplicate
// Text across the table and column sets is left for Engine dedupe.
func (s *SchemaSource) suggestJoinCondition(ctx context.Context, buf *Buffer, prefix string) []Suggestion {
	tables := s.scopeTables(buf)
	if len(tables) == 0 {
		return []Suggestion{}
	}
	out := make([]Suggestion, 0, len(tables))
	for _, t := range tables {
		out = append(out, Suggestion{Text: t, Display: t, Source: SchemaSourceName, Score: SchemaSourceScore})
	}
	out = filterByPrefix(out, prefix)
	return append(out, s.suggestColumnsMulti(ctx, tables, prefix)...)
}

// suggestColumnsMulti returns the union of the named tables' columns,
// deduplicated by column name in scope order (a column shared by two
// tables appears once, attributed to the first), then filtered by
// prefix. Returns empty when there is no session or schema.
func (s *SchemaSource) suggestColumnsMulti(ctx context.Context, tables []string, prefix string) []Suggestion {
	sess := s.activeSession()
	if sess == nil {
		return []Suggestion{}
	}
	schema := s.activeSchema()
	if schema == "" {
		return []Suggestion{}
	}
	seen := map[string]struct{}{}
	out := []Suggestion{}
	for _, t := range tables {
		full, ok := s.cachedColumns(ctx, sess, schema, t)
		if !ok {
			continue
		}
		for _, sg := range full {
			if _, dup := seen[sg.Text]; dup {
				continue
			}
			seen[sg.Text] = struct{}{}
			out = append(out, sg)
		}
	}
	return filterByPrefix(out, prefix)
}

// suggestTables returns the current schema's tables whose name starts
// with prefix (case-insensitive; empty prefix → all). The full list is
// cached per (session,schema); only the prefix filter runs per call.
// Returns empty when no session, no schema, or the driver call fails.
func (s *SchemaSource) suggestTables(ctx context.Context, prefix string) []Suggestion {
	sess := s.activeSession()
	if sess == nil {
		return []Suggestion{}
	}
	schema := s.activeSchema()
	if schema == "" {
		return []Suggestion{}
	}
	full, ok := s.cachedTables(ctx, sess, schema)
	if !ok {
		return []Suggestion{}
	}
	return filterByPrefix(full, prefix)
}

// suggestColumns returns columns of the named table in the current
// schema whose name starts with prefix (case-insensitive; empty →
// all). The full column list is cached per (schema,table). The table
// identifier is passed verbatim to the driver — Postgres case-folding
// semantics apply.
func (s *SchemaSource) suggestColumns(ctx context.Context, table, prefix string) []Suggestion {
	sess := s.activeSession()
	if sess == nil {
		return []Suggestion{}
	}
	schema := s.activeSchema()
	if schema == "" {
		return []Suggestion{}
	}
	full, ok := s.cachedColumns(ctx, sess, schema, table)
	if !ok {
		return []Suggestion{}
	}
	return filterByPrefix(full, prefix)
}

// cachedTables returns the full table-suggestion list for (sess,schema),
// fetching and caching it on a miss. The bool is false on driver error
// or empty result (neither is cached, so the next call retries).
func (s *SchemaSource) cachedTables(ctx context.Context, sess drivers.Session, schema string) ([]Suggestion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateIfStale(sess)
	if s.hasTables && s.tablesCacheKey == schema {
		return s.tablesCache, true
	}
	tables, err := sess.ListTables(ctx, schema)
	if err != nil || len(tables) == 0 {
		return nil, false
	}
	out := make([]Suggestion, 0, len(tables))
	for _, t := range tables {
		if t == nil || t.Name == "" {
			continue
		}
		out = append(out, Suggestion{Text: t.Name, Display: t.Name, Source: SchemaSourceName, Score: SchemaSourceScore})
	}
	s.tablesCache = out
	s.tablesCacheKey = schema
	s.hasTables = true
	return out, true
}

// cachedColumns returns the full column-suggestion list for
// (schema,table), fetching and caching it on a miss. The bool is false
// on driver error or empty result (neither is cached).
func (s *SchemaSource) cachedColumns(ctx context.Context, sess drivers.Session, schema, table string) ([]Suggestion, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateIfStale(sess)
	key := schema + "\x00" + table
	if cached, ok := s.columnsCache[key]; ok {
		return cached, true
	}
	cols, err := sess.ListColumns(ctx, schema, table)
	if err != nil || len(cols) == 0 {
		return nil, false
	}
	out := make([]Suggestion, 0, len(cols))
	for _, c := range cols {
		if c.Name == "" {
			continue
		}
		out = append(out, Suggestion{Text: c.Name, Display: formatColumnDisplay(c), Source: SchemaSourceName, Score: SchemaSourceScore})
	}
	if s.columnsCache == nil {
		s.columnsCache = map[string][]Suggestion{}
	}
	s.columnsCache[key] = out
	return out, true
}

// invalidateIfStale drops all caches when the active session pointer
// differs from the one the caches were built against (reconnect).
// Caller must hold s.mu.
func (s *SchemaSource) invalidateIfStale(sess drivers.Session) {
	if s.cachedSess == sess {
		return
	}
	s.cachedSess = sess
	s.tablesCache = nil
	s.tablesCacheKey = ""
	s.hasTables = false
	s.columnsCache = nil
}

// filterByPrefix returns the subset of sugs whose Text starts with
// prefix (case-insensitive). Empty prefix returns the input unchanged.
// Order is preserved.
func filterByPrefix(sugs []Suggestion, prefix string) []Suggestion {
	if prefix == "" {
		return sugs
	}
	up := strings.ToUpper(prefix)
	out := make([]Suggestion, 0, len(sugs))
	for _, sg := range sugs {
		if strings.HasPrefix(strings.ToUpper(sg.Text), up) {
			out = append(out, sg)
		}
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
	col := min(max(pos.Col, 0), len(runes))
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
	out, _ := stripNoiseEx(s)
	return out
}

// stripNoiseEx is stripNoise plus a flag reporting whether the scan
// ended INSIDE an unterminated construct (open string, dollar-quote,
// block comment, or a line comment running to the end). Callers use the
// flag to suppress completion when the cursor sits inside a literal or
// comment: blanking alone cannot distinguish `SELECT <space>` (a real
// column position) from `SELECT '<blanked string>` (inside a literal).
func stripNoiseEx(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	out := []byte(s)
	n := len(out)
	i := 0
	open := false
	for i < n {
		c := out[i]
		switch {
		case c == '\'':
			// Single-quoted string with '' escape.
			j := i + 1
			closed := false
			for j < n {
				if out[j] == '\'' {
					if j+1 < n && out[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					closed = true
					break
				}
				j++
			}
			fillSpaces(out, i, j)
			if !closed {
				open = true
			}
			i = j
		case c == '-' && i+1 < n && out[i+1] == '-':
			// Line comment to end of string.
			fillSpaces(out, i, n)
			open = true
			i = n
		case c == '/' && i+1 < n && out[i+1] == '*':
			// Block comment; close at first */ (single-line only).
			j := i + 2
			for j+1 < n && (out[j] != '*' || out[j+1] != '/') {
				j++
			}
			if j+1 < n {
				j += 2 // include the closing */
				fillSpaces(out, i, j)
			} else {
				j = n
				fillSpaces(out, i, j)
				open = true
			}
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
				open = true
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
	return string(out), open
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
