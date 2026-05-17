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
	h.mu.Unlock()
	if h.tree == nil || h.prompt == nil {
		return nil
	}
	return h.tree.Push(h.prompt)
}

// Submit invokes onSubmit(value), pops the popup, and clears the helper
// state. Driven by the PROMPT context's "<cr>" handler in a future
// epic.
func (h *PromptHelper) Submit(value string) error {
	h.mu.Lock()
	cb := h.onSubmit
	h.active = false
	h.onSubmit = nil
	h.onCancel = nil
	h.mu.Unlock()
	if h.tree != nil {
		_ = h.tree.Pop()
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
	h.active = false
	h.onSubmit = nil
	h.onCancel = nil
	h.mu.Unlock()
	if h.tree != nil {
		_ = h.tree.Pop()
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
