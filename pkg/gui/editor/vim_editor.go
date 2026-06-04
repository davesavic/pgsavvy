package editor

import (
	"errors"
	"log/slog"
	"unicode"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
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
// VimEditor carries an optional types.GuiDriver so that a chord
// resolving to gocui.ErrQuit — e.g. the GLOBAL-scope `<leader>q` that
// falls through from the focused QUERY_EDITOR — is rescheduled onto the
// main loop. gocui.Editor.Edit can only return a bool, so without this
// the quit would be silently dropped (dbsavvy-dg5). This mirrors
// masterEditor.Edit; the GuiDriver seam (not a raw *gocui.Gui) keeps the
// reschedule observable to the recorder driver in tests, matching the
// orchestrator's own driver.Update(ErrQuit) quit path.
//
// VimEditor implements Dispatcher (the same shape as masterEditor) so
// testfake.RecorderGuiDriver.FeedChord can drive chord sequences.
type VimEditor struct {
	qec     BufferProvider
	matcher *keys.Matcher
	scope   types.ContextKey
	driver  types.GuiDriver

	// autoCompleter is an optional callback invoked after every
	// printable insert-mode keystroke (Buffer.Apply already ran; the
	// buffer cursor reflects the new position). The callback is
	// responsible for the config gate + popup-already-visible guard +
	// AutoTriggerFromContext check + engine Trigger + popup Show.
	// VimEditor only owns the seam; the closure is wired post-
	// construction by Z1 (dbsavvy-bwq.22 / .23). Nil = no auto-trigger.
	autoCompleter func(buf *Buffer, pos Position)

	// completionKey is the optional popup-navigation seam consulted at
	// the top of the insert path for keys that change meaning while the
	// completion popup is visible (Tab = next selection, Enter = accept).
	// The controller owns the SuggestionsContext + accept-replace logic;
	// VimEditor only forwards the decoded key. Returns true when the
	// popup consumed the key (popup was visible), in which case insertKey
	// re-syncs the view and skips its normal handling. Returns false to
	// fall through to the key's normal Insert meaning (newline / drop).
	// Nil = no popup; keys keep their default Insert behaviour. (etp.1)
	completionKey func(k keys.Key) bool

	// sessionLog is the optional per-session logger. When non-nil,
	// insert-mode Buffer.Apply failures are emitted via logs.Event
	// instead of being swallowed silently. Nil = no logging (the seam
	// no-ops, matching the masterEditor convention). Wired via
	// WithSessionLog.
	sessionLog *slog.Logger
}

// VimEditorOption mutates a *VimEditor before NewVimEditor returns,
// mirroring the orchestrator's MasterEditorOption pattern.
type VimEditorOption func(*VimEditor)

// WithSessionLog injects the per-session logger VimEditor uses to report
// insert-mode Buffer.Apply failures. Without it, those failures are
// swallowed silently (the historical behavior that hid the
// stuck-in-insert bug).
func WithSessionLog(l *slog.Logger) VimEditorOption {
	return func(e *VimEditor) { e.sessionLog = l }
}

// WithGuiDriver injects the GuiDriver VimEditor.Edit uses to reschedule a
// chord that resolved to gocui.ErrQuit (e.g. GLOBAL `<leader>q` reached
// from the focused query editor) onto the main loop. Without it the quit
// is dropped, since gocui.Editor.Edit returns only a bool. nil disables
// the reschedule (the testfake-with-nil-driver path).
func WithGuiDriver(d types.GuiDriver) VimEditorOption {
	return func(e *VimEditor) { e.driver = d }
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
// keys.Matcher keys flush callbacks by scope, so this QUERY_EDITOR
// registration coexists with the master Editor registrations for other
// scopes. The `jk` Insert-mode chord is the first multi-key Insert
// binding, so the Pending path now fires for QUERY_EDITOR: a lone `j`
// flushes here on the chord timeout, and a `j` broken by a non-`k` key
// flushes synchronously from Matcher.Dispatch before the breaking key
// is inserted.
func NewVimEditor(qec BufferProvider, matcher *keys.Matcher, scope types.ContextKey, opts ...VimEditorOption) *VimEditor {
	e := &VimEditor{qec: qec, matcher: matcher, scope: scope}
	for _, opt := range opts {
		opt(e)
	}
	if matcher != nil {
		matcher.OnInsertPendingFlush(scope, func(_ types.ContextKey, runes []rune) {
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
		e.logApplyErr("flush_pending", cur, err)
		return
	}
	buf.SetCursor(advancePos(cur, text))
}

// logApplyErr emits a structured record when an insert-mode Buffer.Apply
// fails. Without it these failures are invisible: typing does nothing
// while the mode indicator still reads INSERT (the stuck-in-insert bug).
// kind names the originating path; cur is the cursor the failed Apply
// targeted. No-ops when sessionLog is nil.
func (e *VimEditor) logApplyErr(kind string, cur Position, err error) {
	logs.Event(e.sessionLog, "input", "insert_apply_err",
		slog.String("scope", string(e.scope)),
		slog.String("kind", kind),
		slog.Int("line", cur.Line),
		slog.Int("col", cur.Col),
		slog.String("err", err.Error()),
	)
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

// HasAutoCompleter reports whether an auto-completer callback is wired.
// Test accessor — dbsavvy-etp.4 asserts boot wiring is gated by
// editor.autocomplete (installed when true, absent when false).
func (e *VimEditor) HasAutoCompleter() bool { return e.autoCompleter != nil }

// SetCompletionKey wires the popup-navigation seam invoked at the top
// of insertKey for Tab / Enter while the completion popup is visible.
// The callback returns true when it consumed the key (popup visible);
// nil clears it. dbsavvy-etp.1.
func (e *VimEditor) SetCompletionKey(fn func(k keys.Key) bool) {
	e.completionKey = fn
}

// Edit implements gocui.Editor.
func (e *VimEditor) Edit(v *gocui.View, key gocui.Key) bool {
	if e.matcher == nil {
		return false
	}
	k := keys.KeyFromGocui(key)
	result, err := e.matcher.Dispatch(e.scope, k)
	// gocui.Editor.Edit returns only a bool, so a handler returning
	// gocui.ErrQuit (the GLOBAL `<leader>q` that falls through from this
	// focused editor) would otherwise be silently dropped. Reschedule the
	// quit on the main loop via the GuiDriver so the run loop unwinds
	// (dbsavvy-dg5; mirrors masterEditor.Edit).
	if errors.Is(err, gocui.ErrQuit) && e.driver != nil {
		e.driver.Update(func() error { return gocui.ErrQuit })
	}
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
	// Popup-navigation seam (etp.1): while the completion popup is
	// visible, Tab advances the selection and Enter accepts it. The
	// callback returns true only when the popup consumed the key; we
	// re-sync the view (Accept mutated the buffer) and stop. When it
	// returns false the key keeps its normal Insert meaning below.
	if e.completionKey != nil && isCompletionNavKey(k) && e.completionKey(k) {
		syncViewToBuffer(v, buf)
		return true
	}
	switch {
	case k.Special == keys.KeyNone && k.Mod == 0 && k.Code != 0 && unicode.IsPrint(k.Code):
		cur := buf.CursorPos()
		if err := buf.Apply(Edit{
			Kind:  EditKindInsert,
			Range: Range{Start: cur, End: cur},
			Text:  string(k.Code),
		}); err != nil {
			e.logApplyErr("insert_rune", cur, err)
			return true
		}
		buf.SetCursor(advancePos(cur, string(k.Code)))
		// Auto-trigger completion (dbsavvy-bwq.22 / C5). The callback
		// is responsible for the config gate, popup-visible guard, and
		// AutoTriggerFromContext check. Fired on printable runes and (for
		// in-place refilter) on Backspace; Enter never auto-triggers.
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
			e.logApplyErr("insert_newline", cur, err)
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
			e.logApplyErr("backspace", prev, err)
			return true
		}
		buf.SetCursor(prev)
		// Backspace refilter (dbsavvy-etp.4). Fire the callback so a
		// backspace WITHIN the partial identifier re-narrows the popup.
		// The callback (controller.AutoTrigger) only acts while the popup
		// is already visible (or at an AutoTriggerFromContext position),
		// so deleting outside an active completion does NOT pop it open.
		if e.autoCompleter != nil {
			e.autoCompleter(buf, buf.CursorPos())
		}
	default:
		return false
	}
	syncViewToBuffer(v, buf)
	return true
}

// isCompletionNavKey reports whether k is a key the completion popup
// overrides while visible: Tab (next) or Enter (accept). etp.1.
func isCompletionNavKey(k keys.Key) bool {
	if k.Mod != 0 {
		return false
	}
	return k.Special == keys.KeyTab || k.Special == keys.KeyBacktab || k.Special == keys.KeyEnter
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
	if flash := buf.YankFlashSnapshot(); flash != nil {
		content = ApplyYankFlashOverlay(content, *flash)
	}
	v.SetContent(content)
	cur := buf.CursorPos()
	v.SetCursor(cur.Col, cur.Line)
}
