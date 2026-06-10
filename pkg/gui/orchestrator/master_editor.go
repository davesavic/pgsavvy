package orchestrator

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
)

// sensitiveScopes is the set of context keys whose keysym must be
// redacted before being emitted as a cat=input event (AD-21). Defensive
// across all modes — anything pushed into a sensitive scope is treated
// as potentially carrying credential text. Currently only the
// credentials prompt; expand here when new sensitive contexts ship.
var sensitiveScopes = map[types.ContextKey]struct{}{
	// "credentials_prompt" is a forward-declared sentinel for the future
	// credentials prompt context (not yet defined as a types.ContextKey
	// constant). The literal must match the eventual constant value.
	types.ContextKey("credentials_prompt"): {},
}

// MasterEditorOption mutates a *masterEditor before NewMasterEditor
// returns. Used to inject optional collaborators (session logger)
// without changing the constructor signature for existing call sites.
type MasterEditorOption func(*masterEditor)

// WithSessionLog injects the per-session logger the master editor uses
// to emit cat=input key / dispatch_result events. nil disables emission.
func WithSessionLog(l *slog.Logger) MasterEditorOption {
	return func(e *masterEditor) {
		e.sessionLog = l
	}
}

// WithOnPassthroughEdit registers an onChange seam invoked SYNCHRONOUSLY
// on the MainLoop after gocui.DefaultEditor.Edit applies a Passthrough
// keystroke, with the post-edit (non-destructive) TextArea content. The
// hook fires ONLY for the SEARCH_LINE scope (decision AD-3); attaching
// it to any other scope's editor is a no-op. gocui runs Editor.Edit
// synchronously inside processEvent on the single MainLoop goroutine, so
// the post-edit read is race-free. The hook MUST NOT re-enter the
// dispatcher, push/pop the focus stack, or trigger a render/snapshot
// (AD-4); deferred work belongs on gui.Update. nil disables the seam.
func WithOnPassthroughEdit(fn func(content string)) MasterEditorOption {
	return func(e *masterEditor) {
		e.onPassthroughEdit = fn
	}
}

// WithEmergencyQuit injects the un-rebindable Ctrl-C escape hatch (R5).
// When set, masterEditor.Edit / Dispatch intercept Ctrl-C BEFORE matcher
// dispatch and route it straight to the clean-quit path, so editable views
// (CELL_EDITOR, SEARCH_LINE, …) that capture every key via this editor
// still always quit on Ctrl-C regardless of user keybinding config. nil
// disables the intercept (Ctrl-C falls through to normal trie dispatch).
func WithEmergencyQuit(fn func() error) MasterEditorOption {
	return func(e *masterEditor) {
		e.emergencyQuit = fn
	}
}

// Dispatcher is the side-channel a master Editor exposes so test
// harnesses (testfake.RecorderGuiDriver.FeedChord) can drive a chord
// sequence through the editor and observe the raw keys.DispatchResult
// the gocui.Editor.Edit boolean return cannot carry.
//
// The production path (real *gocui.View) does NOT use this interface;
// gocui's runtime calls Edit directly.
type Dispatcher interface {
	Dispatch(v *gocui.View, key gocui.Key) (keys.DispatchResult, error)
}

