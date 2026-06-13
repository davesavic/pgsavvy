package editor

import "context"

// Suggestion is one entry returned by a completion Source and merged
// into the popup by *Engine. Text is what the editor inserts; Display
// is what the popup renders; Source is the producing source name (for
// debug + tie-break audit); Score is the source's ranking — higher
// wins.
//
// Text MUST be free of C0 controls and newlines when applied to a
// Buffer; callers route Text through SanitizeText before passing the
// chosen Suggestion to the editor.
//
// Display is rendered through the grid escape sanitizer at render
// time (HandleRender on SuggestionsContext) so untrusted server
// output cannot hijack the terminal.
//
// Matches holds the RUNE offsets into Text that the fuzzy matcher
// flagged as contributing characters (for highlight rendering in the
// popup). It is zero-defaulted (nil) for sources that do not run the
// matcher; a nil Matches sorts and renders identically to before (no
// panic). Consumers MUST convert these rune offsets to byte offsets
// before indexing the underlying string — ko4m.4 owns that rune->byte
// conversion and the popup highlight rendering.
//
// The remaining fields are the typed presentation contract (ko4m.4,
// Design D1/D2): metadata that used to be baked into Display is now
// carried as discrete, typed fields so the renderer can lay out a
// detail column, per-kind glyph, and PK/FK/NN annotations. They are all
// additive and zero-defaulted, so a Suggestion that sets none of them
// renders as a bare name (Design D6).
//
//   - Kind classifies the suggestion (column|table|view|function|
//     keyword|history|snippet); see the SuggestionKind const set. The
//     zero value ("") means "unkinded" and renders without a glyph.
//   - Detail is the right-aligned detail text (e.g. a column's data
//     type). It is server-derived where applicable and MUST be
//     sanitized at render time (Design D4) — do not assume it is safe
//     terminal output.
//   - IsPrimaryKey / NotNull are column annotation flags surfaced as
//     render badges.
//   - FKRef, when non-empty, is a schema-qualified foreign-key target
//     in "ref.table.col" form (empty otherwise). Like Detail it is
//     server-derived and MUST be sanitized at render time (Design D4).
//   - Signature is reserved for function signature help (ko4m.5.3);
//     left unpopulated by this task.
//   - Body is reserved for the snippet expansion body (ko4m.7.1); left
//     unpopulated by this task.
//
// Populating these fields is owned by ko4m.4.3; rendering them is owned
// by ko4m.4.4 — this task only defines the data contract.
type Suggestion struct {
	Text         string
	Display      string
	Source       string
	Score        int
	Matches      []int
	Kind         SuggestionKind
	Detail       string
	IsPrimaryKey bool
	NotNull      bool
	FKRef        string
	Signature    string
	Body         string
}

// SuggestionKind classifies a Suggestion for per-kind rendering (glyph,
// badges). The zero value ("") is "unkinded" and renders as a bare name
// with no glyph (Design D6). Populated by ko4m.4.3, consumed by the
// renderer in ko4m.4.4.
type SuggestionKind string

const (
	KindColumn   SuggestionKind = "column"
	KindTable    SuggestionKind = "table"
	KindView     SuggestionKind = "view"
	KindFunction SuggestionKind = "function"
	KindKeyword  SuggestionKind = "keyword"
	KindHistory  SuggestionKind = "history"
	KindSnippet  SuggestionKind = "snippet"
)

// Source-rank composite-ranking contract (dbsavvy-ko4m.3, Finding B4).
//
// The completion ranking is source-weighted: a source's final
// Suggestion.Score is computed as
//
//	Score = matchQuality + sourceBias
//
// where matchQuality is the per-candidate score the fuzzy matcher
// (editor.Match, ko4m.3.1) returns, and sourceBias is the source's
// fixed bias from the const block below. Engine.Trigger sorts Score
// descending; Source.Priority() remains the SECONDARY tiebreak when two
// Suggestions share Score (then registration order). The biases below
// enforce the source rank Schema > Function > Snippet > Keyword >
// History so that, all else equal, a schema match outranks a function,
// which outranks a snippet, a keyword, and finally history.
//
// These are the ONE source of truth for source rank — do not introduce
// parallel bias/priority numbers elsewhere. Sources adopt this contract
// in ko4m.3.3-3.5; this task only pins the constants and the contract.
const (
	SchemaSourceBias   = 80
	FunctionSourceBias = 60
	SnippetSourceBias  = 50
	KeywordSourceBias  = 40
	HistorySourceBias  = 20
)

// Source is one producer of Suggestions. Name is the stable identity
// (used in Suggestion.Source and as the debug label). Priority is the
// SECONDARY tiebreak in dedupe + sort when two Suggestions share Score
// — higher Priority wins (the composite Score = matchQuality +
// sourceBias is the primary key; see the source-rank const block
// above). Suggest computes the candidate list for the cursor position;
// it MUST NOT mutate the Buffer.
type Source interface {
	Name() string
	Priority() int
	Suggest(ctx context.Context, buf *Buffer, pos Position) []Suggestion
}

// SanitizeText strips C0 control bytes (0x00-0x1F and 0x7F) and
// newline runes from s so a Suggestion.Text is safe to insert into a
// Buffer. Tab is also stripped — completion insertions should produce
// a single contiguous token, not a multi-cell jump.
func SanitizeText(s string) string {
	if s == "" {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// SanitizeSnippetText strips C0 control bytes (<0x20, plus 0x7F) from a
// snippet Body so it is safe to splice into a Buffer, but PRESERVES '\n'
// and '\t' — a snippet expansion is intentionally a multi-line,
// indentation-bearing insertion (Design D3/D7 of dbsavvy-ko4m.7). Contrast
// SanitizeText, which also strips '\n'/'\t' because a normal completion
// insertion must stay a single contiguous token. The retained '\r' from a
// CRLF body is dropped (only '\n' demarcates lines for splitTextOnNewline).
//
// Snippet bodies are trusted-local (built-in / user config), so the C0/0x7F
// strip is a defence-in-depth guard against a stray ESC in a hand-edited
// body reaching the editor; if dbsavvy-ktt later loads bodies from shared
// or untrusted sources, re-evaluate the trust boundary.
func SanitizeSnippetText(s string) string {
	if s == "" {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' {
			out = append(out, r)
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
