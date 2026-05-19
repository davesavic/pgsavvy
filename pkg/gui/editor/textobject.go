package editor

import (
	"unicode"
)

// Text-object functions resolve a Range around a cursor Position. The
// returned bool is false when no surrounding object exists (e.g. `i"`
// with no quotes on the line, `i(` with no enclosing parens). When
// false, callers MUST NOT use the returned Range.
//
// All ranges are character-wise (LineWise=false). The Inner variant
// excludes the delimiters; the Around variant includes them.
//
// Quote text-objects are line-local per vim semantics. Bracket
// text-objects span lines and respect nesting. Paragraph follows
// vim's blank-line definition (NOT SQL-statement). Statement is the
// SQL ;-delimited segment via the naive splitter (documented
// limitation: a `;` inside a string literal mis-splits — see the
// debug log in SplitStatements).

// InnerQuote returns the range inside the q-delimited pair surrounding
// pos on the current line. Quotes are paired left-to-right (1st with
// 2nd, 3rd with 4th, …). Cursor sitting on a quote is treated as
// belonging to whichever pair brackets it. When no pair brackets pos,
// the first pair on the line (if any) is returned to mirror vim's
// "expand outward" behaviour. Returns (Range{}, false) when the line
// has fewer than two q runes.
func InnerQuote(b *Buffer, pos Position, q rune) (Range, bool) {
	if empty(b) || pos.Line < 0 || pos.Line >= len(b.Lines) {
		return Range{}, false
	}
	line := b.Lines[pos.Line].Runes
	if pos.Col < 0 {
		return Range{}, false
	}
	var quotes []int
	for i, r := range line {
		if r == q {
			quotes = append(quotes, i)
		}
	}
	if len(quotes) < 2 {
		return Range{}, false
	}
	for i := 0; i+1 < len(quotes); i += 2 {
		a, c := quotes[i], quotes[i+1]
		if pos.Col >= a && pos.Col <= c {
			return Range{
				Start: Position{Line: pos.Line, Col: a + 1},
				End:   Position{Line: pos.Line, Col: c},
			}, true
		}
	}
	if pos.Col < quotes[0] {
		return Range{
			Start: Position{Line: pos.Line, Col: quotes[0] + 1},
			End:   Position{Line: pos.Line, Col: quotes[1]},
		}, true
	}
	return Range{}, false
}

// AroundQuote returns the same range as InnerQuote but extended to
// include the delimiting q runes.
func AroundQuote(b *Buffer, pos Position, q rune) (Range, bool) {
	r, ok := InnerQuote(b, pos, q)
	if !ok {
		return Range{}, false
	}
	r.Start.Col--
	r.End.Col++
	return r, true
}

// InnerParen returns the range inside the innermost (…) enclosing pos.
// Supports nesting and spans across lines.
func InnerParen(b *Buffer, pos Position) (Range, bool) {
	return innerBracket(b, pos, '(', ')')
}

// AroundParen returns the same range as InnerParen but extended to
// include the delimiting ( and ).
func AroundParen(b *Buffer, pos Position) (Range, bool) {
	return aroundBracket(b, pos, '(', ')')
}

// InnerBracket returns the range inside the innermost […] enclosing pos.
func InnerBracket(b *Buffer, pos Position) (Range, bool) {
	return innerBracket(b, pos, '[', ']')
}

// AroundBracket returns the same range as InnerBracket including the
// delimiting [ and ].
func AroundBracket(b *Buffer, pos Position) (Range, bool) {
	return aroundBracket(b, pos, '[', ']')
}

// InnerBraces returns the range inside the innermost {…} enclosing pos.
// Bound to `iB` per vim convention.
func InnerBraces(b *Buffer, pos Position) (Range, bool) {
	return innerBracket(b, pos, '{', '}')
}

// AroundBraces returns the same range as InnerBraces including the
// delimiting { and }. Bound to `aB` per vim convention.
func AroundBraces(b *Buffer, pos Position) (Range, bool) {
	return aroundBracket(b, pos, '{', '}')
}

func innerBracket(b *Buffer, pos Position, open, close rune) (Range, bool) {
	openLine, openCol, ok := findEnclosingOpen(b, pos, open, close)
	if !ok {
		return Range{}, false
	}
	closeLine, closeCol, ok := findMatchingClose(b, openLine, openCol, open, close)
	if !ok {
		return Range{}, false
	}
	return Range{
		Start: Position{Line: openLine, Col: openCol + 1},
		End:   Position{Line: closeLine, Col: closeCol},
	}, true
}

