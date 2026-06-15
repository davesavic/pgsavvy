package editor

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
)

// SplitStatements splits buf on SQL-aware semicolons and returns the
// non-empty segments. Semicolons inside string literals, dollar-quoted
// blocks, and comments are NOT treated as statement boundaries.
//
// An empty input or an input consisting entirely of whitespace and
// semicolons returns a nil slice.
func SplitStatements(buf string) []string {
	if strings.TrimSpace(buf) == "" {
		return nil
	}

	tokens := highlight.Tokenize(buf)
	semis := semicolonRuneOffsets(tokens)

	// No real semicolons: the whole buffer is one statement.
	if len(semis) == 0 {
		return []string{buf}
	}

	runes := []rune(buf)
	var out []string
	start := 0
	for _, semiOff := range semis {
		seg := string(runes[start:semiOff])
		if strings.TrimSpace(seg) != "" {
			out = append(out, seg)
		}
		start = semiOff + 1
	}
	// Trailing segment after the last semicolon.
	if start < len(runes) {
		seg := string(runes[start:])
		if strings.TrimSpace(seg) != "" {
			out = append(out, seg)
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// StatementRangeAt returns the [start, end) rune-offset boundaries of
// the statement containing runeOff. Semicolons inside strings, comments,
// and dollar-quoted blocks are ignored. runeOff is clamped into
// [0, runeCount].
//
// When runeOff lands on a semicolon, the preceding statement is returned
// (matching vim's "cursor on ;" convention).
func StatementRangeAt(buf string, runeOff int) (start, end int) {
	runeCount := utf8.RuneCountInString(buf)
	if runeOff < 0 {
		runeOff = 0
	}
	if runeOff > runeCount {
		runeOff = runeCount
	}

	tokens := highlight.Tokenize(buf)
	semis := semicolonRuneOffsets(tokens)

	if len(semis) == 0 {
		return 0, runeCount
	}

	// If runeOff lands exactly on a semicolon, treat it as belonging
	// to the preceding statement (adjust to look left).
	onSemi := slices.Contains(semis, runeOff)

	if onSemi {
		// Statement is from the previous semicolon (exclusive) to this one (exclusive).
		start = 0
		for _, s := range semis {
			if s < runeOff {
				start = s + 1
			}
		}
		return start, runeOff
	}

	// Normal case: find the semicolons bracketing runeOff.
	start = 0
	end = runeCount
	for _, s := range semis {
		if s < runeOff {
			start = s + 1
		}
	}
	for _, s := range semis {
		if s >= runeOff {
			end = s
			break
		}
	}

	// When the resolved segment is empty (cursor sits just past a
	// trailing ';'), fall back to the preceding statement — matching
	// the original "SELECT 1;|" => "SELECT 1" behaviour.
	if start >= end {
		prevEnd := start - 1 // the ';' we just passed
		prevStart := 0
		for _, s := range semis {
			if s < prevEnd {
				prevStart = s + 1
			}
		}
		return prevStart, prevEnd
	}

	return start, end
}

// StatementAt returns the statement under the byte offset off inside
// buf. The result is the substring between the nearest SQL-aware
// semicolons. off is clamped into [0, len(buf)].
//
// Returns "" when buf is empty or when the resolved segment is
// whitespace-only.
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

	// Convert byte offset to rune offset.
	runeOff := utf8.RuneCountInString(buf[:off])

	start, end := StatementRangeAt(buf, runeOff)
	runes := []rune(buf)
	seg := strings.TrimSpace(string(runes[start:end]))
	return seg
}

// semicolonRuneOffsets walks the token stream and returns the rune
// offsets of all semicolons that are NOT inside strings, comments, or
// dollar-quoted blocks. Only Punctuation tokens are inspected; Chroma
// may coalesce adjacent punctuation (e.g., ");" as one token), so we
// scan within each Punctuation token for ';' characters.
func semicolonRuneOffsets(tokens []highlight.Token) []int {
	var offsets []int
	for _, tok := range tokens {
		if tok.Type != highlight.Punctuation {
			continue
		}
		for i, r := range []rune(tok.Value) {
			if r == ';' {
				offsets = append(offsets, tok.RuneOffset+i)
			}
		}
	}
	return offsets
}
