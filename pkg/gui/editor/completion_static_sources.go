package editor

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

// sqlKeywords is the static list of SQL keywords offered by KeywordsSource.
// Order in this slice is irrelevant — callers always receive results sorted
// alphabetically. Kept uppercase to match conventional SQL style; matching is
// case-insensitive against the buffer prefix.
var sqlKeywords = []string{
	"ALTER",
	"AND",
	"AS",
	"ASC",
	"BEGIN",
	"BETWEEN",
	"BY",
	"CASCADE",
	"CASE",
	"COLUMN",
	"COMMIT",
	"CREATE",
	"CROSS",
	"DELETE",
	"DESC",
	"DISTINCT",
	"DROP",
	"ELSE",
	"END",
	"EXISTS",
	"FALSE",
	"FROM",
	"FULL",
	"GROUP",
	"HAVING",
	"IN",
	"INDEX",
	"INNER",
	"INSERT",
	"INTO",
	"IS",
	"JOIN",
	"LEFT",
	"LIKE",
	"LIMIT",
	"NOT",
	"NULL",
	"OFFSET",
	"ON",
	"OR",
	"ORDER",
	"OUTER",
	"RIGHT",
	"ROLLBACK",
	"SELECT",
	"SET",
	"TABLE",
	"THEN",
	"TRUE",
	"UNION",
	"UPDATE",
	"USING",
	"VALUES",
	"WHEN",
	"WHERE",
	"WITH",
}

// KeywordsSourceName is the registered Source.Name() for KeywordsSource. Kept
// exported so tests and other sources can reference it without hard-coding.
const KeywordsSourceName = "keywords"

// SnippetsStubSourceName is the registered Source.Name() for SnippetsStubSource.
const SnippetsStubSourceName = "snippets"

// KeywordsSource emits SQL keywords whose uppercase form starts with the
// identifier prefix immediately to the left of the cursor. An empty prefix
// returns every keyword (sorted ascending). Suggestion.Source is always set
// to KeywordsSourceName. Score is fixed (low) so keywords lose to richer
// sources (schema, history) when texts collide.
type KeywordsSource struct {
	// PriorityVal controls the Source.Priority() tiebreak. Zero is fine —
	// callers can leave it default for the lowest priority slot.
	PriorityVal int
}

// Name implements Source.
func (KeywordsSource) Name() string { return KeywordsSourceName }

// Priority implements Source.
func (k KeywordsSource) Priority() int { return k.PriorityVal }

// Suggest implements Source. Walks back from pos.Col on pos.Line collecting
// identifier-class runes (letters, digits, underscore) to form the prefix.
// Match is case-insensitive against the keyword list; output is the uppercase
// keyword, sorted ascending.
func (k KeywordsSource) Suggest(_ context.Context, buf *Buffer, pos Position) []Suggestion {
	prefix := identifierPrefixAt(buf, pos)
	matches := filterKeywordsByPrefix(sqlKeywords, prefix)
	out := make([]Suggestion, 0, len(matches))
	for _, kw := range matches {
		out = append(out, Suggestion{
			Text:    kw,
			Display: kw,
			Source:  KeywordsSourceName,
			Score:   1,
		})
	}
	return out
}

// SnippetsStubSource is the placeholder for the snippets epic (dbsavvy-ktt).
// It satisfies the Source interface but returns no Suggestions, allowing C5
// (auto-trigger) and Z1 (wiring) to register it now without an implementation.
type SnippetsStubSource struct {
	PriorityVal int
}

// Name implements Source.
func (SnippetsStubSource) Name() string { return SnippetsStubSourceName }

// Priority implements Source.
func (s SnippetsStubSource) Priority() int { return s.PriorityVal }

// Suggest implements Source. Always returns an empty (non-nil) slice — the
// snippet body is delivered by epic dbsavvy-ktt.
func (SnippetsStubSource) Suggest(_ context.Context, _ *Buffer, _ Position) []Suggestion {
	return []Suggestion{}
}

// identifierPrefixAt collects the run of identifier-class runes immediately
// to the left of pos and returns them as a string. Returns "" when buf is nil,
// pos is out of bounds, or no identifier characters precede the cursor.
func identifierPrefixAt(buf *Buffer, pos Position) string {
	if buf == nil {
		return ""
	}
	lines := buf.LinesCopy()
	if pos.Line < 0 || pos.Line >= len(lines) {
		return ""
	}
	runes := lines[pos.Line].Runes
	end := min(pos.Col, len(runes))
	if end <= 0 {
		return ""
	}
	start := end
	for start > 0 && isIdentRune(runes[start-1]) {
		start--
	}
	return string(runes[start:end])
}

// isIdentRune reports whether r is part of an identifier prefix — letters,
// digits, and underscore. Tabs/spaces/punctuation terminate the prefix walk.
func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// filterKeywordsByPrefix returns the subset of kws whose uppercase form starts
// with the uppercase of prefix, sorted ascending. An empty prefix returns the
// full set (also sorted). The input slice is never mutated.
func filterKeywordsByPrefix(kws []string, prefix string) []string {
	up := strings.ToUpper(prefix)
	out := make([]string, 0, len(kws))
	for _, kw := range kws {
		if up == "" || strings.HasPrefix(kw, up) {
			out = append(out, kw)
		}
	}
	sort.Strings(out)
	return out
}
