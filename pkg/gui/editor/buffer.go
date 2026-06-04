package editor

import (
	"errors"
	"strings"
	"sync"
	"unicode/utf8"
)

// ErrEditOutOfRange is returned by Buffer.Apply when any endpoint of
// the proposed Edit falls outside the buffer's current Lines.
// Validation runs against the pre-Apply snapshot, so an
// ErrEditOutOfRange leaves Lines, History, and Dirty all unchanged
// (atomic Replace semantics).
var ErrEditOutOfRange = errors.New("editor: edit out of range")

// undoCap matches vim's default undolevels=1000. Buffer lazily
// constructs its UndoTree at first Apply with this cap unless the
// History field is pre-populated by a caller (e.g. wwd.9 hydration).
const undoCap = 1000

// Line is one editor row. Runes (not bytes) is the storage choice so
// cursor columns and motions reason in rune coordinates without
// repeated UTF-8 decoding. The Highlights []Span field DESIGN.md
// §13.1 mentions is deferred to the highlighter epic
// ([[dbsavvy-wwd-highlighter]]); wwd.2 keeps Line minimal.
type Line struct {
	Runes []rune
}

// Position is the (line, column) coordinate of a point inside the
// editor buffer. Zero-indexed; Col is in runes so callers compare
// against Line.Runes directly.
type Position struct {
	Line int
	Col  int
}

// Range is the half-open span [Start, End) in (Line, Col) rune
// coordinates.
//
// LineWise flags a vim line-wise range (dd, yy, V): TextInRange
// emits every whole line in [Start.Line, End.Line] regardless of
// Col; deleteRangeLocked removes whole lines. BlockWise is reserved
// for the VisualBlock motion (wwd.7 / wwd.8); wwd.2 stores the
// field and treats it as character-wise.
type Range struct {
	Start, End Position
	LineWise   bool
	BlockWise  bool
}

// Buffer is the canonical text + cursor + undo state for one
// QUERY_EDITOR pane.
//
// Concurrency: mu (sync.RWMutex) guards every field. Method
// receivers grab RLock for read-only accessors and Lock for
// mutators; the worker-dispatched SaveBuffer in wwd.9 takes a
// LinesCopy snapshot on the MainLoop before handing the copy off to
// disk so mu is never held across the file write.
//
// Buffer intentionally has NO Mode field — keys.ModeStore[QUERY_EDITOR]
// is the sole source of truth (Architecture Decision 1 of epic
// dbsavvy-wwd).
type Buffer struct {
	mu sync.RWMutex

	Lines     []Line
	Cursor    Position
	Marks     map[rune]Position
	Jumps     *JumpList
	History   *UndoTree
	Selection *Range

	// yankFlash is the transient post-yank highlight range (Neovim
	// on_yank parity). yankFlashEpoch monotonically increments per
	// SetYankFlash so a delayed clear timer only clears the flash it
	// armed; both are guarded by mu.
	yankFlash      *Range
	yankFlashEpoch uint64

	ConnectionID string
	Path         string
	UUID         string
	Dirty        bool
}

// Apply validates e against the pre-Apply Lines snapshot, computes
// the reverse Edit, then atomically mutates Lines, appends the
// reverse to History, sets Dirty, and calls cancelSelectionIfOverlap.
// Any out-of-range endpoint returns ErrEditOutOfRange and leaves
// Lines, History, and Dirty unchanged.
func (b *Buffer) Apply(e Edit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.applyRecordLocked(e)
}

func (b *Buffer) applyRecordLocked(e Edit) error {
	if !b.editInRangeLocked(e) {
		return ErrEditOutOfRange
	}
	rev := b.buildReverseLocked(e)
	b.mutateLocked(e)
	b.clampCursorLocked()
	e.reverse = &rev
	if b.History == nil {
		b.History = NewUndoTree(undoCap)
	}
	b.History.Apply(e)
	b.Dirty = true
	b.cancelSelectionIfOverlap(e.Range)
	b.clearYankFlashLocked()
	return nil
}