// NewMasterEditor builds a gocui.Editor that routes every keystroke on
// its host view through matcher under the supplied scope. Insert-mode
// partial sequences that time out without resolving to a leaf are
// flushed to the view's TextArea via the Matcher's
// OnInsertPendingFlush callback (D16).
//
// g may be nil (testfake path) — when nil the timer-driven flush is a
// no-op (the test path drives flushPendingSync directly with the
// per-call *gocui.View). Production wiring passes the real *gocui.Gui
// so flushes are scheduled onto the MainLoop, and the editor's
// viewName is used to look up the live view at flush time (no cached
// pointer; re-pushes create a fresh view and a cached pointer would
// dangle).
func NewMasterEditor(g *gocui.Gui, matcher *keys.Matcher, scope types.ContextKey, opts ...MasterEditorOption) gocui.Editor {
	e := &masterEditor{
		gui:      g,
		matcher:  matcher,
		scope:    scope,
		viewName: string(scope),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	if matcher != nil {
		matcher.OnInsertPendingFlush(scope, func(_ types.ContextKey, runes []rune) {
			e.flushRunes(runes)
		})
	}
	return e
}

// masterEditor is the concrete gocui.Editor that bridges gocui's
// per-view editor mechanism and keys.Matcher chord dispatch.
type masterEditor struct {
	gui        *gocui.Gui
	matcher    *keys.Matcher
	scope      types.ContextKey
	viewName   string
	sessionLog *slog.Logger
	// onPassthroughEdit is the SEARCH_LINE onChange seam (AD-3). Fired
	// synchronously after a Passthrough keystroke writes into v.TextArea,
	// with the post-edit content. Scope-gated: only SEARCH_LINE invokes it.
	onPassthroughEdit func(content string)
	// emergencyQuit is the un-rebindable Ctrl-C escape hatch (R5). Invoked
	// before matcher dispatch when the decoded key is Ctrl-C; nil disables.
	emergencyQuit func() error

	mu           sync.Mutex
	pendingRunes []rune
}

// Edit implements gocui.Editor. The boolean return follows gocui's
// convention: true = "handled, do not propagate"; false = "not handled,
// let gocui fall through".
func (e *masterEditor) Edit(v *gocui.View, key gocui.Key) bool {
	if e.matcher == nil {
		return false
	}
	k := keys.KeyFromGocui(key)
	// R5: un-rebindable emergency Ctrl-C exit. Intercept before matcher
	// dispatch so editable views always quit on Ctrl-C regardless of config.
	if isEmergencyQuitKey(k) && e.emergencyQuit != nil {
		if errors.Is(e.emergencyQuit(), gocui.ErrQuit) && e.gui != nil {
			e.gui.Update(func(*gocui.Gui) error { return gocui.ErrQuit })
		}
		return true
	}
	start := time.Now()
	result, err := e.matcher.Dispatch(e.scope, k)
	// gocui.Editor.Edit returns only bool, so a Dispatched handler
	// returning gocui.ErrQuit (e.g. the :q ex-command) would otherwise
	// be silently dropped. Re-schedule the quit on the MainLoop via
	// Gui.Update so the gocui run loop unwinds.
	if errors.Is(err, gocui.ErrQuit) && e.gui != nil {
		e.gui.Update(func(*gocui.Gui) error { return gocui.ErrQuit })
	}
	out := e.applyResult(v, key, k, result)
	e.emitInputEvents(k, result, time.Since(start))
	return out
}

// Dispatch satisfies Dispatcher; the testfake recorder uses it to drive
// chord sequences without owning a *gocui.View / *gocui.Gui. v may be
// nil — applyResult tolerates that by skipping the DefaultEditor
// delegation and the pending-buffer flush write.
func (e *masterEditor) Dispatch(v *gocui.View, key gocui.Key) (keys.DispatchResult, error) {
	if e.matcher == nil {
		return keys.FellThrough, nil
	}
	k := keys.KeyFromGocui(key)
	// R5: emergency Ctrl-C — same intercept as Edit, surfaced as a
	// Dispatched result carrying the quit error so the recorder harness
	// (FeedChord) observes gocui.ErrQuit at the dispatch layer.
	if isEmergencyQuitKey(k) && e.emergencyQuit != nil {
		return keys.Dispatched, e.emergencyQuit()
	}
	start := time.Now()
	result, err := e.matcher.Dispatch(e.scope, k)
	e.applyResult(v, key, k, result)
	e.emitInputEvents(k, result, time.Since(start))
	return result, err
}

// isEmergencyQuitKey reports whether k is the Ctrl-C chord. Under tcell
// raw mode ISIG is cleared, so keyboard Ctrl-C arrives as a KeyCtrlC KEY
// event decoded by KeyFromGocui into Key{Code:'c', Mod:ModCtrl} (R5).
func isEmergencyQuitKey(k keys.Key) bool {
	return k == ctrlCKey
}

// emitInputEvents emits the cat=input key + dispatch_result pair for a
// single keystroke. Centralized so both Edit (gocui runtime) and
// Dispatch (testfake recorder) cover the same event surface (AD-20).
// When the active scope is in sensitiveScopes the keysym is replaced
// with "<redacted>" — mode + scope remain logged (AD-21).
func (e *masterEditor) emitInputEvents(decoded keys.Key, result keys.DispatchResult, elapsed time.Duration) {
	log := e.sessionLog
	if log == nil {
		return
	}
	mode := types.ModeNormal
	if e.matcher != nil {
		mode = e.matcher.CurrentMode(e.scope)
	}
	keyLabel := decoded.String()
	if _, sensitive := sensitiveScopes[e.scope]; sensitive {
		keyLabel = "<redacted>"
	}
	logs.Event(log, "input", "key",
		slog.String("key", keyLabel),
		slog.String("scope", string(e.scope)),
		slog.String("mode", mode.String()),
	)
	logs.Event(log, "input", "dispatch_result",
		slog.String("result", dispatchResultLabel(result)),
		slog.String("scope", string(e.scope)),
		slog.String("mode", mode.String()),
		slog.Int64("ms", elapsed.Microseconds()),
	)
}

// dispatchResultLabel renders the canonical PascalCase label for r as
// required by AC ("Dispatched|Pending|FellThrough|Cancelled|
// Passthrough|Swallowed"). Falls back to keys.DispatchResult.String
// for unknown values so a new variant never blows up logging.
func dispatchResultLabel(r keys.DispatchResult) string {
	switch r {
	case keys.Dispatched:
		return "Dispatched"
	case keys.Pending:
		return "Pending"
	case keys.FellThrough:
		return "FellThrough"
	case keys.Cancelled:
		return "Cancelled"
	case keys.Passthrough:
		return "Passthrough"
	case keys.Swallowed:
		return "Swallowed"
	default:
		return r.String()
	}
}

// applyResult performs the side effects implied by result and returns
// the bool Edit should return. v may be nil (testfake path).
func (e *masterEditor) applyResult(v *gocui.View, raw gocui.Key, decoded keys.Key, result keys.DispatchResult) bool {
	switch result {
	case keys.Dispatched:
		e.clearPending()
		return true
	case keys.Pending:
		if e.matcher.CurrentMode(e.scope) == types.ModeInsert {
			e.appendPendingRune(decoded)
		}
		return true
	case keys.Passthrough:
		mode := e.matcher.CurrentMode(e.scope)
		if mode == types.ModeInsert || mode == types.ModeCommand {
			if v == nil {
				return false
			}
			out := gocui.DefaultEditor.Edit(v, raw)
			e.fireOnPassthroughEdit(v)
			return out
		}
		return false
	case keys.FellThrough:
		return false
	case keys.Cancelled:
		e.flushPendingSync(v)
		return true
	case keys.Swallowed:
		return true
	}
	return false
}

// fireOnPassthroughEdit invokes the SEARCH_LINE onChange seam with the
// post-edit (non-destructive) TextArea content. Scope-gated to
// SEARCH_LINE (AD-3) so PROMPT / QUERY_EDITOR / COMMAND_LINE never fire
// it. Called synchronously on the MainLoop after DefaultEditor.Edit;
// the read of v.TextArea is race-free for that reason.
func (e *masterEditor) fireOnPassthroughEdit(v *gocui.View) {
	if e.onPassthroughEdit == nil || e.scope != types.SEARCH_LINE {
		return
	}
	if v == nil || v.TextArea == nil {
		return
	}
	e.onPassthroughEdit(v.TextArea.GetContent())
}

// appendPendingRune buffers decoded's rune (if any) on the pending
// slice. Called only for ModeInsert Pending results.
func (e *masterEditor) appendPendingRune(decoded keys.Key) {
	if decoded.Special != keys.KeyNone || decoded.Mod != 0 || decoded.Code == 0 {
		return
	}
	e.mu.Lock()
	e.pendingRunes = append(e.pendingRunes, decoded.Code)
	e.mu.Unlock()
}

// clearPending drops every buffered rune. Safe to call when idle.
func (e *masterEditor) clearPending() {
	e.mu.Lock()
	e.pendingRunes = nil
	e.mu.Unlock()
}

// flushPendingSync writes every buffered rune to v.TextArea, then
// clears the buffer. Called on the Cancelled dispatch path (the
// Matcher has already dropped its pending state). v may be nil — in
// that case the buffer is still drained so subsequent Edits start
// clean.
func (e *masterEditor) flushPendingSync(v *gocui.View) {
	e.mu.Lock()
	runes := e.pendingRunes
	e.pendingRunes = nil
	e.mu.Unlock()
	if v == nil || v.TextArea == nil {
		return
	}
	for _, r := range runes {
		v.TextArea.TypeCharacter(string(r))
	}
	v.RenderTextArea()
}

// flushRunes is invoked by the Matcher's timer goroutine when a
// ModeInsert partial sequence times out. It schedules the write onto
// the MainLoop via gocui.Update; inside the closure the live view is
// looked up via gui.View so the write hits the current view-instance
// (re-pushes DeleteView + recreate, so any cached pointer would
// dangle). When gui is nil (testfake path) the call is a no-op — the
// test path drives flushPendingSync directly with the per-call view.
func (e *masterEditor) flushRunes(runes []rune) {
	if len(runes) == 0 {
		return
	}
	e.clearPending()
	if e.gui == nil {
		return
	}
	name := e.viewName
	e.gui.Update(func(g *gocui.Gui) error {
		v, err := g.View(name)
		if err != nil || v == nil || v.TextArea == nil {
			return nil
		}
		for _, r := range runes {
			v.TextArea.TypeCharacter(string(r))
		}
		v.RenderTextArea()
		return nil
	})
}
