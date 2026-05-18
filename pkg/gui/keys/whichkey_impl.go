package keys

import (
	"sync"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// WhichKey is the concrete WhichKeyNotifier the Matcher drives from
// the PARTIAL state. It schedules a visibility flip after the
// configured delay, exposes a Snapshot for the renderer
// (WhichKeyContext), and cancels cleanly on Hide / re-arm.
//
// Lifecycle:
//
//  1. Matcher.notifyWhichKeyLocked calls ShowAfter(delay, scope, prefix)
//     when a partial chord is pending. ShowAfter increments seq, cancels
//     any prior schedule, and starts a time.AfterFunc.
//  2. The closure fires under the mutex and checks `state.id == seq`
//     before flipping `visible` true. A second ShowAfter or a Hide
//     between schedule and fire bumps seq → the stale closure does
//     nothing.
//  3. Hide() resets state and stops the timer. Idempotent. Per the
//     WhichKeyNotifier contract, Hide MUST NOT re-enter the Matcher.
//
// Concurrency: every public method takes mu. The AfterFunc closure
// re-takes mu — never holds the Matcher mutex.
type WhichKey struct {
	mu    sync.Mutex
	seq   uint64
	state *whichKeyState
}

type whichKeyState struct {
	id      uint64
	scope   types.ContextKey
	prefix  []Key
	timer   *time.Timer
	visible bool
}

// NewWhichKey constructs a WhichKey with no pending schedule.
func NewWhichKey() *WhichKey {
	return &WhichKey{}
}

// Compile-time check: *WhichKey satisfies both the Matcher-facing
// notifier and the renderer-facing snapshot interface.
var (
	_ WhichKeyNotifier    = (*WhichKey)(nil)
	_ types.WhichKeyState = (*WhichKey)(nil)
)

// ShowAfter schedules the popup to become visible after delay. A
// non-positive delay flips visibility immediately. Any prior schedule
// is cancelled before the new one is recorded (seq bump invalidates
// the prior closure).
//
// prefix is COPIED defensively — the Matcher passes a slice it may
// reuse after the call returns.
func (w *WhichKey) ShowAfter(delay time.Duration, scope types.ContextKey, prefix []Key) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.cancelLocked()

	w.seq++
	id := w.seq
	cp := append([]Key(nil), prefix...)
	st := &whichKeyState{id: id, scope: scope, prefix: cp}
	if delay <= 0 {
		st.visible = true
		w.state = st
		return
	}
	st.timer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		if w.state != nil && w.state.id == id {
			w.state.visible = true
		}
		w.mu.Unlock()
	})
	w.state = st
}

// Hide drops any pending schedule and any visible popup. Idempotent;
// safe to call without a prior ShowAfter. Does NOT call back into the
// Matcher (per the WhichKeyNotifier interface contract).
func (w *WhichKey) Hide() {
	w.mu.Lock()
	w.cancelLocked()
	w.mu.Unlock()
}

// Visible reports whether the popup is currently shown. False when no
// schedule is active, or when a schedule is pending but the timer has
// not yet fired.
func (w *WhichKey) Visible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state != nil && w.state.visible
}

// Snapshot returns a fresh copy of (scope, prefix, visible) under the
// mutex. The returned prefix is a fresh slice — callers may mutate it
// without affecting internal state. With no active state, returns
// ("", nil, false).
func (w *WhichKey) Snapshot() (types.ContextKey, []Key, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == nil {
		return "", nil, false
	}
	cp := append([]Key(nil), w.state.prefix...)
	return w.state.scope, cp, w.state.visible
}

// seqForTest exposes the monotonic counter for AC tests that need to
// confirm stale closures bump out cleanly. Production code does not
// read seq directly.
func (w *WhichKey) seqForTest() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// cancelLocked clears any pending schedule and visibility. Bumps seq so
// the in-flight AfterFunc closure (if any) becomes a no-op when it
// finally acquires the mutex. Caller must hold w.mu.
func (w *WhichKey) cancelLocked() {
	if w.state == nil {
		w.seq++
		return
	}
	if w.state.timer != nil {
		w.state.timer.Stop()
	}
	w.state = nil
	w.seq++
}