// clampCursorLocked repositions b.Cursor to the nearest valid Position
// after a mutation may have shrunk Lines or the cursor's line — a delete
// that removes the cursor's line (e.g. `dd` on the last line) otherwise
// leaves Cursor.Line dangling past the buffer end, which makes every
// subsequent Insert fail editInRangeLocked with ErrEditOutOfRange (the
// "stuck in insert mode, can't type" bug). An empty buffer collapses the
// cursor to the origin. Idempotent for an already-valid cursor, so
// callers that SetCursor explicitly after Apply are unaffected. Caller
// must hold b.mu.
func (b *Buffer) clampCursorLocked() {
	if len(b.Lines) == 0 {
		b.Cursor = Position{}
		return
	}
	if b.Cursor.Line < 0 {
		b.Cursor.Line = 0
	}
	if b.Cursor.Line >= len(b.Lines) {
		b.Cursor.Line = len(b.Lines) - 1
	}
	if b.Cursor.Col < 0 {
		b.Cursor.Col = 0
	}
	if max := len(b.Lines[b.Cursor.Line].Runes); b.Cursor.Col > max {
		b.Cursor.Col = max
	}
}

// Undo applies the inverse of the most recent recorded Edit and
// rewinds the History cursor one step. Returns nil and no-ops when
// History is nil or already at the root sentinel. The replayed
// reverse mutation is NOT recorded — History's cursor position is
// the authoritative pointer over branching undo state.
func (b *Buffer) Undo() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.History == nil {
		return nil
	}
	rev, ok := b.History.Undo()
	if !ok {
		return nil
	}
	b.mutateLocked(rev)
	b.clampCursorLocked()
	b.Dirty = true
	b.cancelSelectionIfOverlap(rev.Range)
	b.clearYankFlashLocked()
	return nil
}

// Redo walks the History cursor forward along the current branch
// (children[0]) and re-applies the recorded forward Edit. Returns
// nil and no-ops when there is nothing to redo. The replayed Edit
// is NOT re-recorded.
func (b *Buffer) Redo() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.History == nil {
		return nil
	}
	fwd, ok := b.History.Redo()
	if !ok {
		return nil
	}
	b.mutateLocked(fwd)
	b.clampCursorLocked()
	b.Dirty = true
	b.cancelSelectionIfOverlap(fwd.Range)
	b.clearYankFlashLocked()
	return nil
}

// String returns the buffer's text as a single \n-joined string. An
// empty Lines slice returns "". A trailing newline appears only when
// the final Line is empty (i.e., the buffer ends on a blank row).
func (b *Buffer) String() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.stringLocked()
}

func (b *Buffer) stringLocked() string {
	if len(b.Lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, l := range b.Lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(string(l.Runes))
	}
	return sb.String()
}

// TextInRange returns the substring spanning r. Line-wise ranges
// emit every whole line from Start.Line to End.Line inclusive,
// joined with \n; character-wise ranges honour Start.Col and End.Col
// (half-open at End). An out-of-range r returns "".
func (b *Buffer) TextInRange(r Range) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.textInRangeLocked(r)
}

func (b *Buffer) textInRangeLocked(r Range) string {
	if !b.rangeInBoundsLocked(r) {
		return ""
	}
	if r.LineWise {
		var sb strings.Builder
		for i := r.Start.Line; i <= r.End.Line && i < len(b.Lines); i++ {
			if i > r.Start.Line {
				sb.WriteByte('\n')
			}
			sb.WriteString(string(b.Lines[i].Runes))
		}
		return sb.String()
	}
	if r.Start.Line == r.End.Line {
		return string(b.Lines[r.Start.Line].Runes[r.Start.Col:r.End.Col])
	}
	var sb strings.Builder
	sb.WriteString(string(b.Lines[r.Start.Line].Runes[r.Start.Col:]))
	sb.WriteByte('\n')
	for l := r.Start.Line + 1; l < r.End.Line; l++ {
		sb.WriteString(string(b.Lines[l].Runes))
		sb.WriteByte('\n')
	}
	sb.WriteString(string(b.Lines[r.End.Line].Runes[:r.End.Col]))
	return sb.String()
}

// CursorByteOffset returns the byte offset of Cursor into String().
// Each preceding line contributes its UTF-8 byte length plus one
// for the joining newline; the current line contributes the UTF-8
// byte length of Runes[:Cursor.Col]. An out-of-range Cursor is
// clamped to the line's end.
func (b *Buffer) CursorByteOffset() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	off := 0
	for i := 0; i < b.Cursor.Line && i < len(b.Lines); i++ {
		off += utf8ByteLen(b.Lines[i].Runes)
		off++
	}
	if b.Cursor.Line < len(b.Lines) {
		col := b.Cursor.Col
		runes := b.Lines[b.Cursor.Line].Runes
		if col > len(runes) {
			col = len(runes)
		}
		off += utf8ByteLen(runes[:col])
	}
	return off
}

