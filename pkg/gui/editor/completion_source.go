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
type Suggestion struct {
	Text    string
	Display string
	Source  string
	Score   int
}

// Source is one producer of Suggestions. Name is the stable identity
// (used in Suggestion.Source and as the debug label). Priority breaks
// ties in dedupe + sort when two Suggestions share Text and Score —
// higher Priority wins. Suggest computes the candidate list for the
// cursor position; it MUST NOT mutate the Buffer.
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
