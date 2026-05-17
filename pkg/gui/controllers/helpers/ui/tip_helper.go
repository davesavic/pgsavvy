package ui

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui"
)

// TipHelper owns the first-run tip popup dismissal. The popup itself is
// rendered by a deferred PERSISTENT_POPUP context (E5+); this helper is
// invoked by the connections controller's <esc> / <cr> handler when the
// tip is visible. It performs two things:
//
//  1. Stamps the "tip seen" timestamp via AppStateStore.StampStartupTips
//     so the popup never re-appears on subsequent launches (debounced
//     persistence is handled by the store).
//  2. Pops the popup off the focus stack. The stack may be empty (the
//     tip is opt-in for first-run only); Pop's ErrPopAtBottom is
//     swallowed so the helper is safe to invoke from a "dismiss" key
//     binding even after the popup was auto-dismissed by a context
//     switch.
//
// Concurrency: runs on the MainLoop. The store is goroutine-safe.
type TipHelper struct {
	tree  *gui.ContextTree
	store *common.AppStateStore
}

// NewTipHelper builds a helper bound to the focus-stack tree and the
// app-state store. Either may be nil during early-boot wiring; the
// helper nil-checks at call time so the bag can be assembled in any
// order.
func NewTipHelper(tree *gui.ContextTree, store *common.AppStateStore) *TipHelper {
	return &TipHelper{tree: tree, store: store}
}

// DismissStartupTip stamps the seen-at timestamp and pops the popup.
// Signature matches the controllers.TipHelper interface verbatim.
func (h *TipHelper) DismissStartupTip() error {
	if h.store != nil {
		h.store.StampStartupTips()
	}
	if h.tree != nil {
		// Pop returns ErrPopAtBottom when the stack has only the root
		// entry; that's the "tip was already dismissed" case and we
		// swallow it. Any other error is propagated.
		if err := h.tree.Pop(); err != nil && err != gui.ErrPopAtBottom {
			return err
		}
	}
	return nil
}