// LineRuneLen returns the rune count of the line at index line, or 0
// when line is out of range. Snapshot reads under b.mu.RLock.
func (b *Buffer) LineRuneLen(line int) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if line < 0 || line >= len(b.Lines) {
		return 0
	}
	return len(b.Lines[line].Runes)
}

// LineCount returns the number of lines in the buffer. Snapshot read
// under b.mu.RLock.
func (b *Buffer) LineCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.Lines)
}

// SetCursor writes p to Cursor under b.mu. Used by VimEditor after
// Apply to keep Cursor following the insertion point; future motion
// handlers (wwd.5) call this too.
func (b *Buffer) SetCursor(p Position) {
	b.mu.Lock()
	b.Cursor = p
	b.mu.Unlock()
}

// CursorPos returns a snapshot of Cursor under b.mu.RLock.
func (b *Buffer) CursorPos() Position {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Cursor
}

func (b *Buffer) SelectionSnapshot() *Range {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.Selection == nil {
		return nil
	}
	cp := *b.Selection
	return &cp
}

// SetYankFlash stores a copy of r as the active post-yank highlight,
// bumps the flash epoch, and returns the new epoch. The caller passes
// the returned epoch to a delayed ClearYankFlash so a later yank that
// re-arms the flash invalidates the earlier timer.
func (b *Buffer) SetYankFlash(r Range) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := r
	b.yankFlash = &cp
	b.yankFlashEpoch++
	return b.yankFlashEpoch
}

// ClearYankFlash clears the flash only when epoch matches the current
// flash epoch; a stale epoch (a newer SetYankFlash has since run, or a
// mutation already cleared the flash) is a no-op.
func (b *Buffer) ClearYankFlash(epoch uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if epoch != b.yankFlashEpoch {
		return
	}
	b.yankFlash = nil
}

// YankFlashSnapshot returns a copy of the active flash range, or nil
// when no flash is armed. Mirrors SelectionSnapshot.
func (b *Buffer) YankFlashSnapshot() *Range {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.yankFlash == nil {
		return nil
	}
	cp := *b.yankFlash
	return &cp
}

// clearYankFlashLocked drops the flash on the next text mutation
// (Apply/Undo/Redo) for Neovim on_yank parity. The epoch is left
// untouched so any in-flight delayed ClearYankFlash still no-ops.
// Caller must hold b.mu.
func (b *Buffer) clearYankFlashLocked() {
	b.yankFlash = nil
}

// LinesCopy returns a deep copy of Lines safe to hand off to a
// worker goroutine. Mutating the returned slice (or any Line.Runes
// within it) does not affect the Buffer's internal state.
func (b *Buffer) LinesCopy() []Line {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.Lines) == 0 {
		return nil
	}
	out := make([]Line, len(b.Lines))
	for i, l := range b.Lines {
		cp := make([]rune, len(l.Runes))
		copy(cp, l.Runes)
		out[i] = Line{Runes: cp}
	}
	return out
}

// SelectionText returns the text covered by the live Selection under
// b.mu.RLock, or ("", false) when no Selection is set. A single read
// path that holds the lock across both the nil-check and the
// TextInRange read keeps adapters race-free even if a concurrent
// ExitVisual / cancelSelectionIfOverlap clears Selection.
func (b *Buffer) SelectionText() (string, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.Selection == nil {
		return "", false
	}
	return b.textInRangeLocked(*b.Selection), true
}