func aroundBracket(b *Buffer, pos Position, open, close rune) (Range, bool) {
	openLine, openCol, ok := findEnclosingOpen(b, pos, open, close)
	if !ok {
		return Range{}, false
	}
	closeLine, closeCol, ok := findMatchingClose(b, openLine, openCol, open, close)
	if !ok {
		return Range{}, false
	}
	return Range{
		Start: Position{Line: openLine, Col: openCol},
		End:   Position{Line: closeLine, Col: closeCol + 1},
	}, true
}

// findEnclosingOpen walks backward from pos counting close/open pairs;
// returns the position of the first unmatched open. Cursor sitting on
// an open delim counts that delim as the enclosing one.
func findEnclosingOpen(b *Buffer, pos Position, open, close rune) (int, int, bool) {
	if empty(b) {
		return 0, 0, false
	}
	line, col := pos.Line, pos.Col
	if line < 0 {
		line = 0
	}
	if line >= len(b.Lines) {
		line = len(b.Lines) - 1
		col = len(b.Lines[line].Runes)
	}
	if cur, ok := runeAt(b, line, col); ok && cur == open {
		return line, col, true
	}
	depth := 0
	for l := line; l >= 0; l-- {
		runes := b.Lines[l].Runes
		end := len(runes)
		if l == line {
			end = col
			if end > len(runes) {
				end = len(runes)
			}
		}
		for i := end - 1; i >= 0; i-- {
			switch runes[i] {
			case close:
				depth++
			case open:
				if depth == 0 {
					return l, i, true
				}
				depth--
			}
		}
	}
	return 0, 0, false
}

// findMatchingClose walks forward from (openLine, openCol) counting
// open/close depth; returns the position of the matching close. The
// open delim at the starting position is not counted.
func findMatchingClose(b *Buffer, openLine, openCol int, open, close rune) (int, int, bool) {
	depth := 1
	for l := openLine; l < len(b.Lines); l++ {
		runes := b.Lines[l].Runes
		start := 0
		if l == openLine {
			start = openCol + 1
		}
		for i := start; i < len(runes); i++ {
			switch runes[i] {
			case open:
				depth++
			case close:
				depth--
				if depth == 0 {
					return l, i, true
				}
			}
		}
	}
	return 0, 0, false
}

// InnerParagraph returns the range of contiguous non-blank lines
// around pos (vim's blank-line-delimited paragraph). On a blank line
// the range is empty (Start==End). The range is character-wise: it
// spans from the first column of the first non-blank line to the
// rune-end of the last non-blank line.
func InnerParagraph(b *Buffer, pos Position) (Range, bool) {
	if empty(b) || pos.Line < 0 || pos.Line >= len(b.Lines) {
		return Range{}, false
	}
	if isBlankLine(b.Lines[pos.Line].Runes) {
		return Range{}, false
	}
	startLine := pos.Line
	for startLine > 0 && !isBlankLine(b.Lines[startLine-1].Runes) {
		startLine--
	}
	endLine := pos.Line
	for endLine+1 < len(b.Lines) && !isBlankLine(b.Lines[endLine+1].Runes) {
		endLine++
	}
	return Range{
		Start: Position{Line: startLine, Col: 0},
		End:   Position{Line: endLine, Col: len(b.Lines[endLine].Runes)},
	}, true
}

// AroundParagraph extends InnerParagraph to include trailing blank
// lines (or leading blanks when there are no trailing ones, mirroring
// vim's `ap`).
func AroundParagraph(b *Buffer, pos Position) (Range, bool) {
	r, ok := InnerParagraph(b, pos)
	if !ok {
		return Range{}, false
	}
	endLine := r.End.Line
	for endLine+1 < len(b.Lines) && isBlankLine(b.Lines[endLine+1].Runes) {
		endLine++
	}
	if endLine == r.End.Line {
		startLine := r.Start.Line
		for startLine > 0 && isBlankLine(b.Lines[startLine-1].Runes) {
			startLine--
		}
		r.Start.Line = startLine
		r.Start.Col = 0
		return r, true
	}
	r.End.Line = endLine
	r.End.Col = len(b.Lines[endLine].Runes)
	return r, true
}

// InnerStatement returns the range of the SQL statement under pos,
// excluding the trailing `;`. Boundaries are the nearest `;` to the
// left (exclusive of leading whitespace? NO — vim `is` is mechanical:
// statement = text between `;` delimiters as-is) and the nearest `;`
// to the right. When pos sits ON a `;`, the preceding statement is
// returned.
func InnerStatement(b *Buffer, pos Position) (Range, bool) {
	return statementRange(b, pos, false)
}

