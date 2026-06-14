package editor

import "context"

// SnippetSourceName is the registered Source.Name() for SnippetSource. The
// "snippets" identity is the stable source name surfaced on Suggestion.Source
// for snippet candidates and used by the engine to distinguish this source.
const SnippetSourceName = "snippets"

// SnippetSourcePriority is SnippetSource's secondary-tiebreak Priority(). It
// reconciles to the single source-rank source of truth (SnippetSourceBias,
// completion_source.go) rather than introducing a parallel number, mirroring
// how the schema/function sources tie their Priority to their bias. The
// composite Score = matchQuality + SnippetSourceBias remains the primary sort
// key; Priority only breaks Score ties.
const SnippetSourcePriority = SnippetSourceBias

// Snippet is one named, expandable SQL template. Name is the token the user
// types/matches against in the completion popup; Body is the (typically
// multi-line) text inserted when the snippet is accepted. The accept/expand
// insertion path is owned elsewhere — this type only carries the data.
type Snippet struct {
	Name string
	Body string
}

// SnippetProvider is the data seam SnippetSource reads from. Keeping the
// snippet set behind an interface lets the built-in starter set
// (BuiltinSnippetProvider) and, later, a config-file-backed provider
// swap in without touching SnippetSource. Mirrors the HistoryStore seam used
// by HistorySource.
type SnippetProvider interface {
	Snippets() []Snippet
}

// BuiltinSnippetProvider is the hardcoded starter set shipped with the editor.
// It satisfies SnippetProvider with a handful of common SQL templates so the
// snippet source is useful out of the box before any user config loads.
type BuiltinSnippetProvider struct{}

// Snippets implements SnippetProvider. Each entry has a non-empty Name and a
// multi-line Body. The set is intentionally small and SQL-generic.
func (BuiltinSnippetProvider) Snippets() []Snippet {
	return []Snippet{
		{
			Name: "select_all",
			Body: "SELECT *\nFROM table_name\nWHERE condition\nLIMIT 100;",
		},
		{
			Name: "inner_join",
			Body: "SELECT a.*, b.*\nFROM table_a AS a\nINNER JOIN table_b AS b\n  ON a.id = b.a_id;",
		},
		{
			Name: "cte",
			Body: "WITH cte AS (\n  SELECT *\n  FROM table_name\n  WHERE condition\n)\nSELECT *\nFROM cte;",
		},
		{
			Name: "insert_into",
			Body: "INSERT INTO table_name (col1, col2)\nVALUES\n  (val1, val2);",
		},
		{
			Name: "update",
			Body: "UPDATE table_name\nSET col1 = val1,\n    col2 = val2\nWHERE condition;",
		},
		{
			Name: "delete_from",
			Body: "DELETE FROM table_name\nWHERE condition;",
		},
	}
}

// SnippetSource emits Suggestions for snippets whose Name fuzzily matches the
// identifier prefix immediately left of the cursor (editor.Match).
// Suggestion.Source is always SnippetSourceName; Suggestion.Text and
// Suggestion.Display are the snippet Name (the token the user is completing),
// and Suggestion.Body carries the multi-line expansion body for the
// accept/expand path. Score = matchQuality + SnippetSourceBias so
// snippets sit below schema/function and above keyword/history in the rank
// order.
//
// The snippet set comes from the injected SnippetProvider; a nil provider
// yields an empty (non-nil) slice. SnippetSource adds no drivers.Session call —
// snippets are static editor data, not server-derived.
type SnippetSource struct {
	provider SnippetProvider
	// PriorityVal overrides the Source.Priority() tiebreak. Zero falls back to
	// SnippetSourcePriority so the source-rank const stays the single source of
	// truth.
	PriorityVal int
}

// NewSnippetSource builds a SnippetSource backed by the given provider. A nil
// provider is allowed and makes Suggest return an empty slice.
func NewSnippetSource(provider SnippetProvider) *SnippetSource {
	return &SnippetSource{provider: provider}
}

// Name implements Source.
func (*SnippetSource) Name() string { return SnippetSourceName }

// Priority implements Source. Falls back to SnippetSourcePriority when
// PriorityVal is left zero.
func (s *SnippetSource) Priority() int {
	if s.PriorityVal != 0 {
		return s.PriorityVal
	}
	return SnippetSourcePriority
}

// Suggest implements Source. Collects the identifier prefix left of the cursor
// and fuzzy-matches it against each snippet Name via editor.Match. An empty
// prefix matches every snippet (Match("",x) contract). Returns an empty
// (non-nil) slice when the provider is nil. Never mutates the Buffer and never
// touches a drivers.Session.
func (s *SnippetSource) Suggest(_ context.Context, buf *Buffer, pos Position) []Suggestion {
	if s.provider == nil {
		return []Suggestion{}
	}
	snippets := s.provider.Snippets()
	prefix := identifierPrefixAt(buf, pos)
	out := make([]Suggestion, 0, len(snippets))
	for _, sn := range snippets {
		ok, quality, positions := Match(prefix, sn.Name)
		if !ok {
			continue
		}
		out = append(out, Suggestion{
			Text:    sn.Name,
			Display: sn.Name,
			Source:  SnippetSourceName,
			Score:   quality + SnippetSourceBias,
			Matches: positions,
			Kind:    KindSnippet,
			Body:    sn.Body,
		})
	}
	return out
}
