package ui

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// ToastLevel classifies a toast for styling purposes. The current
// emission API (controllers.ToastHelper.Show) takes only (message, ttl)
// — out of scope to extend — so the level is derived
// from the message content inside Show via classifyToastLevel. The
// status-bar renderer (orchestrator.RenderStatusLine) reads it via
// CurrentLevel() to apply a distinguishable foreground style.
type ToastLevel int

const (
	// ToastInfo styles the toast as a success / informational message
	// (green foreground). Default classification.
	ToastInfo ToastLevel = iota
	// ToastError styles the toast as an error (red foreground).
	// Selected when the message contains an error-indicating substring
	// (case-insensitive "fail", "error", "panic").
	ToastError
)

// classifyToastLevel picks a level from the (already-redacted) message
// text. Emission sites (pkg/gui/keys/reload.go, command_line.go) pass
// only a plain string; the heuristic recognises the shipped error
// phrasing ("reload failed: ...", "build panic: ...", "unknown
// ex-command: ...", etc.) so the status bar can paint them red without
// changing the ToastFunc surface. "unknown" is a known error-phrasing
// substring used by pkg/gui/keys/command_line.go for the
// "unknown ex-command: X" toast — confirmed via grep over the
// production emission sites (pkg/gui/keys, pkg/gui/controllers,
// pkg/gui/orchestrator) that no success toast contains "unknown".
func classifyToastLevel(msg string) ToastLevel {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "fail") ||
		strings.Contains(lower, "error") ||
		strings.Contains(lower, "panic") ||
		strings.Contains(lower, "unknown") {
		return ToastError
	}
	return ToastInfo
}

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
	level     ToastLevel
	gen       uint64 // monotonic: timer fires whose gen doesn't match are stale
	clearTime time.Time
	history   []string // bounded ring of every redacted message passed to Show
	// key tags the currently visible toast for ShowOrUpdate. "" means
	// untagged (last writer was a plain Show). Cleared on auto-clear /
	// Clear() so a subsequent ShowOrUpdate with the same key starts a
	// fresh toast rather than silently replacing nothing.
	key string

	// log is the optional structured logger used for the
	// cat=state toast_set instrumentation. Nil-tolerant.
	log *slog.Logger
}

// SetLogger wires the per-session structured logger consumed by the
// cat=state toast_set emit. Must be set before the first Show /
// ShowOrUpdate to be observed.
func (h *ToastHelper) SetLogger(l *slog.Logger) { h.log = l }

// toastHistoryCap caps the in-memory message history. The history is a
// test/diagnostic surface only; cap keeps long-running sessions from
// growing unbounded.
const toastHistoryCap = 64

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
	level := classifyToastLevel(redacted)
	h.mu.Lock()
	h.gen++
	gen := h.gen
	h.current = redacted
	h.level = level
	h.key = "" // plain Show clears any active key tag.
	if ttl > 0 {
		h.clearTime = time.Now().Add(ttl)
	} else {
		h.clearTime = time.Time{}
	}
	h.history = append(h.history, redacted)
	if len(h.history) > toastHistoryCap {
		h.history = h.history[len(h.history)-toastHistoryCap:]
	}
	h.mu.Unlock()

	logs.Event(h.log, "state", "toast_set",
		slog.String("key", ""),
		slog.String("msg_preview", redacted),
		slog.Int64("ttl_ms", ttl.Milliseconds()),
		slog.Uint64("gen", gen),
	)
	h.scheduleAutoClear(gen, ttl)
}

// ShowOrUpdate replaces the message in place if a toast tagged with the
// given key is currently active; otherwise it behaves like Show and
// tags the new toast with key. ttl is applied (or refreshed) on every
// call so a steady stream of updates keeps the toast visible. Passing
// key == "" delegates to Show (untagged emission).
//
// Threading: same contract as Show — the message is redacted via
// session.RedactDSN, stored under the mutex, and the auto-clear timer
// is bumped via the gen counter so any in-flight clear becomes a
// no-op.
func (h *ToastHelper) ShowOrUpdate(key, message string, ttl time.Duration) {
	if key == "" {
		h.Show(message, ttl)
		return
	}
	redacted := session.RedactDSN(message)
	level := classifyToastLevel(redacted)
	h.mu.Lock()
	h.gen++
	gen := h.gen
	h.current = redacted
	h.level = level
	h.key = key
	if ttl > 0 {
		h.clearTime = time.Now().Add(ttl)
	} else {
		h.clearTime = time.Time{}
	}
	h.history = append(h.history, redacted)
	if len(h.history) > toastHistoryCap {
		h.history = h.history[len(h.history)-toastHistoryCap:]
	}
	h.mu.Unlock()

	logs.Event(h.log, "state", "toast_set",
		slog.String("key", key),
		slog.String("msg_preview", redacted),
		slog.Int64("ttl_ms", ttl.Milliseconds()),
		slog.Uint64("gen", gen),
	)
	h.scheduleAutoClear(gen, ttl)
}

// scheduleAutoClear arms the auto-clear AfterFunc shared by Show and
// ShowOrUpdate. The gen check inside the driver.Update closure makes
// stale timer-fires a no-op.
func (h *ToastHelper) scheduleAutoClear(gen uint64, ttl time.Duration) {
	if ttl <= 0 || h.driver == nil {
		return
	}
	time.AfterFunc(ttl, func() {
		h.driver.Update(func() error {
			h.mu.Lock()
			if h.gen == gen {
				h.current = ""
				h.level = ToastInfo
				h.clearTime = time.Time{}
				h.key = ""
			}
			h.mu.Unlock()
			return nil
		})
	})
}

// CurrentLevel returns the level classification of the currently visible
// toast (ToastInfo when the slot is empty). Read by RenderStatusLine to
// pick the foreground style. Snapshotted under the same mutex Current()
// uses so a concurrent Show() cannot deliver (msg, level) torn across
// the two accessors.
func (h *ToastHelper) CurrentLevel() ToastLevel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.level
}

// Current returns the currently visible toast message (already
// redacted). Returns the empty string when no toast is active.
func (h *ToastHelper) Current() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

// History returns a defensive copy of every redacted message the helper
// has accepted via Show, bounded by toastHistoryCap. Newest-last order.
// Test accessor; not consumed by production rendering.
func (h *ToastHelper) History() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.history))
	copy(out, h.history)
	return out
}

// Clear immediately drops the visible toast on the calling goroutine.
// Intended for the status-bar's explicit-dismiss path (later epic).
func (h *ToastHelper) Clear() {
	h.mu.Lock()
	h.gen++ // invalidate any pending timer-fire
	h.current = ""
	h.level = ToastInfo
	h.clearTime = time.Time{}
	h.key = ""
	h.mu.Unlock()
}

// CurrentKey returns the key tag of the currently visible toast (the
// empty string when no toast is active or the toast was emitted via
// Show without a key). Test accessor.
func (h *ToastHelper) CurrentKey() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.key
}
