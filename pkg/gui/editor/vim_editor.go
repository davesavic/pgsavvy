package editor

import (
	"unicode"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/editor/highlight"
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

	// autoCompleter is an optional callback invoked after every
	// printable insert-mode keystroke (Buffer.Apply already ran; the
	// buffer cursor reflects the new position). The callback is
	// responsible for the config gate + popup-already-visible guard +
	// AutoTriggerFromContext check + engine Trigger + popup Show.
	// VimEditor only owns the seam; the closure is wired post-
	// construction by Z1 (dbsavvy-bwq.22 / .23). Nil = no auto-trigger.
	autoCompleter func(buf *Buffer, pos Position)
}

// NewVimEditor builds a VimEditor bound to qec / matcher / scope. Any
// of qec / matcher may be nil — Edit nil-checks before dereferencing
// so partially-wired test rigs do not panic.
//
// When matcher is non-nil, the constructor registers an
// OnInsertPendingFlush callback (scope-filtered) so that a ModeInsert
// chord prefix timing out without resolving to a leaf flushes its
// buffered printable runes into the canonical *Buffer (rather than
// silently dropping them). This mirrors the master_editor.go pattern
// for COMMAND_LINE; here the flush target is Buffer.Apply instead of
// TextArea.TypeCharacter because Buffer is the source of truth for
// QUERY_EDITOR. The view re-syncs on the next user keystroke via the
// existing syncViewToBuffer path.
//
// Caveat: keys.Matcher holds a single InsertPendingFlush callback
// globally — every scope's editor constructor races to register one,
// and the last writer wins. In MVP this is benign: VimEditor has no
// multi-key Insert-mode bindings today (`<esc>` is a single-key leaf),
// so Pending never fires for QUERY_EDITOR. The registration here is
// preemptive; a multi-callback registry can wait for an actual
// consumer.
func NewVimEditor(qec BufferProvider, matcher *keys.Matcher, scope types.ContextKey) *VimEditor {
	e := &VimEditor{qec: qec, matcher: matcher, scope: scope}
	if matcher != nil {
		matcher.OnInsertPendingFlush(func(s types.ContextKey, runes []rune) {
			if s != scope {
				return
			}
			e.flushPendingRunes(runes)
		})
	}
	return e
}

// flushPendingRunes applies buffered Pending-state runes to the
// *Buffer as a single Insert at the live Cursor, then advances the
// Cursor past the inserted text. Called from the Matcher's timer
// goroutine; Buffer.Apply is goroutine-safe via the Buffer mutex. The
// view re-syncs on the next user keystroke (syncViewToBuffer runs at
// the end of every insertKey / motion handler).
func (e *VimEditor) flushPendingRunes(runes []rune) {
	if len(runes) == 0 || e.qec == nil {
		return
	}
	buf := e.qec.Buffer()
	if buf == nil {
		return
	}
	text := string(runes)
	cur := buf.CursorPos()
	if err := buf.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: cur, End: cur},
		Text:  text,
	}); err != nil {
		return
	}
	buf.SetCursor(advancePos(cur, text))
}

// SetAutoCompleter wires the optional callback VimEditor invokes after
// every printable insert-mode keystroke. The callback receives the
// buffer + post-insert cursor position; it owns the config gate,
// popup-visibility check, AutoTriggerFromContext check, and the actual
// engine Trigger / popup Show. Passing nil clears any previously-
// installed callback. dbsavvy-bwq.22 (C5).
func (e *VimEditor) SetAutoCompleter(fn func(buf *Buffer, pos Position)) {
	e.autoCompleter = fn
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
		// OnInsertPendingFlush is registered in NewVimEditor — if a
		// ModeInsert chord prefix times out without resolving, the
		// callback flushes the buffered printable runes into the
		// canonical *Buffer. No flush logic lives here in applyResult.
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
		// Auto-trigger completion (dbsavvy-bwq.22 / C5). The callback
		// is responsible for the config gate, popup-visible guard, and
		// AutoTriggerFromContext check. Only fired on printable runes —
		// Enter / Backspace do not auto-trigger.
		if e.autoCompleter != nil {
			e.autoCompleter(buf, buf.CursorPos())
		}
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
	content := highlight.Highlight(buf.String())
	if sel := buf.SelectionSnapshot(); sel != nil {
		content = ApplySelectionOverlay(content, *sel)
	}
	v.SetContent(content)
	cur := buf.CursorPos()
	v.SetCursor(cur.Col, cur.Line)
}
