package ui

import (
	"fmt"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
)

// ChoiceHelper pushes the SELECTION popup (TEMPORARY_POPUP per
// dbsavvy-m47.2). Mirrors PromptHelper's shape but for a list-style
// picker: instead of a label+initial pair the caller supplies a label +
// a slice of choices, and onSubmit receives the selected index.
//
// Unlike PromptHelper, the cursor lives on the helper itself rather
// than the controller. SelectionController reads/mutates the cursor
// through Cursor/SetCursor — there is no SetResetHandler.
//
// Concurrency: per D8 every method runs on the gocui MainLoop. The
// mutex defends the forward-compatibility surface the same way
// ConfirmHelper / PromptHelper do.
type ChoiceHelper struct {
	tree      *gui.ContextTree
	selection *guicontext.SelectionContext

	mu       sync.Mutex
	label    string
	choices  []string
	cursor   int
	onSubmit func(idx int) error
	onCancel func() error
	active   bool
}

// NewChoiceHelper builds a helper bound to the focus stack and the
// concrete SELECTION context. Both are required for the push to land.
func NewChoiceHelper(tree *gui.ContextTree, selection *guicontext.SelectionContext) *ChoiceHelper {
	return &ChoiceHelper{tree: tree, selection: selection}
}

// Choose pushes the SELECTION popup with the supplied label / choices
// and records the submit/cancel callbacks. Cursor resets to 0 on every
// Choose. Signature matches controllers.ChoiceHelper verbatim. Returns
// tree.Push errors.
func (h *ChoiceHelper) Choose(label string, choices []string, onSubmit func(idx int) error, onCancel func() error) error {
	h.mu.Lock()
	h.label = label
	h.choices = choices
	h.cursor = 0
	h.onSubmit = onSubmit
	h.onCancel = onCancel
	h.active = true
	h.mu.Unlock()
	if h.tree == nil || h.selection == nil {
		return nil
	}
	return h.tree.Push(h.selection)
}

// Submit invokes onSubmit(idx), pops the popup, and clears the helper
// state. Returns an error WITHOUT invoking the callback or popping when
// idx is out of range. Called from the SelectionController's "<cr>"
// handler with the current cursor value.
func (h *ChoiceHelper) Submit(idx int) error {
	h.mu.Lock()
	if idx < 0 || idx >= len(h.choices) {
		n := len(h.choices)
		h.mu.Unlock()
		return fmt.Errorf("selection.submit: index %d out of range [0,%d)", idx, n)
	}
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
	return cb(idx)
}

// Cancel invokes onCancel, pops the popup, and clears state. Esc path.
func (h *ChoiceHelper) Cancel() error {
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

// Active reports whether a selection popup is currently waiting.
func (h *ChoiceHelper) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// Label returns the most recent label. Test accessor.
func (h *ChoiceHelper) Label() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.label
}

// Choices returns the most recent choice slice. Returns the live slice
// (callers must not mutate). Test accessor.
func (h *ChoiceHelper) Choices() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.choices
}

// Cursor returns the current cursor index. Returns 0 when no choices
// are loaded.
func (h *ChoiceHelper) Cursor() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cursor
}

// Selected is an alias for Cursor exposing the AC's preferred
// terminology.
func (h *ChoiceHelper) Selected() int {
	return h.Cursor()
}

// SetCursor sets the cursor index, clamping to [0, len(choices)-1].
// When choices is empty the cursor stays at 0.
func (h *ChoiceHelper) SetCursor(i int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(h.choices)
	if n == 0 {
		h.cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i > n-1 {
		i = n - 1
	}
	h.cursor = i
}
