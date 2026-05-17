package keys

import (
	"sync"
	"time"
)

// DefaultArmTTL is the auto-cancel window applied when no caller-provided
// TTL is in effect. Per dbsavvy-zro AC; matches the lazygit "leader hint"
// feel.
const DefaultArmTTL = 1500 * time.Millisecond

// Handler is the suffix callback invoked when Dispatch matches a suffix
// rune against an active arm. The Handler error is returned verbatim by
// Dispatch.
type Handler = func() error

// OneshotArm is the shared one-shot prefix dispatcher used for the
// leader (`<space>`) and colon (`:`) chord paths.
//
// Lifecycle:
//
//  1. A bare prefix keypress invokes Arm(prefix, suffixes, scope). Arm
//     records the suffix map under the helper's mutex, starts a TTL
//     timer that auto-cancels the arm, and (at most one arm is ever
//     live — a second Arm cancels the first before recording the new
//     one).
//  2. The next "suffix" keypress is fed to Dispatch(suffix). If the
//     suffix rune matches an entry in the active arm's map, the handler
//     fires and the arm is cleared. If it does not match, the arm is
//     cleared silently (per AC "unknown suffix cancels silently").
//  3. Cross-cutting cancellations: Cancel() drops the current arm
//     without firing any handler. The bootstrap registers Cancel as a
//     gui.ContextTree swap hook so context-switch wipes the arm, and as
//     a mouse-event side-effect via OnMouseEvent.
//
// OneshotArm is concurrency-safe: all internal state lives behind one
// mutex. In production every call happens on the gocui MainLoop, but the
// mutex keeps the helper safe for test goroutines too (the AC suite
// races second-Arm vs. timer-fire).
type OneshotArm struct {
	ttl time.Duration

	mu       sync.Mutex
	armed    *armState
	armCount uint64 // monotonic — distinguishes a stale timer from the live arm
}

// armState is the live arm record. It is referenced by the helper AND
// captured by the timer closure; both compare armState.id against the
// helper's armCount to ignore stale timer fires.
type armState struct {
	id       uint64
	prefix   string
	scope    string
	suffixes map[rune]Handler
	timer    *time.Timer
}

// NewOneshotArm builds a dispatcher with the supplied TTL. A non-positive
// ttl falls back to DefaultArmTTL.
func NewOneshotArm(ttl time.Duration) *OneshotArm {
	if ttl <= 0 {
		ttl = DefaultArmTTL
	}
	return &OneshotArm{ttl: ttl}
}

// Arm records a new pending one-shot. The returned error is always nil
// today; the signature matches controllers.OneshotArmer.Arm so the
// helper plugs into the controller HelperBag without an adapter.
//
// If an arm is already live, it is dropped before the new one is
// recorded (per AC "second Arm cancels first"). The dropped arm's
// timer is stopped and any pending fire is ignored via the id check.
//
// A nil or empty suffix map still records an arm — Dispatch will treat
// every suffix as "unknown" and silently cancel. This matches the
// expected leader-prefix UX where the next keypress always ends the
// chord.
func (o *OneshotArm) Arm(prefix string, suffixes map[rune]Handler, scope string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.cancelLocked()

	o.armCount++
	id := o.armCount
	st := &armState{
		id:       id,
		prefix:   prefix,
		scope:    scope,
		suffixes: suffixes,
	}
	// AfterFunc runs on its own goroutine; the closure takes the helper
	// mutex and compares id against the current armCount before clearing.
	// A second Arm or an explicit Cancel between the timer firing and the
	// closure grabbing the mutex is therefore a no-op.
	st.timer = time.AfterFunc(o.ttl, func() {
		o.mu.Lock()
		if o.armed != nil && o.armed.id == id {
			o.armed = nil
		}
		o.mu.Unlock()
	})
	o.armed = st
	return nil
}

// Dispatch consumes one suffix keystroke. If an arm is live AND the
// suffix exists in the arm's map, the handler is invoked and the arm is
// cleared. Returns (true, handlerErr) in that case.
//
// If an arm is live but the suffix is NOT in the map, the arm is
// cleared and (false, nil) is returned (silent cancel per AC).
//
// If no arm is live, (false, nil) is returned with no side-effects.
func (o *OneshotArm) Dispatch(suffix rune) (bool, error) {
	o.mu.Lock()
	st := o.armed
	if st == nil {
		o.mu.Unlock()
		return false, nil
	}
	handler, ok := st.suffixes[suffix]
	// Either branch consumes the arm.
	o.cancelLocked()
	o.mu.Unlock()
	if !ok {
		return false, nil
	}
	if handler == nil {
		return true, nil
	}
	return true, handler()
}

// Cancel drops any live arm without firing a handler. Safe to call when
// no arm is live (no-op). Intended targets: ContextTree.RegisterSwapHook
// (context-switch cancellation) and the mouse helper (mouse-event
// cancellation).
func (o *OneshotArm) Cancel() {
	o.mu.Lock()
	o.cancelLocked()
	o.mu.Unlock()
}

// IsArmed reports whether a one-shot is currently waiting for a suffix.
// Test-only helper; exported so the AC suite can assert the post-cancel
// state without poking internals via reflection.
func (o *OneshotArm) IsArmed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.armed != nil
}

// cancelLocked clears the live arm. Caller must hold o.mu.
func (o *OneshotArm) cancelLocked() {
	if o.armed == nil {
		return
	}
	if o.armed.timer != nil {
		o.armed.timer.Stop()
	}
	o.armed = nil
}
