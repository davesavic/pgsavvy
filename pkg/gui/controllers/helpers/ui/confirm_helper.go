package ui

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
)

// ConfirmHelper pushes the CONFIRMATION popup onto the focus stack and
// remembers the onYes / onNo callbacks the controller wants invoked on
// dismissal. Per dbsavvy-enn-T2 CONFIRMATION is a TEMPORARY_POPUP, so
// pushing it auto-replaces a top-of-stack temporary popup (which is the
// desired UX — only one popup visible at a time).
//
// The helper does NOT own the popup's view contents or its key handlers
// — that wiring lives in the confirmation context + its dedicated
// controller (E5+). For T7b we only own the push/pop seam and the
// callback book-keeping.
//
// Concurrency: all methods run on the gocui MainLoop per D8. The mutex
// is defensive against future cross-goroutine callers (a debounced AppState
// save firing a deferred prompt, say).
type ConfirmHelper struct {
	tree         *gui.ContextTree
	confirmation *guicontext.ConfirmationContext

	mu     sync.Mutex
	onYes  func() error
	onNo   func() error
	title  string
	body   string
	active bool
}

// NewConfirmHelper builds a helper bound to the focus-stack tree and the
// concrete CONFIRMATION context from the context registry. Both are
// required.
func NewConfirmHelper(tree *gui.ContextTree, confirmation *guicontext.ConfirmationContext) *ConfirmHelper {
	return &ConfirmHelper{tree: tree, confirmation: confirmation}
}

// Confirm pushes the CONFIRMATION popup with the supplied title/body and
// records the onYes / onNo callbacks. The signature matches the
// controllers.ConfirmHelper interface verbatim so the helper plugs into
// HelperBag without an adapter.
//
// onYes is the only required callback; onNo may be nil (treated as a
// silent dismiss). Returns the tree.Push error verbatim — the AC
// requires the controller to surface a popup-push failure to the caller.
func (h *ConfirmHelper) Confirm(title, body string, onYes func() error, onNo func() error) error {
	h.mu.Lock()
	h.title = title
	h.body = body
	h.onYes = onYes
	h.onNo = onNo
	h.active = true
	h.mu.Unlock()
	if h.tree == nil || h.confirmation == nil {
		return nil
	}
	return h.tree.Push(h.confirmation)
}

// Yes invokes the recorded onYes callback (if any), pops the popup, and
// clears the helper's pending state. Called by the CONFIRMATION
// context's "y" / "<cr>" handler in a later epic; exposed now so the
// AC suite can drive the callback path without a real popup view.
func (h *ConfirmHelper) Yes() error {
	cb := h.consume()
	if cb == nil {
		return nil
	}
	return cb()
}

// No invokes the recorded onNo callback (if any), pops the popup, and
// clears the helper's pending state. Esc / "n" path.
func (h *ConfirmHelper) No() error {
	h.mu.Lock()
	cb := h.onNo
	h.active = false
	h.onYes = nil
	h.onNo = nil
	h.mu.Unlock()
	if h.tree != nil {
		_ = h.tree.Pop()
	}
	if cb == nil {
		return nil
	}
	return cb()
}

// Active reports whether a confirm popup is currently waiting on the
// user. Useful for tests and for the layout's "is a popup visible"
// short-circuit.
func (h *ConfirmHelper) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// Title returns the most recent prompt title. Test-friendly accessor.
func (h *ConfirmHelper) Title() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.title
}

// Body returns the most recent prompt body. Test-friendly accessor.
func (h *ConfirmHelper) Body() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.body
}

// consume removes the popup from the stack and returns the onYes
// callback (so the caller invokes it OUTSIDE the lock).
func (h *ConfirmHelper) consume() func() error {
	h.mu.Lock()
	cb := h.onYes
	h.active = false
	h.onYes = nil
	h.onNo = nil
	h.mu.Unlock()
	if h.tree != nil {
		_ = h.tree.Pop()
	}
	return cb
}
