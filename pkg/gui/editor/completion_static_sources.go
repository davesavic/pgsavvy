package editor

import (
	"context"
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

// KeywordsSource emits SQL keywords whose uppercase form fuzzily matches the
// identifier prefix immediately to the left of the cursor (editor.Match,
// a non-prefix subsequence still surfaces a keyword). An empty
// prefix returns every keyword (Match("",x) contract). Suggestion.Source is
// always set to KeywordsSourceName. Score = matchQuality + KeywordSourceBias so
// keywords lose to richer sources (schema, function) but beat history when
// texts collide (rank order). Suggestion.Matches carries the matched
// rune offsets for popup highlighting.
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
// Matching is the fzf-style subsequence matcher (editor.Match), case-
// insensitive against the keyword list; output is the uppercase keyword.
// sqlKeywords is maintained in ascending order, so iterating it and keeping
// matches preserves the sorted output the popup expects. An empty prefix keeps
// every keyword at Score = KeywordSourceBias (Match("",x) → quality 0).
func (k KeywordsSource) Suggest(_ context.Context, buf *Buffer, pos Position) []Suggestion {
	prefix := identifierPrefixAt(buf, pos)
	out := make([]Suggestion, 0, len(sqlKeywords))
	for _, kw := range sqlKeywords {
		ok, quality, positions := Match(prefix, kw)
		if !ok {
			continue
		}
		out = append(out, Suggestion{
			Text:    kw,
			Display: kw,
			Source:  KeywordsSourceName,
			Score:   quality + KeywordSourceBias,
			Matches: positions,
			Kind:    KindKeyword,
			Detail:  "kw",
		})
	}
	return out
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
