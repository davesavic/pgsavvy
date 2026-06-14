package editor

import "errors"

// jumpListCap matches the epic Architecture Decision 9 — 100-entry
// ring, push-only in MVP. Bidirectional walk (`<C-o>`/`<C-i>`)
// deferred to a successor epic.
const jumpListCap = 100

// ErrEmptyBuffer is returned by SetCursor when the buffer has no
// lines — there is no valid cursor target until at least one line
// (possibly empty) exists.
var ErrEmptyBuffer = errors.New("editor: cursor set on empty buffer")

// ErrCursorOutOfRange is returned by SetCursor when Line falls
// outside `[0, len(Lines))`. Col is clamped rather than rejected so
// motions can drive past line-end without a special case.
var ErrCursorOutOfRange = errors.New("editor: cursor line out of range")

// JumpList is the bounded ring buffer of recent cursor positions
// (vim's `<C-o>` / `<C-i>` stack). MVP exposes only Push; Len and
// At are test-only accessors. Eviction drops the oldest entry once
// jumpListCap is exceeded.
//
// Concurrency: JumpList is NOT safe for concurrent use. Buffer
// serialises every access through Buffer.mu.
type JumpList struct {
	entries []Position
	head    int
	full    bool
}

func newJumpList() *JumpList {
	return &JumpList{entries: make([]Position, jumpListCap)}
}

// Push appends p, evicting the oldest entry once the ring fills.
func (j *JumpList) Push(p Position) {
	j.entries[j.head] = p
	j.head++
	if j.head == jumpListCap {
		j.head = 0
		j.full = true
	}
}

// Len returns the live entry count, 0..jumpListCap.
func (j *JumpList) Len() int {
	if j.full {
		return jumpListCap
	}
	return j.head
}

// At returns the i-th entry in chronological order (oldest first).
// Out-of-range i returns the zero Position.
func (j *JumpList) At(i int) Position {
	n := j.Len()
	if i < 0 || i >= n {
		return Position{}
	}
	if !j.full {
		return j.entries[i]
	}
	return j.entries[(j.head+i)%jumpListCap]
}

// NewBuffer returns a Buffer with Jumps (cap 100) initialised.
// zero-valued Buffers (`&Buffer{}`) still work for the legacy
// buffer_test.go path — Jumps is lazily allocated by Push when nil.
func NewBuffer() *Buffer {
	return &Buffer{
		Jumps: newJumpList(),
	}
}

// SetCursor writes p to b.Cursor after validating Line and clamping
// Col to `[0, len(Lines[Line].Runes)]`. Empty buffer returns
// ErrEmptyBuffer; out-of-range Line returns ErrCursorOutOfRange.
// Negative Col clamps to 0; Col past line-end clamps to line-end
// (col == rune-len is valid — that's the append-past-end position).
//
// This is the validating package-level helper. The unvalidated
// b.SetCursor method on Buffer remains for VimEditor's internal
// post-Apply cursor sync (which always passes already-valid pos
// from advancePos).
func SetCursor(b *Buffer, p Position) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.Lines) == 0 {
		return ErrEmptyBuffer
	}
	if p.Line < 0 || p.Line >= len(b.Lines) {
		return ErrCursorOutOfRange
	}
	if p.Col < 0 {
		p.Col = 0
	}
	runeLen := len(b.Lines[p.Line].Runes)
	if p.Col > runeLen {
		p.Col = runeLen
	}
	b.Cursor = p
	return nil
}
