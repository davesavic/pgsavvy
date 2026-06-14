package ui

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
)

// PromptHelper pushes the PROMPT popup (TEMPORARY_POPUP per T2). Like
// ConfirmHelper this is a thin facade — the line-editor + autocomplete
// suggestion wiring lives in PromptContext + its dedicated controller
// (later epic). For T7b we provide the push/pop seam plus onSubmit /
// onCancel callback storage so the connections "add-flow" can chain
// prompts through the helper interface.
//
// Concurrency: per D8 every method runs on the gocui MainLoop. The
// mutex defends against the same forward-compatibility surface called
// out on ConfirmHelper.
type PromptHelper struct {
	tree   *gui.ContextTree
	prompt *guicontext.PromptContext

	mu       sync.Mutex
	label    string
	initial  string
	onSubmit func(value string) error
	onCancel func() error
	active   bool
	onReset  func(initial string)
	// caretToggler flips the global gocui caret on Prompt and off on
	// Submit / Cancel. Without this the layout pass's SetViewCursor sets
	// the caret position but gocui's flush() skips Screen.ShowCursor
	// when g.Cursor is false — the user sees a popup with no caret.
	// Mirrors CommandLineCommandDeps.CaretToggler. Nil → no-op.
	caretToggler func(enabled bool)
}

// NewPromptHelper builds a helper bound to the focus stack and the
// concrete PROMPT context. Both are required.
func NewPromptHelper(tree *gui.ContextTree, prompt *guicontext.PromptContext) *PromptHelper {
	return &PromptHelper{tree: tree, prompt: prompt}
}

// Prompt pushes the PROMPT popup with the supplied label / initial
// value and records the submit / cancel callbacks. Signature matches the
// controllers.PromptHelper interface verbatim. Returns tree.Push errors.
func (h *PromptHelper) Prompt(label, initial string, onSubmit func(value string) error, onCancel func() error) error {
	h.mu.Lock()
	h.label = label
	h.initial = initial
	h.onSubmit = onSubmit
	h.onCancel = onCancel
	h.active = true
	reset := h.onReset
	caret := h.caretToggler
	h.mu.Unlock()
	// Notify the PromptController so it re-seeds its line buffer with
	// the new initial value BEFORE the popup is pushed.
	// The controller subscribes via SetResetHandler;
	// nil-safe when no subscriber is wired (tests, early bootstrap).
	if reset != nil {
		reset(initial)
	}
	if h.tree == nil || h.prompt == nil {
		return nil
	}
	if err := h.tree.Push(h.prompt); err != nil {
		return err
	}
	// Caret on AFTER Push so the focus is already on PROMPT — gocui
	// draws the caret at the current view's cursor, so flipping caret
	// while the previous rail is current would briefly draw there.
	if caret != nil {
		caret(true)
	}
	return nil
}

// SetResetHandler registers fn as the buffer-reset callback. Invoked
// from Prompt(label, initial, ...) with the new initial value so the
// PromptController can re-seed its line buffer. Pass nil to clear the
// subscription.
func (h *PromptHelper) SetResetHandler(fn func(initial string)) {
	h.mu.Lock()
	h.onReset = fn
	h.mu.Unlock()
}

// SetCaretToggler registers fn as the caret-on/off callback. Invoked
// with true after Prompt activates the popup and with false after
// Submit / Cancel pop it. Bootstrap wires this to
// driver.SetCaretEnabled; nil is treated as a no-op so unit tests
// without a driver keep compiling.
func (h *PromptHelper) SetCaretToggler(fn func(enabled bool)) {
	h.mu.Lock()
	h.caretToggler = fn
	h.mu.Unlock()
}

// Submit invokes onSubmit(value), pops the popup, and clears the helper
// state. Driven by the PROMPT context's "<cr>" handler in a future
// epic.
func (h *PromptHelper) Submit(value string) error {
	h.mu.Lock()
	cb := h.onSubmit
	caret := h.caretToggler
	h.active = false
	h.onSubmit = nil
	h.onCancel = nil
	h.mu.Unlock()
	if h.tree != nil {
		_ = h.tree.Pop()
	}
	// Caret off AFTER Pop — same ordering rationale as command.cancel:
	// gocui draws the caret at the current view's cursor, so leaving
	// caret=true pointed at a just-deleted PROMPT view would briefly
	// draw at (0,0) of whatever rail is now current.
	if caret != nil {
		caret(false)
	}
	if cb == nil {
		return nil
	}
	return cb(value)
}

// Cancel invokes onCancel, pops the popup, and clears state. Esc path.
func (h *PromptHelper) Cancel() error {
	h.mu.Lock()
	cb := h.onCancel
	caret := h.caretToggler
	h.active = false
	h.onSubmit = nil
	h.onCancel = nil
	h.mu.Unlock()
	if h.tree != nil {
		_ = h.tree.Pop()
	}
	if caret != nil {
		caret(false)
	}
	if cb == nil {
		return nil
	}
	return cb()
}

// Active reports whether a prompt popup is currently waiting.
func (h *PromptHelper) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// Label returns the most recent prompt label. Test accessor.
func (h *PromptHelper) Label() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.label
}

// Initial returns the most recent initial value. Test accessor.
func (h *PromptHelper) Initial() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.initial
}
