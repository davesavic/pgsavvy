package editor

import (
	"strings"
	"unicode"
)

// ShiftWidth is the fixed indent step (in spaces) for the >>/<< operators.
// Hardcoded for MVP per epic dbsavvy-wwd Architecture; vim's per-buffer
// `&shiftwidth` option is deferred to a successor epic.
const ShiftWidth = 2

// Delete removes the runes covered by r and returns the deleted text
// (the "cut" string written to a vim register). The Buffer's UndoTree
// records a Delete Edit; Apply errors propagate. A nil buffer or empty
// range (Start == End) is a no-op returning ("", nil).
//
// Range normalisation: callers MUST hand Delete a range whose
// (Start, End) are in lex order (operator handlers normalise via
// normaliseSelection before calling). LineWise ranges remove whole
// lines from Start.Line through End.Line inclusive.
func Delete(b *Buffer, r Range) (string, error) {
	if b == nil {
		return "", nil
	}
	if r.BlockWise {
		return deleteBlock(b, r)
	}
	cut := b.TextInRange(r)
	if cut == "" && r.Start == r.End && !r.LineWise {
		return "", nil
	}
	if r.LineWise {
		// LineWise yanks vim-style: trailing newline appended so paste
		// with `p` opens a new line. TextInRange returns the joined
		// content without a trailing newline; add one for the register.
		cut = cut + "\n"
	}
	if err := b.Apply(Edit{Kind: EditKindDelete, Range: r}); err != nil {
		return "", err
	}
	// LineWise dd on a single-line buffer would empty Lines entirely;
	// vim leaves an empty line so the buffer always has at least one
	// addressable row. Mirror that here so motion/textobject handlers
	// can still call b.SetCursor(Position{0,0}) post-delete.
	if r.LineWise {
		b.mu.Lock()
		if len(b.Lines) == 0 {
			b.Lines = []Line{{Runes: []rune{}}}
			b.Cursor = Position{Line: 0, Col: 0}
		}
		b.mu.Unlock()
	}
	return cut, nil
}

// deleteBlock removes the column rectangle of a VisualBlock range and
// returns the cut rectangle (rows joined by '\n'). The mutation is one
// EditKindReplace over the full affected line-span [minLine..maxLine],
// with each row rebuilt minus its [minCol, maxCol) slice — a single undo
// step whose reverse the Apply machinery captures automatically. Ragged
// rows (shorter than maxCol) lose only the columns they have.
func deleteBlock(b *Buffer, r Range) (string, error) {
	cut := b.TextInRange(r)
	minLine, maxLine := minMax(r.Start.Line, r.End.Line)
	minCol, maxCol := minMax(r.Start.Col, r.End.Col)
	rows := make([]string, 0, maxLine-minLine+1)
	for line := minLine; line <= maxLine; line++ {
		runes := lineRunesSnapshot(b, line)
		lo := min(minCol, len(runes))
		hi := min(maxCol, len(runes))
		rows = append(rows, string(runes[:lo])+string(runes[hi:]))
	}
	rng := Range{
		Start: Position{Line: minLine, Col: 0},
		End:   Position{Line: maxLine, Col: lineRuneLenSnapshot(b, maxLine)},
	}
	if err := b.Apply(Edit{Kind: EditKindReplace, Range: rng, Text: strings.Join(rows, "\n")}); err != nil {
		return "", err
	}
	return cut, nil
}

// Yank returns the text covered by r WITHOUT mutating the buffer. The
// returned string is what callers write to a vim register. LineWise
// ranges include the trailing newline so paste-after `p` semantics
// match vim. A nil buffer returns "".
func Yank(b *Buffer, r Range) string {
	if b == nil {
		return ""
	}
	text := b.TextInRange(r)
	if r.LineWise {
		return text + "\n"
	}
	return text
}

// Change deletes the runes covered by r and returns the cut text. The
// caller (operator handler) is responsible for flipping the editor into
// ModeInsert after Change returns — the editor function itself is
// mutation-only and does not touch mode.
//
// Behaviour is identical to Delete in MVP (no `cw` / `ciw` whitespace
// trimming distinction yet); the separation keeps the call-site
// register-name semantics clear and leaves room for a future tweak.
func Change(b *Buffer, r Range) (string, error) {
	return Delete(b, r)
}

// Upper uppercases every rune in the range covered by r. The Buffer's
// UndoTree records a single Replace Edit. LineWise ranges operate on
// whole lines; character-wise ranges keep surrounding text untouched.
// A nil buffer or empty range is a no-op.
func Upper(b *Buffer, r Range) error {
	return transformRange(b, r, unicode.ToUpper)
}

// Lower lowercases every rune in the range covered by r. See Upper for
// semantics.
func Lower(b *Buffer, r Range) error {
	return transformRange(b, r, unicode.ToLower)
}

// ToggleCase flips the case of every rune in the range covered by r:
// upper→lower, lower→upper, non-letters untouched (vim `~`). See Upper
// for range semantics; non-letters are a clean no-op via transformRange.
func ToggleCase(b *Buffer, r Range) error {
	return transformRange(b, r, toggleCaseRune)
}

func toggleCaseRune(r rune) rune {
	if unicode.IsUpper(r) {
		return unicode.ToLower(r)
	}
	if unicode.IsLower(r) {
		return unicode.ToUpper(r)
	}
	return r
}