// cancelSelectionIfOverlap drops b.Selection when the supplied edit
// Range overlaps it in (Line, Col) lex order, so an edit inside a
// visual range cannot leave a stale Selection pointing at moved runes.
// Overlap test (half-open [a,b) vs [c,d)): a < d && c < b.
//
// The caller (applyRecordLocked / Undo / Redo) already holds b.mu.
// Non-overlapping edits leave Selection intact so a write outside the
// active visual range — e.g. background fill, sibling cursor edit —
// is invisible to the user's selection.
func (b *Buffer) cancelSelectionIfOverlap(r Range) {
	if b.Selection == nil {
		return
	}
	sel := *b.Selection
	selStart, selEnd := sel.Start, sel.End
	if posLess(selEnd, selStart) {
		selStart, selEnd = selEnd, selStart
	}
	editStart, editEnd := r.Start, r.End
	if posLess(editEnd, editStart) {
		editStart, editEnd = editEnd, editStart
	}
	// Overlap: editStart < selEnd && selStart < editEnd. Equal endpoints
	// don't overlap (half-open), but a zero-length edit at the selection
	// boundary still counts as touching and is cleared defensively.
	if posLess(editStart, selEnd) && posLess(selStart, editEnd) {
		b.Selection = nil
		return
	}
	// Boundary touch: a zero-length insert (editStart == editEnd) anchored
	// inside [selStart, selEnd] is split across the selection by insertAtLocked;
	// cancel to avoid stale offsets.
	if editStart == editEnd && !posLess(editStart, selStart) && posLess(editStart, selEnd) {
		b.Selection = nil
	}
}

// editInRangeLocked validates both endpoints of e.Range. Insert
// only consults Start (End is unused by Insert); Delete and Replace
// require both Start and End in bounds and Start ≤ End.
func (b *Buffer) editInRangeLocked(e Edit) bool {
	if !b.positionInBoundsLocked(e.Range.Start) {
		return false
	}
	if e.Kind == EditKindInsert {
		return true
	}
	if !b.positionInBoundsLocked(e.Range.End) {
		return false
	}
	return !posLess(e.Range.End, e.Range.Start)
}

func (b *Buffer) rangeInBoundsLocked(r Range) bool {
	if !b.positionInBoundsLocked(r.Start) || !b.positionInBoundsLocked(r.End) {
		return false
	}
	return !posLess(r.End, r.Start)
}

// positionInBoundsLocked accepts Position{0,0} on an empty buffer
// (the implicit append point) and any Position whose Line is a
// valid index and whose Col is within [0, len(Runes)] of that line.
func (b *Buffer) positionInBoundsLocked(p Position) bool {
	if p.Line < 0 || p.Col < 0 {
		return false
	}
	if len(b.Lines) == 0 {
		return p.Line == 0 && p.Col == 0
	}
	if p.Line >= len(b.Lines) {
		return false
	}
	return p.Col <= len(b.Lines[p.Line].Runes)
}

// buildReverseLocked computes the inverse Edit using the pre-mutate
// snapshot. Delete/Replace capture the about-to-be-removed text via
// textInRangeLocked; Insert/Replace compute the post-insert end
// Position analytically from e.Text.
func (b *Buffer) buildReverseLocked(e Edit) Edit {
	switch e.Kind {
	case EditKindInsert:
		endPos := advancePos(e.Range.Start, e.Text)
		return Edit{
			Kind:  EditKindDelete,
			Range: Range{Start: e.Range.Start, End: endPos},
		}
	case EditKindDelete:
		deleted := b.textInRangeLocked(e.Range)
		return Edit{
			Kind:  EditKindInsert,
			Range: Range{Start: e.Range.Start, End: e.Range.Start},
			Text:  deleted,
		}
	case EditKindReplace:
		replaced := b.textInRangeLocked(e.Range)
		endPos := advancePos(e.Range.Start, e.Text)
		return Edit{
			Kind:  EditKindReplace,
			Range: Range{Start: e.Range.Start, End: endPos},
			Text:  replaced,
		}
	}
	return Edit{}
}

// mutateLocked applies e to Lines. The caller must have validated
// the edit; mutateLocked itself does not bounds-check.
func (b *Buffer) mutateLocked(e Edit) {
	switch e.Kind {
	case EditKindInsert:
		b.insertAtLocked(e.Range.Start, e.Text)
	case EditKindDelete:
		b.deleteRangeLocked(e.Range)
	case EditKindReplace:
		b.deleteRangeLocked(e.Range)
		b.insertAtLocked(e.Range.Start, e.Text)
	}
}