// AroundStatement returns the inner statement range extended to
// include the trailing `;` and any leading whitespace runs.
func AroundStatement(b *Buffer, pos Position) (Range, bool) {
	return statementRange(b, pos, true)
}

func statementRange(b *Buffer, pos Position, around bool) (Range, bool) {
	if empty(b) {
		return Range{}, false
	}
	line := pos.Line
	col := pos.Col
	if line < 0 {
		line = 0
		col = 0
	}
	if line >= len(b.Lines) {
		line = len(b.Lines) - 1
		col = len(b.Lines[line].Runes)
	}
	onSemicolon := col < len(b.Lines[line].Runes) && b.Lines[line].Runes[col] == ';'
	var (
		left, right           Position
		leftFound, rightFound bool
	)
	if onSemicolon {
		// The ';' under the cursor is the right boundary of the
		// preceding statement; scan for the previous ';' (if any) as
		// the left boundary.
		right = Position{Line: line, Col: col}
		rightFound = true
		left, leftFound = scanCharBackward(b, line, col, ';')
	} else {
		left, leftFound = scanCharBackward(b, line, col, ';')
		right, rightFound = scanCharForward(b, line, col, ';')
	}
	var start Position
	if leftFound {
		start = stepAfter(b, left)
	} else {
		start = Position{Line: 0, Col: 0}
	}
	var end Position
	if rightFound {
		end = right
	} else {
		last := len(b.Lines) - 1
		end = Position{Line: last, Col: len(b.Lines[last].Runes)}
	}
	if !around {
		if !posLess(start, end) && start != end {
			return Range{}, false
		}
		return Range{Start: start, End: end}, true
	}
	if leftFound {
		start = expandLeadingWhitespace(b, start)
	}
	if rightFound {
		end = stepAfter(b, end)
	}
	if !posLess(start, end) && start != end {
		return Range{}, false
	}
	return Range{Start: start, End: end}, true
}

// scanCharForward scans from (line, col) inclusive for the first
// occurrence of c. Returns the matching Position and true; false when
// not found.
func scanCharForward(b *Buffer, line, col int, c rune) (Position, bool) {
	for l := line; l < len(b.Lines); l++ {
		runes := b.Lines[l].Runes
		start := 0
		if l == line {
			start = col
			if start < 0 {
				start = 0
			}
		}
		for i := start; i < len(runes); i++ {
			if runes[i] == c {
				return Position{Line: l, Col: i}, true
			}
		}
	}
	return Position{}, false
}

// scanCharBackward scans backward from (line, col) (exclusive of the
// rune AT (line, col)) for the first occurrence of c. Returns the
// matching Position and true; false when not found.
func scanCharBackward(b *Buffer, line, col int, c rune) (Position, bool) {
	for l := line; l >= 0; l-- {
		runes := b.Lines[l].Runes
		end := len(runes)
		if l == line {
			end = col
			if end > len(runes) {
				end = len(runes)
			}
		}
		for i := end - 1; i >= 0; i-- {
			if runes[i] == c {
				return Position{Line: l, Col: i}, true
			}
		}
	}
	return Position{}, false
}

// stepAfter returns the position immediately after p — one rune to
// the right, wrapping to the next line's column 0 at end-of-line.
// Used to convert a delimiter position into the position just past
// it.
func stepAfter(b *Buffer, p Position) Position {
	if p.Line < 0 || p.Line >= len(b.Lines) {
		return p
	}
	if p.Col < len(b.Lines[p.Line].Runes) {
		return Position{Line: p.Line, Col: p.Col + 1}
	}
	if p.Line+1 < len(b.Lines) {
		return Position{Line: p.Line + 1, Col: 0}
	}
	return p
}

// expandLeadingWhitespace walks p leftward across same-line whitespace
// (vim `as` includes the run of spaces preceding the statement).
func expandLeadingWhitespace(b *Buffer, p Position) Position {
	if p.Line < 0 || p.Line >= len(b.Lines) {
		return p
	}
	runes := b.Lines[p.Line].Runes
	col := p.Col
	for col > 0 && col-1 < len(runes) && unicode.IsSpace(runes[col-1]) && runes[col-1] != '\n' {
		col--
	}
	return Position{Line: p.Line, Col: col}
}
