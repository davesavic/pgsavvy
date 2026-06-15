package context

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestWriteView_ToleratesUnknownView reproduces the commit-dialog crash:
// writeView queues a view write on the gocui Update queue, and the closure
// returns gocui.ErrUnknownView when the target view was deleted between the
// schedule and the drain (popup-replaces-popup evicts + deletes the view,
// the recreating layout pass races the queued write). gocui's MainLoop
// treats a returned error as FATAL, so a benign "view gone" must NOT
// escape the closure — the layout repaints the view next frame.
func TestWriteView_ToleratesUnknownView(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	deps := types.ContextTreeDeps{GuiDriver: drv}

	writeView(deps, func() error { return gocui.ErrUnknownView })

	if errs := drv.UpdateErrors(); len(errs) != 0 {
		t.Fatalf("writeView leaked %d error(s) to the Update queue (gocui MainLoop would exit fatally): %v", len(errs), errs)
	}
}

// TestWriteView_PropagatesRealErrors guards the fix from over-swallowing:
// any error that is NOT ErrUnknownView must still reach the Update queue so
// genuine failures are not silently hidden.
func TestWriteView_PropagatesRealErrors(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	deps := types.ContextTreeDeps{GuiDriver: drv}

	sentinel := errors.New("boom")
	writeView(deps, func() error { return sentinel })

	errs := drv.UpdateErrors()
	if len(errs) != 1 || !errors.Is(errs[0], sentinel) {
		t.Fatalf("writeView should propagate non-ErrUnknownView errors, got: %v", errs)
	}
}
