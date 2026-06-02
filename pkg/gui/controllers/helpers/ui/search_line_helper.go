package ui

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
)

// SearchLineHelper drives the SEARCH_LINE TEMPORARY_POPUP lifecycle: it
// pushes/pops the bottom search input and routes the three callback seams
// the grid-search task (.4) consumes — OnChange (per keystroke),
// OnAccept (<cr>) and OnCancel (<esc>).
//
// This is the SearchLine analogue of PromptHelper. The typed query lives
// in the SEARCH_LINE context's TextArea (master-editor Passthrough); the
// per-keystroke onChange seam is fed by the master editor's
// WithOnPassthroughEdit hook, which calls OnChange with the post-edit
// buffer content.
//
// Pre-search cursor snapshot/restore is injected, not grid-coupled: the
// caller (.4) supplies a snapshot accessor and a restore closure so this
// helper stays free of any grid/result-pane import. On open the snapshot
// is captured; on cancel it is restored AFTER Pop() (the grid context is
// back on top before the cursor is moved). Accept does not restore — the
// match the user landed on stays selected.
//
// Concurrency: every method runs on the gocui MainLoop. The mutex
// defends the same forward-compatibility surface PromptHelper guards.
type SearchLineHelper struct {
	tree   *gui.ContextTree
	search *guicontext.SearchLineContext

	mu        sync.Mutex
	active    bool
	onChange  func(query string)
	onAccept  func(query string)
	onCancel  func()
	snapshot  any
	cursorSet func(snapshot any)
	// caretToggler flips the global gocui caret on Open and off on
	// Accept / Cancel. Mirrors PromptHelper.caretToggler. Nil → no-op.
	caretToggler func(enabled bool)
}

// NewSearchLineHelper builds a helper bound to the focus stack and the
// concrete SEARCH_LINE context.
func NewSearchLineHelper(tree *gui.ContextTree, search *guicontext.SearchLineContext) *SearchLineHelper {
	return &SearchLineHelper{tree: tree, search: search}
}

// SearchLineOpts bundles the callback + cursor seams a caller registers
// when opening the search input.
type SearchLineOpts struct {
	// OnChange fires per applied keystroke with the post-edit query.
	OnChange func(query string)
	// OnAccept fires on <cr> with the final query (before pop).
	OnAccept func(query string)
	// OnCancel fires on <esc> after pop + cursor restore.
	OnCancel func()
	// CursorSnapshot captures the pre-search cursor; its return value is
	// passed verbatim to CursorRestore on cancel. Nil → no snapshot.
	CursorSnapshot func() (snapshot any)
	// CursorRestore restores the snapshot captured at open. Invoked on
	// cancel AFTER Pop(). Nil → no restore.
	CursorRestore func(snapshot any)
}

// Open pushes the SEARCH_LINE popup, records the callback + cursor seams,
// snapshots the pre-search cursor, and flips the caret on. Returns
// tree.Push errors.
func (h *SearchLineHelper) Open(opts SearchLineOpts) error {
	h.mu.Lock()
	h.active = true
	h.onChange = opts.OnChange
	h.onAccept = opts.OnAccept
	h.onCancel = opts.OnCancel
	h.cursorSet = opts.CursorRestore
	h.snapshot = nil
	if opts.CursorSnapshot != nil {
		h.snapshot = opts.CursorSnapshot()
	}
	caret := h.caretToggler
	h.mu.Unlock()

	if h.tree == nil || h.search == nil {
		return nil
	}
	if err := h.tree.Push(h.search); err != nil {
		return err
	}
	// Caret on AFTER Push so focus is already on SEARCH_LINE — gocui
	// draws the caret at the current view's cursor (mirrors PromptHelper).
	if caret != nil {
		caret(true)
	}
	return nil
}

// SetCaretToggler registers fn as the caret-on/off callback. Bootstrap
// wires this to driver.SetCaretEnabled; nil is a no-op.
func (h *SearchLineHelper) SetCaretToggler(fn func(enabled bool)) {
	h.mu.Lock()
	h.caretToggler = fn
	h.mu.Unlock()
}

// OnChange forwards the post-edit query to the registered onChange
// callback. Fed by the master editor's WithOnPassthroughEdit seam on
// every applied keystroke. The seam is pure: it MUST NOT re-enter the
// dispatcher, push/pop the stack, or render (AD-4) — it just invokes the
// stored callback.
func (h *SearchLineHelper) OnChange(query string) {
	h.mu.Lock()
	cb := h.onChange
	h.mu.Unlock()
	if cb != nil {
		cb(query)
	}
}

// OnAccept fires the accept callback with query, pops the popup, and
// flips the caret off. Driven by the SEARCH_LINE <cr> handler (.4). The
// pre-search cursor is NOT restored — the user keeps the match they
// accepted.
func (h *SearchLineHelper) OnAccept(query string) error {
	h.mu.Lock()
	cb := h.onAccept
	caret := h.caretToggler
	h.clearLocked()
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
	cb(query)
	return nil
}

// OnCancel pops the popup, restores the pre-search cursor AFTER Pop()
// (grid context back on top), then fires the cancel callback and flips
// the caret off. Driven by the SEARCH_LINE <esc> handler (.4).
func (h *SearchLineHelper) OnCancel() error {
	h.mu.Lock()
	cb := h.onCancel
	caret := h.caretToggler
	restore := h.cursorSet
	snap := h.snapshot
	h.clearLocked()
	h.mu.Unlock()

	if h.tree != nil {
		_ = h.tree.Pop()
	}
	// Restore AFTER Pop so the grid context owns the focus before the
	// cursor moves (AD-5).
	if restore != nil {
		restore(snap)
	}
	if caret != nil {
		caret(false)
	}
	if cb == nil {
		return nil
	}
	cb()
	return nil
}

// SetMatchCount forwards the right-aligned count slot text to the
// SEARCH_LINE context. Nil-safe when no context is wired.
func (h *SearchLineHelper) SetMatchCount(s string) {
	if h.search == nil {
		return
	}
	h.search.SetMatchCount(s)
}

// Active reports whether the search input is currently open.
func (h *SearchLineHelper) Active() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active
}

// clearLocked resets the per-open state. Caller holds h.mu.
func (h *SearchLineHelper) clearLocked() {
	h.active = false
	h.onChange = nil
	h.onAccept = nil
	h.onCancel = nil
	h.cursorSet = nil
	h.snapshot = nil
}
