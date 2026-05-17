package ui

import (
	"sync"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// ToastHelper renders a transient message in the status-bar's toast
// slot. The actual status-bar rendering loop reads Current() on every
// repaint; for T7b we only store the message and arm an auto-clear
// timer.
//
// AC contract:
//   - sync.Mutex around the message slot — concurrent toast pushes from
//     different controllers must not race the cleared slot.
//   - auto-clear is scheduled via driver.Update so the clear runs on the
//     gocui MainLoop (D8: no direct goroutine mutation of view state).
//   - the message string is run through session.RedactDSN before being
//     stored, as defense-in-depth against a caller accidentally passing
//     a raw connection-string error message into the visible status bar.
//
// A nil driver disables the auto-clear path entirely; the toast is
// still stored under the mutex and remains until the next Show() call
// overwrites it. This shape lets unit tests exercise the redaction +
// concurrency contracts without supplying a real driver.
type ToastHelper struct {
	driver types.GuiDriver

	mu        sync.Mutex
	current   string
	gen       uint64 // monotonic: timer fires whose gen doesn't match are stale
	clearTime time.Time
}

// NewToastHelper builds a helper bound to the supplied driver. driver
// may be nil for tests; in that case auto-clear is a no-op.
func NewToastHelper(driver types.GuiDriver) *ToastHelper {
	return &ToastHelper{driver: driver}
}

// Show redacts the message, stores it under the helper mutex, and arms
// an auto-clear timer. A subsequent Show() within ttl replaces the
// pending clear (the prior timer's fire becomes a no-op via the gen
// check). Signature matches the controllers.ToastHelper interface.
//
// A non-positive ttl skips the auto-clear: the toast stays until the
// next Show() overwrites it. (No "stays forever" path is needed for
// any current call site, but the helper supports it for diagnostic
// pop-ups.)
func (h *ToastHelper) Show(message string, ttl time.Duration) {
	redacted := session.RedactDSN(message)
	h.mu.Lock()
	h.gen++
	gen := h.gen
	h.current = redacted
	if ttl > 0 {
		h.clearTime = time.Now().Add(ttl)
	} else {
		h.clearTime = time.Time{}
	}
	h.mu.Unlock()

	if ttl <= 0 || h.driver == nil {
		return
	}
	// Schedule the clear via the driver's Update queue. The closure
	// re-checks gen under the mutex so a re-toast that bumped gen wins
	// over a stale timer-fire.
	time.AfterFunc(ttl, func() {
		h.driver.Update(func() error {
			h.mu.Lock()
			if h.gen == gen {
				h.current = ""
				h.clearTime = time.Time{}
			}
			h.mu.Unlock()
			return nil
		})
	})
}

// Current returns the currently visible toast message (already
// redacted). Returns the empty string when no toast is active.
func (h *ToastHelper) Current() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

// Clear immediately drops the visible toast on the calling goroutine.
// Intended for the status-bar's explicit-dismiss path (later epic).
func (h *ToastHelper) Clear() {
	h.mu.Lock()
	h.gen++ // invalidate any pending timer-fire
	h.current = ""
	h.clearTime = time.Time{}
	h.mu.Unlock()
}
