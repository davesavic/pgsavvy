package editor

// Motion functions compute a new cursor Position from a starting
// position on a Buffer, without mutating the Buffer.
//
// Signature: every motion is `(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool)`.
// The frame carries the editor viewport (top visible line + height) for
// the view-relative motions (H/M/L); every other motion ignores it.
// The bool is false when the motion is rejected (negative count, empty
// buffer, position already at the bounding edge for the motion). When
// false, callers must NOT use the returned Position. A motion that
// reaches the bound (e.g. WordNext at last word) returns the bound
// Position and true.
//
// Count convention: count <= 0 is rejected (returns Position{}, false).
// The exec-context contract sets count=1 explicitly when the user
// supplied no prefix (ExecCtx.Count==0 → caller normalises to 1 before
// calling here).
//
// Concurrency: motion functions read b.Lines directly without taking
// b.mu. The invariant is that motions run on the gocui
// main-loop goroutine alongside every other Buffer mutator (Apply,
// Undo, Redo); there is no concurrent reader/writer to fight. If a
// future epic dispatches motions off the main loop, callers must
// arrange the lock themselves (e.g. snapshot b.LinesCopy() first).
//
// View-relative motions (H/M/L): these are vim "screen top/middle/bottom".
// They read the ViewFrame the controller threads in (top visible line +
// height) to resolve the first/middle/last VISIBLE line, and fall back
// to the whole-buffer first/middle/last line when no frame is supplied.

import "unicode"

// ViewFrame describes the editor viewport at the instant a motion is
// dispatched: Top is the buffer line currently at the top of the
// visible region and Height is the number of visible text rows. It is
// threaded through every motion so the view-relative motions (H/M/L)
// can resolve screen top/middle/bottom; all other motions ignore it.
//
// A zero-value ViewFrame (Height <= 0) means the viewport is
// unavailable — H/M/L then fall back to buffer-relative behaviour
// (first / middle / last line of the whole buffer) without panicking.
type ViewFrame struct {
	Top    int
	Height int
}

// available reports whether the frame carries a usable viewport.
func (f ViewFrame) available() bool { return f.Height > 0 }

// lastVisible returns the buffer line at the bottom of the visible
// region, clamped to the last addressable line of b.
func (f ViewFrame) lastVisible(b *Buffer) int {
	bottom := f.Top + f.Height - 1
	if hi := len(b.Lines) - 1; bottom > hi {
		return hi
	}
	return bottom
}

// classify is a 3-state classification of a rune for word motions.
// vim's `word` (lowercase) treats keyword runes as one class and
// punctuation as another; `WORD` (uppercase) lumps every non-space
// rune together. Whitespace is its own class for both.
type runeClass int

const (
	classSpace runeClass = iota
	classWord
	classPunct
)

func classifyWord(r rune) runeClass {
	if unicode.IsSpace(r) {
		return classSpace
	}
	if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
		return classWord
	}
	return classPunct
}

// classifyWORD collapses word + punct into the same class. Used by
// WORDNext / WORDPrev / WORDEnd.
func classifyWORD(r rune) runeClass {
	if unicode.IsSpace(r) {
		return classSpace
	}
	return classWord
}

// runeAt returns the rune at (line, col) and true; returns 0/false
// when out of range. Cursor positions Col==len(Runes) (the
// past-end / append slot) report as a space so motions treat
// line-end as whitespace for class purposes.
func runeAt(b *Buffer, line, col int) (rune, bool) {
	if line < 0 || line >= len(b.Lines) {
		return 0, false
	}
	rs := b.Lines[line].Runes
	if col < 0 || col > len(rs) {
		return 0, false
	}
	if col == len(rs) {
		return ' ', true
	}
	return rs[col], true
}

// stepRight advances (line, col) one rune to the right, wrapping to
// the start of the next line at line-end. Returns (line, col, true)
// or (line, col, false) when already at buffer end.
func stepRight(b *Buffer, line, col int) (int, int, bool) {
	if line < 0 || line >= len(b.Lines) {
		return line, col, false
	}
	if col < len(b.Lines[line].Runes) {
		return line, col + 1, true
	}
	if line+1 < len(b.Lines) {
		return line + 1, 0, true
	}
	return line, col, false
}

// stepLeft retreats one rune. Wraps to the end of the previous line
// at column 0. Returns false when already at buffer start.
func stepLeft(b *Buffer, line, col int) (int, int, bool) {
	if line < 0 || line >= len(b.Lines) {
		return line, col, false
	}
	if col > 0 {
		return line, col - 1, true
	}
	if line > 0 {
		prev := line - 1
		return prev, len(b.Lines[prev].Runes), true
	}
	return line, col, false
}