// IndentRight inserts ShiftWidth spaces at column 0 of every line in
// [startLine, endLine] inclusive. Empty lines are indented too (vim's
// behaviour). A nil buffer or out-of-range line is a no-op.
func IndentRight(b *Buffer, startLine, endLine int) error {
	if b == nil {
		return nil
	}
	if startLine > endLine {
		startLine, endLine = endLine, startLine
	}
	indent := strings.Repeat(" ", ShiftWidth)
	for line := startLine; line <= endLine; line++ {
		// Insert spaces at column 0 of each line.
		pos := Position{Line: line, Col: 0}
		if err := b.Apply(Edit{
			Kind:  EditKindInsert,
			Range: Range{Start: pos, End: pos},
			Text:  indent,
		}); err != nil {
			// Out-of-range line: stop the run; previous inserts are kept.
			if err == ErrEditOutOfRange {
				return nil
			}
			return err
		}
	}
	return nil
}

// IndentLeft removes up to ShiftWidth leading spaces from every line in
// [startLine, endLine] inclusive. Lines with fewer than ShiftWidth
// leading spaces are dedented to column 0 (their leading whitespace is
// fully removed). Lines starting with a non-space rune are unchanged.
// A nil buffer or out-of-range line is a no-op.
func IndentLeft(b *Buffer, startLine, endLine int) error {
	if b == nil {
		return nil
	}
	if startLine > endLine {
		startLine, endLine = endLine, startLine
	}
	for line := startLine; line <= endLine; line++ {
		runes := lineRunesSnapshot(b, line)
		if runes == nil {
			continue
		}
		strip := 0
		for strip < ShiftWidth && strip < len(runes) && runes[strip] == ' ' {
			strip++
		}
		if strip == 0 {
			continue
		}
		from := Position{Line: line, Col: 0}
		to := Position{Line: line, Col: strip}
		if err := b.Apply(Edit{Kind: EditKindDelete, Range: Range{Start: from, End: to}}); err != nil {
			if err == ErrEditOutOfRange {
				return nil
			}
			return err
		}
	}
	return nil
}

// transformRange applies a per-rune fold (Upper/Lower) over r as a single
// Replace edit so the operation lands as one undo step. Empty input
// (Start == End in char-wise) returns nil.
func transformRange(b *Buffer, r Range, fold func(rune) rune) error {
	if b == nil {
		return nil
	}
	text := b.TextInRange(r)
	if text == "" && r.Start == r.End && !r.LineWise {
		return nil
	}
	folded := strings.Map(fold, text)
	if folded == text {
		// Nothing actually changes; skip the edit so the undo stack
		// doesn't grow with no-op entries.
		return nil
	}
	if r.LineWise {
		// LineWise Replace is awkward (whole-line semantics + a string).
		// Re-build the range as character-wise covering the same span.
		runes := lineRunesSnapshot(b, r.End.Line)
		r = Range{
			Start: Position{Line: r.Start.Line, Col: 0},
			End:   Position{Line: r.End.Line, Col: len(runes)},
		}
	}
	return b.Apply(Edit{Kind: EditKindReplace, Range: r, Text: folded})
}

// lineRunesSnapshot returns a copy of Lines[line].Runes under b.mu.RLock,
// or nil when the line index is out of range. Used by IndentLeft /
// transformRange to read line content without holding the lock across
// the subsequent Apply (which takes the write lock itself).
func lineRunesSnapshot(b *Buffer, line int) []rune {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if line < 0 || line >= len(b.Lines) {
		return nil
	}
	out := make([]rune, len(b.Lines[line].Runes))
	copy(out, b.Lines[line].Runes)
	return out
}

// NormaliseRange returns r with (Start, End) in lex order so operator
// handlers can call this on Buffer.Selection (which may be backwards
// from the anchor) before passing into Delete/Yank/etc.
func NormaliseRange(r Range) Range {
	if posLess(r.End, r.Start) {
		r.Start, r.End = r.End, r.Start
	}
	return r
}

// LineWiseFromVisualLine converts a VisualLine selection into a LineWise
// Range covering the same line span. Cols are dropped (Start.Col = 0,
// End.Col = len of End line) so downstream Delete/Yank/etc. see the
// whole-line geometry.
func LineWiseFromVisualLine(b *Buffer, r Range) Range {
	r = NormaliseRange(r)
	r.LineWise = true
	r.BlockWise = false
	if b == nil {
		return r
	}
	r.Start.Col = 0
	if r.End.Line >= 0 && r.End.Line < lineCountSnapshot(b) {
		r.End.Col = lineRuneLenSnapshot(b, r.End.Line)
	}
	return r
}

// CurrentLineLineWise constructs a LineWise Range over the current line
// only — backs `dd`, `yy`, `cc`, `>>`, `<<`. With count > 1 it spans the
// current line plus (count-1) lines below.
func CurrentLineLineWise(b *Buffer, cursor Position, count int) Range {
	if count <= 0 {
		count = 1
	}
	startLine := cursor.Line
	endLine := startLine + count - 1
	last := lineCountSnapshot(b) - 1
	if endLine > last {
		endLine = last
	}
	if startLine > endLine {
		endLine = startLine
	}
	endCol := 0
	if endLine >= 0 && endLine <= last {
		endCol = lineRuneLenSnapshot(b, endLine)
	}
	return Range{
		Start:    Position{Line: startLine, Col: 0},
		End:      Position{Line: endLine, Col: endCol},
		LineWise: true,
	}
}

func lineCountSnapshot(b *Buffer) int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.Lines)
}

func lineRuneLenSnapshot(b *Buffer, line int) int {
	if b == nil {
		return 0
	}
	return b.LineRuneLen(line)
}
