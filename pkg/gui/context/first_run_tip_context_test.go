package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func newTestFirstRunTip(text func() (string, string), drv types.GuiDriver) *FirstRunTipContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.FIRST_RUN_TIP,
		ViewName: string(types.FIRST_RUN_TIP),
		Kind:     types.PERSISTENT_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv, FirstRunTipText: text}
	return NewFirstRunTipContext(base, deps)
}

// TestFirstRunTipContext_NilHookNoOps asserts that with no
// FirstRunTipText hook wired, HandleRender does not write to the driver.
func TestFirstRunTipContext_NilHookNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFirstRunTip(nil, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times with nil hook; want 0", drv.writes)
	}
}

// TestFirstRunTipContext_NilGuiDriverNoPanic asserts HandleRender is
// safe when no driver is wired (test wiring / partial bootstrap).
func TestFirstRunTipContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestFirstRunTip(func() (string, string) { return "T", "B" }, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

// TestFirstRunTipContext_RendersTitleAndBody asserts the rendered text
// contains both the title and the body returned from the hook.
func TestFirstRunTipContext_RendersTitleAndBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFirstRunTip(func() (string, string) {
		return "Welcome", "Press ? for help"
	}, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("SetContent calls = %d, want 1", drv.writes)
	}
	if drv.lastView != string(types.FIRST_RUN_TIP) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.FIRST_RUN_TIP))
	}
	if !strings.Contains(drv.lastContent, "Welcome") {
		t.Errorf("content missing title %q: %q", "Welcome", drv.lastContent)
	}
	if !strings.Contains(drv.lastContent, "Press ? for help") {
		t.Errorf("content missing body %q: %q", "Press ? for help", drv.lastContent)
	}
}

// TestFirstRunTipContext_HasPersistentPopupKind locks the AD-1 promise:
// the context's kind is PERSISTENT_POPUP, distinct from the TEMPORARY_POPUP
// kind used by Confirmation / Prompt / etc.
func TestFirstRunTipContext_HasPersistentPopupKind(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFirstRunTip(nil, drv)
	if got := c.GetKind(); got != types.PERSISTENT_POPUP {
		t.Fatalf("GetKind() = %v, want PERSISTENT_POPUP", got)
	}
	if got := c.GetKey(); got != types.FIRST_RUN_TIP {
		t.Fatalf("GetKey() = %q, want %q", got, types.FIRST_RUN_TIP)
	}
}