// validCount returns count normalised to a positive int, or (0, false)
// when count <= 0. Motion handlers should treat false as "no-op + ok=false".
func validCount(count int) (int, bool) {
	if count <= 0 {
		return 0, false
	}
	return count, true
}

// empty reports whether the buffer has no addressable position.
func empty(b *Buffer) bool {
	return b == nil || len(b.Lines) == 0
}

// --- Character motions ---

// CharLeft moves count characters left, wrapping over newlines.
func CharLeft(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := stepLeft(b, line, col)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

// CharRight moves count characters right, wrapping over newlines.
func CharRight(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := stepRight(b, line, col)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

// --- Line motions ---

// LineDown moves count lines down, clamping Col to the new line's
// length (vim's "preferred column" tracking is not modelled in MVP).
func LineDown(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	dst := pos.Line + n
	if dst >= len(b.Lines) {
		dst = len(b.Lines) - 1
	}
	if dst == pos.Line {
		return Position{}, false
	}
	col := pos.Col
	if l := len(b.Lines[dst].Runes); col > l {
		col = l
	}
	return Position{Line: dst, Col: col}, true
}

// LineUp moves count lines up.
func LineUp(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	dst := max(pos.Line-n, 0)
	if dst == pos.Line {
		return Position{}, false
	}
	col := pos.Col
	if l := len(b.Lines[dst].Runes); col > l {
		col = l
	}
	return Position{Line: dst, Col: col}, true
}

// LineStart returns column 0 of the current line (vim `0`). Count is
// accepted but ignored — `0` does not take a count in vim.
func LineStart(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if _, ok := validCount(count); !ok {
		return Position{}, false
	}
	if empty(b) || pos.Line < 0 || pos.Line >= len(b.Lines) {
		return Position{}, false
	}
	if pos.Col == 0 {
		return Position{}, false
	}
	return Position{Line: pos.Line, Col: 0}, true
}

// LineFirstNonBlank returns the first non-whitespace column of the
// current line (vim `^`). Falls back to column 0 on an all-blank
// line.
func LineFirstNonBlank(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if _, ok := validCount(count); !ok {
		return Position{}, false
	}
	if empty(b) || pos.Line < 0 || pos.Line >= len(b.Lines) {
		return Position{}, false
	}
	rs := b.Lines[pos.Line].Runes
	col := 0
	for i, r := range rs {
		if !unicode.IsSpace(r) {
			col = i
			break
		}
		col = i + 1
	}
	if col == pos.Col {
		return Position{}, false
	}
	return Position{Line: pos.Line, Col: col}, true
}

// LineEnd returns the last column of the current line (vim `$`).
// The result is the rune-len (past-end / append slot) — consistent
// with vim's `$` putting the cursor on the final printable char in
// Normal mode (callers in Normal mode should clamp by -1 if needed;
// in OperatorPending the past-end position is the correct delete bound).
func LineEnd(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if _, ok := validCount(count); !ok {
		return Position{}, false
	}
	if empty(b) || pos.Line < 0 || pos.Line >= len(b.Lines) {
		return Position{}, false
	}
	end := len(b.Lines[pos.Line].Runes)
	if pos.Col == end {
		return Position{}, false
	}
	return Position{Line: pos.Line, Col: end}, true
}

// --- Buffer motions ---

// BufferStart returns the first line, first non-blank column (vim `gg`).
// Count is accepted but ignored — vim `gg` with a count means
// "jump to line N", which is a different operation (out of scope).
func BufferStart(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if _, ok := validCount(count); !ok {
		return Position{}, false
	}
	if empty(b) {
		return Position{}, false
	}
	if pos.Line == 0 && pos.Col == 0 {
		return Position{}, false
	}
	return Position{Line: 0, Col: 0}, true
}

// BufferEnd returns the last line, column 0 (vim `G`). When count >= 2
// the motion is the explicit `NG` form: jump to line N (1-indexed),
// clamped to the last line. Bare `G` arrives here as count=1 (the
// handler normalises 0 → 1), which preserves the legacy "go to last
// line" behavior.
func BufferEnd(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if _, ok := validCount(count); !ok {
		return Position{}, false
	}
	if empty(b) {
		return Position{}, false
	}
	last := len(b.Lines) - 1
	dst := last
	if count >= 2 {
		dst = min(count-1, last)
	}
	if pos.Line == dst && pos.Col == 0 {
		return Position{}, false
	}
	return Position{Line: dst, Col: 0}, true
}

// --- Word motions ---

// WordNext moves count "word" steps forward. A vim word boundary
// fires on a transition between rune classes (word ↔ punct), and on
// the first non-space rune after any run of whitespace (including
// newlines).
func WordNext(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	return wordNext(b, pos, count, classifyWord)
}

// WORDNext moves count "WORD" steps forward (whitespace-delimited
// only, no punct/word distinction). Used by `W`.
func WORDNext(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	return wordNext(b, pos, count, classifyWORD)
}

func wordNext(b *Buffer, pos Position, count int, classify func(rune) runeClass) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := stepWordForward(b, line, col, classify)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

// stepWordForward advances one word boundary forward. Returns the
// new position. ok=false when already at buffer end.
func stepWordForward(b *Buffer, line, col int, classify func(rune) runeClass) (int, int, bool) {
	cur, ok := runeAt(b, line, col)
	if !ok {
		return line, col, false
	}
	startClass := classify(cur)
	// 1. Walk to end of current non-space class run.
	for startClass != classSpace {
		nl, nc, ok := stepRight(b, line, col)
		if !ok {
			return line, col, true
		}
		r, ok := runeAt(b, nl, nc)
		if !ok {
			return nl, nc, true
		}
		if classify(r) != startClass {
			line, col = nl, nc
			break
		}
		line, col = nl, nc
	}
	// 2. Skip any whitespace.
	for {
		r, ok := runeAt(b, line, col)
		if !ok {
			return line, col, true
		}
		if classify(r) != classSpace {
			return line, col, true
		}
		nl, nc, ok := stepRight(b, line, col)
		if !ok {
			return line, col, true
		}
		line, col = nl, nc
	}
}

// WordPrev moves count "word" steps backward (vim `b`).
func WordPrev(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	return wordPrev(b, pos, count, classifyWord)
}

// WORDPrev moves count "WORD" steps backward (vim `B`).
func WORDPrev(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	return wordPrev(b, pos, count, classifyWORD)
}

func wordPrev(b *Buffer, pos Position, count int, classify func(rune) runeClass) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := stepWordBackward(b, line, col, classify)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

// stepWordBackward retreats one word boundary. The destination is
// the FIRST rune of the previous non-space class run.
func stepWordBackward(b *Buffer, line, col int, classify func(rune) runeClass) (int, int, bool) {
	// Move one rune back first.
	nl, nc, ok := stepLeft(b, line, col)
	if !ok {
		return line, col, false
	}
	line, col = nl, nc
	// Skip any whitespace.
	for {
		r, ok := runeAt(b, line, col)
		if !ok {
			return line, col, true
		}
		if classify(r) != classSpace {
			break
		}
		nl, nc, ok := stepLeft(b, line, col)
		if !ok {
			return line, col, true
		}
		line, col = nl, nc
	}
	// Walk to start of the current class run.
	r, ok := runeAt(b, line, col)
	if !ok {
		return line, col, true
	}
	curClass := classify(r)
	for {
		nl, nc, ok := stepLeft(b, line, col)
		if !ok {
			return line, col, true
		}
		r, ok := runeAt(b, nl, nc)
		if !ok {
			return line, col, true
		}
		if classify(r) != curClass {
			return line, col, true
		}
		line, col = nl, nc
	}
}

// WordEnd moves count word-ends forward (vim `e`). The destination
// is the LAST rune of the next non-space class run (so it lands ON
// the final char, not past it).
func WordEnd(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	return wordEnd(b, pos, count, classifyWord)
}

// WORDEnd moves count WORD-ends forward (vim `E`).
func WORDEnd(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	return wordEnd(b, pos, count, classifyWORD)
}

func wordEnd(b *Buffer, pos Position, count int, classify func(rune) runeClass) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := stepWordEnd(b, line, col, classify)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

func stepWordEnd(b *Buffer, line, col int, classify func(rune) runeClass) (int, int, bool) {
	// Step right once unconditionally so `e` from the end of a word
	// moves to the end of the NEXT word, not stays put.
	nl, nc, ok := stepRight(b, line, col)
	if !ok {
		return line, col, false
	}
	line, col = nl, nc
	// Skip whitespace.
	for {
		r, ok := runeAt(b, line, col)
		if !ok {
			return line, col, true
		}
		if classify(r) != classSpace {
			break
		}
		nl, nc, ok := stepRight(b, line, col)
		if !ok {
			return line, col, true
		}
		line, col = nl, nc
	}
	// Walk to end of current class run — stop on the LAST rune.
	r, ok := runeAt(b, line, col)
	if !ok {
		return line, col, true
	}
	curClass := classify(r)
	for {
		nl, nc, ok := stepRight(b, line, col)
		if !ok {
			return line, col, true
		}
		r, ok := runeAt(b, nl, nc)
		if !ok {
			return line, col, true
		}
		if classify(r) != curClass {
			return line, col, true
		}
		line, col = nl, nc
	}
}

// --- Paragraph motions ---
//
// A vim paragraph is a maximal run of non-blank lines separated by
// one or more blank (empty or all-whitespace) lines. `{` jumps to
// the start of the previous paragraph; `}` jumps to the start of
// the next. Both land on the blank line that delimits the paragraph
// (or BOF/EOF when no blank line exists).

func isBlankLine(rs []rune) bool {
	for _, r := range rs {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// ParagraphPrev jumps count paragraphs backward.
func ParagraphPrev(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line := pos.Line
	moved := false
	for range n {
		nl, ok := paragraphBackwardOne(b, line)
		if !ok {
			break
		}
		line = nl
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: 0}, true
}

func paragraphBackwardOne(b *Buffer, line int) (int, bool) {
	cur := line - 1
	if cur < 0 {
		return 0, line != 0
	}
	// If on a non-blank line, walk up through this paragraph first.
	for cur >= 0 && !isBlankLine(b.Lines[cur].Runes) {
		cur--
	}
	// Now skip any blank-line run above (so consecutive blanks count
	// as one paragraph boundary).
	for cur >= 0 && cur > 0 && isBlankLine(b.Lines[cur-1].Runes) && isBlankLine(b.Lines[cur].Runes) {
		cur--
	}
	if cur < 0 {
		return 0, true
	}
	return cur, true
}

// ParagraphNext jumps count paragraphs forward.
func ParagraphNext(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line := pos.Line
	moved := false
	for range n {
		nl, ok := paragraphForwardOne(b, line)
		if !ok {
			break
		}
		line = nl
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: 0}, true
}

func paragraphForwardOne(b *Buffer, line int) (int, bool) {
	last := len(b.Lines) - 1
	cur := line + 1
	if cur > last {
		return last, line != last
	}
	// If already on a blank run, skip it.
	for cur <= last && isBlankLine(b.Lines[cur].Runes) {
		cur++
	}
	// Walk past the non-blank block until we hit a blank or EOF.
	for cur <= last && !isBlankLine(b.Lines[cur].Runes) {
		cur++
	}
	if cur > last {
		return last, true
	}
	return cur, true
}

// --- Sentence motions ---
//
// vim's sentence is "a sequence of characters ending at . ! ? followed
// by either EOL or whitespace, then any quotes/brackets". This ships
// a simpler approximation: any of `.!?` followed by space or EOL is a
// sentence boundary. The successor sentence starts at the first
// non-space rune after that boundary.

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// SentenceNext jumps count sentences forward (vim `)`).
func SentenceNext(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := sentenceForwardOne(b, line, col)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

func sentenceForwardOne(b *Buffer, line, col int) (int, int, bool) {
	// Step forward at least once so we don't sit on the same boundary.
	nl, nc, ok := stepRight(b, line, col)
	if !ok {
		return line, col, false
	}
	line, col = nl, nc
	for {
		// On a blank line? Treat the blank line as a sentence boundary.
		if line >= 0 && line < len(b.Lines) && isBlankLine(b.Lines[line].Runes) {
			// Skip blanks; land on first non-blank line, col 0.
			for line < len(b.Lines) && isBlankLine(b.Lines[line].Runes) {
				line++
			}
			if line >= len(b.Lines) {
				return len(b.Lines) - 1, 0, true
			}
			return line, 0, true
		}
		r, ok := runeAt(b, line, col)
		if !ok {
			return line, col, true
		}
		if isSentenceEnd(r) {
			// Need a space or EOL after.
			ahead, aheadOK := runeAt(b, line, col+1)
			if !aheadOK || unicode.IsSpace(ahead) {
				// Skip the punctuation and any following whitespace.
				nl, nc, ok := stepRight(b, line, col)
				if !ok {
					return line, col, true
				}
				line, col = nl, nc
				for {
					r, ok := runeAt(b, line, col)
					if !ok {
						return line, col, true
					}
					if !unicode.IsSpace(r) {
						return line, col, true
					}
					nl, nc, ok := stepRight(b, line, col)
					if !ok {
						return line, col, true
					}
					line, col = nl, nc
				}
			}
		}
		nl, nc, ok := stepRight(b, line, col)
		if !ok {
			return line, col, true
		}
		line, col = nl, nc
	}
}

// SentencePrev jumps count sentences backward (vim `(`).
func SentencePrev(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	line, col := pos.Line, pos.Col
	moved := false
	for range n {
		nl, nc, ok := sentenceBackwardOne(b, line, col)
		if !ok {
			break
		}
		line, col = nl, nc
		moved = true
	}
	if !moved {
		return Position{}, false
	}
	return Position{Line: line, Col: col}, true
}

func sentenceBackwardOne(b *Buffer, line, col int) (int, int, bool) {
	// Step back at least once.
	nl, nc, ok := stepLeft(b, line, col)
	if !ok {
		return line, col, false
	}
	line, col = nl, nc
	for {
		// Skip any whitespace.
		for {
			r, ok := runeAt(b, line, col)
			if !ok {
				break
			}
			if !unicode.IsSpace(r) {
				break
			}
			nl, nc, ok := stepLeft(b, line, col)
			if !ok {
				return line, col, true
			}
			line, col = nl, nc
		}
		// Walk back until we find sentence-end punctuation followed
		// by whitespace, then the next non-space is our sentence start.
		for {
			// Check the rune before us.
			pl, pc, ok := stepLeft(b, line, col)
			if !ok {
				return line, col, true
			}
			r, ok := runeAt(b, pl, pc)
			if !ok {
				return line, col, true
			}
			if isSentenceEnd(r) {
				// Confirmed boundary; current (line, col) is the start.
				return line, col, true
			}
			// Also: if we crossed onto a blank line, that's a paragraph boundary.
			if pl != line && pl >= 0 && pl < len(b.Lines) && isBlankLine(b.Lines[pl].Runes) {
				return line, col, true
			}
			line, col = pl, pc
			if line == 0 && col == 0 {
				return line, col, true
			}
		}
	}
}

// --- Screen-relative motions (H / M / L) ---
//
// These resolve the first / middle / last VISIBLE buffer line from the
// ViewFrame the controller threads in. When the frame is unavailable
// (zero value — headless test rigs, view not yet wired) they fall back
// to the whole-buffer first / middle / last line, so behaviour is
// unchanged when the buffer fits inside the viewport.

// ScreenTop is `H`: the count-th line from the top of the viewport
// (1H == top visible line). Falls back to the first buffer line.
func ScreenTop(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if !frame.available() {
		return BufferStart(b, pos, count, frame)
	}
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	return screenLand(b, pos, frame, frame.Top+n-1)
}

// ScreenMiddle is `M`: the middle visible line. Count is ignored (vim
// semantics). Falls back to the middle line of the whole buffer.
func ScreenMiddle(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if _, ok := validCount(count); !ok {
		return Position{}, false
	}
	if empty(b) {
		return Position{}, false
	}
	if !frame.available() {
		return landLine(pos, len(b.Lines)/2)
	}
	return screenLand(b, pos, frame, frame.Top+(frame.lastVisible(b)-frame.Top)/2)
}

// ScreenBottom is `L`: the count-th line from the bottom of the
// viewport (1L == last visible line). Falls back to the last buffer line.
func ScreenBottom(b *Buffer, pos Position, count int, frame ViewFrame) (Position, bool) {
	if !frame.available() {
		return BufferEnd(b, pos, count, frame)
	}
	n, ok := validCount(count)
	if !ok || empty(b) {
		return Position{}, false
	}
	return screenLand(b, pos, frame, frame.lastVisible(b)-(n-1))
}

// screenLand clamps target into the visible [Top, lastVisible] range
// and lands on column 0, returning ok=false when the cursor is already
// there (the op-pending no-op convention shared with BufferStart/End).
func screenLand(b *Buffer, pos Position, frame ViewFrame, target int) (Position, bool) {
	if lo := frame.Top; target < lo {
		target = lo
	}
	if hi := frame.lastVisible(b); target > hi {
		target = hi
	}
	return landLine(pos, target)
}

// landLine returns line at column 0, or ok=false when pos already sits
// at {line, 0}.
func landLine(pos Position, line int) (Position, bool) {
	if pos.Line == line && pos.Col == 0 {
		return Position{}, false
	}
	return Position{Line: line, Col: 0}, true
}