// insertAtLocked splices text at p. Internal newlines split the
// surrounding line: the first chunk merges with the head of the
// line at p; the last chunk merges with the tail; any middle chunks
// become new whole lines.
func (b *Buffer) insertAtLocked(p Position, text string) {
	if text == "" {
		return
	}
	if len(b.Lines) == 0 {
		b.Lines = []Line{{Runes: []rune{}}}
	}
	chunks := splitTextOnNewline(text)
	cur := b.Lines[p.Line].Runes
	head := append([]rune{}, cur[:p.Col]...)
	tail := append([]rune{}, cur[p.Col:]...)
	if len(chunks) == 1 {
		merged := make([]rune, 0, len(head)+len(chunks[0])+len(tail))
		merged = append(merged, head...)
		merged = append(merged, chunks[0]...)
		merged = append(merged, tail...)
		b.Lines[p.Line] = Line{Runes: merged}
		return
	}
	first := make([]rune, 0, len(head)+len(chunks[0]))
	first = append(first, head...)
	first = append(first, chunks[0]...)
	b.Lines[p.Line] = Line{Runes: first}
	middle := make([]Line, 0, len(chunks)-1)
	for i := 1; i < len(chunks)-1; i++ {
		mid := append([]rune{}, chunks[i]...)
		middle = append(middle, Line{Runes: mid})
	}
	lastChunk := chunks[len(chunks)-1]
	last := make([]rune, 0, len(lastChunk)+len(tail))
	last = append(last, lastChunk...)
	last = append(last, tail...)
	middle = append(middle, Line{Runes: last})
	newLines := make([]Line, 0, len(b.Lines)+len(middle))
	newLines = append(newLines, b.Lines[:p.Line+1]...)
	newLines = append(newLines, middle...)
	newLines = append(newLines, b.Lines[p.Line+1:]...)
	b.Lines = newLines
}

// deleteRangeLocked removes the runes in r. Line-wise ranges remove
// whole lines from Start.Line through End.Line; character-wise
// ranges keep the head of Start.Line and the tail of End.Line and
// join them on Start.Line.
func (b *Buffer) deleteRangeLocked(r Range) {
	if len(b.Lines) == 0 {
		return
	}
	if r.LineWise {
		endLine := r.End.Line
		if endLine >= len(b.Lines) {
			endLine = len(b.Lines) - 1
		}
		b.Lines = append(b.Lines[:r.Start.Line], b.Lines[endLine+1:]...)
		return
	}
	if r.Start.Line == r.End.Line {
		line := b.Lines[r.Start.Line].Runes
		merged := make([]rune, 0, len(line)-(r.End.Col-r.Start.Col))
		merged = append(merged, line[:r.Start.Col]...)
		merged = append(merged, line[r.End.Col:]...)
		b.Lines[r.Start.Line] = Line{Runes: merged}
		return
	}
	head := b.Lines[r.Start.Line].Runes[:r.Start.Col]
	tail := b.Lines[r.End.Line].Runes[r.End.Col:]
	merged := make([]rune, 0, len(head)+len(tail))
	merged = append(merged, head...)
	merged = append(merged, tail...)
	b.Lines[r.Start.Line] = Line{Runes: merged}
	b.Lines = append(b.Lines[:r.Start.Line+1], b.Lines[r.End.Line+1:]...)
}

// splitTextOnNewline splits s on '\n' into rune slices. A trailing
// '\n' yields a final empty chunk so insertAtLocked emits a blank
// trailing line.
func splitTextOnNewline(s string) [][]rune {
	parts := strings.Split(s, "\n")
	out := make([][]rune, len(parts))
	for i, p := range parts {
		out[i] = []rune(p)
	}
	return out
}

// EndOfInsert returns the Position the cursor should occupy after
// inserting text at p — the end of the freshly inserted span. Callers
// that Apply an EditKindInsert at p then SetCursor the result keep the
// cursor following the insertion point (the VimEditor insert path and
// editorBufferAdapter.InsertAtCursor both do this).
func EndOfInsert(p Position, text string) Position {
	return advancePos(p, text)
}

// advancePos returns the Position reached after inserting text at p.
// Internal newlines advance the line counter and reset the column;
// the final chunk's rune length advances the column on the
// destination line.
func advancePos(p Position, text string) Position {
	if text == "" {
		return p
	}
	nl := strings.Count(text, "\n")
	if nl == 0 {
		return Position{Line: p.Line, Col: p.Col + utf8.RuneCountInString(text)}
	}
	last := strings.LastIndex(text, "\n")
	rest := text[last+1:]
	return Position{Line: p.Line + nl, Col: utf8.RuneCountInString(rest)}
}

func utf8ByteLen(runes []rune) int {
	n := 0
	for _, r := range runes {
		n += utf8.RuneLen(r)
	}
	return n
}

// posLess returns true when a is strictly before b in (Line, Col)
// order.
func posLess(a, b Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Col < b.Col
}
