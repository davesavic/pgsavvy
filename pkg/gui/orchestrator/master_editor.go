package orchestrator

import (
	"sync"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

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
// g may be nil — when nil the flush callback writes directly into the
// captured *gocui.View without going through gocui.Update. Production
// wiring passes the real *gocui.Gui so flushes are scheduled onto the
// MainLoop.
func NewMasterEditor(g *gocui.Gui, matcher *keys.Matcher, scope types.ContextKey) gocui.Editor {
	e := &masterEditor{
		gui:     g,
		matcher: matcher,
		scope:   scope,
	}
	if matcher != nil {
		matcher.OnInsertPendingFlush(func(s types.ContextKey, runes []rune) {
			if s != scope {
				return
			}
			e.flushRunes(runes)
		})
	}
	return e
}

// masterEditor is the concrete gocui.Editor that bridges gocui's
// per-view editor mechanism and keys.Matcher chord dispatch.
type masterEditor struct {
	gui     *gocui.Gui
	matcher *keys.Matcher
	scope   types.ContextKey

	mu           sync.Mutex
	view         *gocui.View
	pendingRunes []rune
}

// Edit implements gocui.Editor. The boolean return follows gocui's
// convention: true = "handled, do not propagate"; false = "not handled,
// let gocui fall through".
func (e *masterEditor) Edit(v *gocui.View, key gocui.Key) bool {
	e.captureView(v)
	if e.matcher == nil {
		return false
	}
	k := keys.KeyFromGocui(key)
	result, _ := e.matcher.Dispatch(e.scope, k)
	return e.applyResult(v, key, k, result)
}

// Dispatch satisfies Dispatcher; the testfake recorder uses it to drive
// chord sequences without owning a *gocui.View / *gocui.Gui. v may be
// nil — applyResult tolerates that by skipping the DefaultEditor
// delegation and the pending-buffer flush write.
func (e *masterEditor) Dispatch(v *gocui.View, key gocui.Key) (keys.DispatchResult, error) {
	e.captureView(v)
	if e.matcher == nil {
		return keys.FellThrough, nil
	}
	k := keys.KeyFromGocui(key)
	result, err := e.matcher.Dispatch(e.scope, k)
	e.applyResult(v, key, k, result)
	return result, err
}

// captureView records the first non-nil *gocui.View it sees so the
// flush callback (which runs on the Matcher's timer goroutine) can
// write into the right view.
func (e *masterEditor) captureView(v *gocui.View) {
	if v == nil {
		return
	}
	e.mu.Lock()
	if e.view == nil {
		e.view = v
	}
	e.mu.Unlock()
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
			return gocui.DefaultEditor.Edit(v, raw)
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
// the MainLoop via gocui.Update when a *gocui.Gui is available, or
// performs the write inline when it is not (testfake path).
func (e *masterEditor) flushRunes(runes []rune) {
	if len(runes) == 0 {
		return
	}
	e.clearPending()
	e.mu.Lock()
	v := e.view
	e.mu.Unlock()
	if e.gui != nil {
		e.gui.Update(func(*gocui.Gui) error {
			if v == nil || v.TextArea == nil {
				return nil
			}
			for _, r := range runes {
				v.TextArea.TypeCharacter(string(r))
			}
			v.RenderTextArea()
			return nil
		})
		return
	}
	if v == nil || v.TextArea == nil {
		return
	}
	for _, r := range runes {
		v.TextArea.TypeCharacter(string(r))
	}
	v.RenderTextArea()
}
