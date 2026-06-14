package editor

import "github.com/davesavic/dbsavvy/pkg/gui/types"

// visual.go holds the Buffer-level Selection primitives the
// VimEditorController drives once the Matcher flips
// ModeStore[QUERY_EDITOR] into one of the Visual modes
// (ModeVisual / ModeVisualLine / ModeVisualBlock).
//
// The functions here are intentionally narrow: they own Selection
// state on b but DO NOT touch mode. Mode transitions are the
// controller's responsibility (Architecture Decision 1 —
// keys.ModeStore is the sole source of truth for
// per-context mode; Buffer carries no Mode field). Operator
// handlers read Buffer.Selection directly per Architecture
// Decision 4 (Visual+operator bypasses op-pending).

// EnterVisual seeds a Selection anchored at the current Cursor. The
// LineWise/BlockWise flags mirror the requested visual mode so
// TextInRange / Apply downstream see the correct geometry without
// the controller having to re-derive it from the mode bit.
//
// Unknown modes (anything other than ModeVisual / ModeVisualLine /
// ModeVisualBlock) are a no-op so a stray dispatch can't corrupt
// Selection state.
func EnterVisual(b *Buffer, mode types.Mode) {
	if b == nil {
		return
	}
	switch mode {
	case types.ModeVisual, types.ModeVisualLine, types.ModeVisualBlock:
	default:
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	anchor := b.Cursor
	b.Selection = &Range{
		Start:     anchor,
		End:       anchor,
		LineWise:  mode == types.ModeVisualLine,
		BlockWise: mode == types.ModeVisualBlock,
	}
}

// ExitVisual clears any live Selection on b under b.mu. Safe to call
// on a nil buffer or when no Selection is active (idempotent).
// QueryEditorContext.HandleFocusLost calls this so Selection never
// persists across a focus change (and therefore never lands on disk
// on disk.
func ExitVisual(b *Buffer) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Selection = nil
}

// ExtendSelection moves the live Selection.End to to and snaps Cursor
// to the same position. The anchor (Selection.Start) is preserved so
// repeated extension keeps the original entry point of Visual mode.
//
// No-op when Selection is nil (caller hasn't entered Visual yet).
// Vim allows Selection.End to be < Selection.Start in lex order
// (backwards visual selection); ExtendSelection does NOT normalise —
// operator handlers normalise at consume time.
func ExtendSelection(b *Buffer, to Position) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Selection == nil {
		return
	}
	b.Selection.End = to
	b.Cursor = to
}

// SetSelection installs r as the live Selection on b under b.mu. Used
// by text-object handlers in Visual mode to snap the Selection to a
// resolved range (e.g. `vi"` expands the visual range to cover the
// inner-quote text). A nil r clears Selection — equivalent to
// ExitVisual but without touching mode.
func SetSelection(b *Buffer, r *Range) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if r == nil {
		b.Selection = nil
		return
	}
	cp := *r
	b.Selection = &cp
}
