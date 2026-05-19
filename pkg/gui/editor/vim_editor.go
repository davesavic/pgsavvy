package editor

import (
	"unicode"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// BufferProvider is the minimal surface VimEditor needs from the
// QUERY_EDITOR context. *context.QueryEditorContext.Buffer() returns
// *editor.Buffer directly, so the concrete type implicitly satisfies
// BufferProvider. Declared here (not in pkg/gui/context) to avoid an
// import cycle — pkg/gui/context already imports pkg/gui/editor.
type BufferProvider interface {
	Buffer() *Buffer
}

// VimEditor is the gocui.Editor bound to the QUERY_EDITOR view. It
// routes every keystroke through keys.Matcher under its scope and, on
// Insert-mode Passthrough, mutates the *Buffer directly rather than
// delegating to gocui.DefaultEditor (the buffer is the canonical state;
// the view is a mirror — Architecture Decision 2 of epic dbsavvy-wwd).
//
// VimEditor intentionally does NOT carry a *gocui.Gui for ErrQuit
// re-scheduling: the QUERY_EDITOR scope has no :q-style ex-command
// path. masterEditor handles that for COMMAND_LINE.
//
// VimEditor implements Dispatcher (the same shape as masterEditor) so
// testfake.RecorderGuiDriver.FeedChord can drive chord sequences.
type VimEditor struct {
	qec     BufferProvider
	matcher *keys.Matcher
	scope   types.ContextKey
}

// NewVimEditor builds a VimEditor bound to qec / matcher / scope. Any
// of qec / matcher may be nil — Edit nil-checks before dereferencing
// so partially-wired test rigs do not panic.
func NewVimEditor(qec BufferProvider, matcher *keys.Matcher, scope types.ContextKey) *VimEditor {
	return &VimEditor{qec: qec, matcher: matcher, scope: scope}
}

// Edit implements gocui.Editor.
func (e *VimEditor) Edit(v *gocui.View, key gocui.Key) bool {
	if e.matcher == nil {
		return false
	}
	k := keys.KeyFromGocui(key)
	result, _ := e.matcher.Dispatch(e.scope, k)
	return e.applyResult(v, k, result)
}

// Dispatch satisfies the Dispatcher contract used by
// testfake.RecorderGuiDriver.FeedChord; v may be nil.
func (e *VimEditor) Dispatch(v *gocui.View, key gocui.Key) (keys.DispatchResult, error) {
	if e.matcher == nil {
		return keys.FellThrough, nil
	}
	k := keys.KeyFromGocui(key)
	result, err := e.matcher.Dispatch(e.scope, k)
	e.applyResult(v, k, result)
	return result, err
}

// applyResult performs the buffer/view side effects implied by result
// and returns the bool Edit should return.
//
// FellThrough on a printable rune / Backspace / Enter under ModeInsert
// is folded into insertKey: the Matcher returns FellThrough for any
// Special key not in isEditorSafeSpecial (Enter is one such key), but
// the QUERY_EDITOR Insert mode expects Enter to insert a newline.
// Routing FellThrough through insertKey mirrors how masterEditor
// delegates Passthrough to gocui.DefaultEditor for COMMAND_LINE.
func (e *VimEditor) applyResult(v *gocui.View, decoded keys.Key, result keys.DispatchResult) bool {
	switch result {
	case keys.Dispatched, keys.Pending, keys.Swallowed, keys.Cancelled:
		// wwd.10 TODO: register OnInsertPendingFlush for QUERY_EDITOR so
		// the leading rune of an Insert-mode chord prefix isn't dropped
		// when the chord times out. Mirror master_editor.go:45-52 (scope
		// filter + flushRunes). Latent until wwd.10 wires Insert-mode
		// chord bindings (i/a/o) — no QUERY_EDITOR Insert chords exist
		// today, so Pending never fires for this scope.
		return true
	case keys.Passthrough, keys.FellThrough:
		if e.matcher.CurrentMode(e.scope) != types.ModeInsert {
			return false
		}
		return e.insertKey(v, decoded)
	}
	return false
}

// insertKey routes one Insert-mode Passthrough key through the
// *Buffer. Printable bare runes Insert at the cursor; KeyBs deletes
// the rune before the cursor; KeyEnter inserts a newline. Other
// specials are dropped (out of scope for wwd.4 — arrows / Home / End
// land with the motion epic). Returns true when handled.
func (e *VimEditor) insertKey(v *gocui.View, k keys.Key) bool {
	if e.qec == nil {
		return false
	}
	buf := e.qec.Buffer()
	if buf == nil {
		return false
	}
	switch {
	case k.Special == keys.KeyNone && k.Mod == 0 && k.Code != 0 && unicode.IsPrint(k.Code):
		cur := buf.CursorPos()
		if err := buf.Apply(Edit{
			Kind:  EditKindInsert,
			Range: Range{Start: cur, End: cur},
			Text:  string(k.Code),
		}); err != nil {
			return true
		}
		buf.SetCursor(advancePos(cur, string(k.Code)))
	case k.Special == keys.KeyEnter && k.Mod == 0:
		cur := buf.CursorPos()
		if err := buf.Apply(Edit{
			Kind:  EditKindInsert,
			Range: Range{Start: cur, End: cur},
			Text:  "\n",
		}); err != nil {
			return true
		}
		buf.SetCursor(advancePos(cur, "\n"))
	case k.Special == keys.KeyBs && k.Mod == 0:
		cur := buf.CursorPos()
		prev, ok := backspacePos(buf, cur)
		if !ok {
			return true
		}
		if err := buf.Apply(Edit{
			Kind:  EditKindDelete,
			Range: Range{Start: prev, End: cur},
		}); err != nil {
			return true
		}
		buf.SetCursor(prev)
	default:
		return false
	}
	syncViewToBuffer(v, buf)
	return true
}

// backspacePos returns the Position one rune before p (joining onto
// the prior line when p is at start-of-line). Returns ok=false at
// start-of-buffer so Backspace is a silent no-op there.
func backspacePos(b *Buffer, p Position) (Position, bool) {
	if p.Col > 0 {
		return Position{Line: p.Line, Col: p.Col - 1}, true
	}
	if p.Line == 0 {
		return Position{}, false
	}
	return Position{Line: p.Line - 1, Col: b.LineRuneLen(p.Line - 1)}, true
}

// syncViewToBuffer mirrors the canonical *Buffer state into v.
// Architecture Decision 2: Buffer is the source of truth, View is a
// mirror — we rewrite the view content on every buffer-mutating
// Passthrough so external readers (highlighter / statement splitter)
// can keep reading v.Buffer() if they prefer.
//
// SetContent in this lazygit fork clears v.lines + read/write
// positions but does NOT touch v.cx / v.cy (the cursor), so the
// SetCursor restore below is for buffer-cursor tracking, not for
// reversing any clobber by SetContent.
//
// v may be nil — the testfake recorder driver does not own real
// *gocui.View handles and tests use Buffer() directly to assert state.
func syncViewToBuffer(v *gocui.View, buf *Buffer) {
	if v == nil {
		return
	}
	v.SetContent(buf.String())
	cur := buf.CursorPos()
	v.SetCursor(cur.Col, cur.Line)
}

