package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newTestFKReversePicker(drv types.GuiDriver) *FKReversePickerContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      FKReversePickerContextKey,
		ViewName: string(FKReversePickerContextKey),
		Kind:     types.TEMPORARY_POPUP,
		Title:    "FK reverse picker",
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewFKReversePickerContext(base, deps)
}

// fkReverseLeaf builds a DisplayLeafContext wired to the picker's shared view
// carrying the supplied body — the runtime leaf the controller injects.
func fkReverseLeaf(c *FKReversePickerContext, drv types.GuiDriver, body string) types.IBaseContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      FKReversePickerContextKey,
		ViewName: c.GetViewName(),
		Kind:     types.DISPLAY_CONTEXT,
	})
	return NewDisplayLeafContext(base, types.ContextTreeDeps{GuiDriver: drv}, c.GetViewName(), body)
}

func TestNewFKReversePickerContext_Identity(t *testing.T) {
	c := newTestFKReversePicker(nil)
	if got := c.GetKey(); got != FKReversePickerContextKey {
		t.Errorf("GetKey() = %q, want %q", got, FKReversePickerContextKey)
	}
	if got := c.GetKind(); got != types.TEMPORARY_POPUP {
		t.Errorf("GetKind() = %d, want TEMPORARY_POPUP (%d)", got, types.TEMPORARY_POPUP)
	}
	if got := c.GetViewName(); got != "fk_reverse_picker" {
		t.Errorf("GetViewName() = %q, want %q", got, "fk_reverse_picker")
	}
}

// HandleRender writes the ACTIVE leaf's body verbatim into the shared view.
// AD-17 places SafeText at the leaf-body layer, not here; the container must
// NOT re-strip the body, so newlines survive intact.
func TestFKReversePickerContext_HandleRender_DelegatesToActiveLeafBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFKReversePicker(drv)
	specs := []TabSpec{{Label: "orders.user_id", LeafKey: "fk_reverse_0"}}
	leaves := []types.IBaseContext{fkReverseLeaf(c, drv, "orders\n~50 rows")}
	c.SetTabs(specs, leaves)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "orders\n~50 rows") {
		t.Errorf("lastContent = %q; want to contain active leaf body", drv.lastContent)
	}
}

// Cycling the active tab re-renders the NEW active leaf's body, and the body is
// written verbatim (newlines preserved) — the regression mirror for TableInspect.
func TestFKReversePickerContext_HandleRender_ActiveLeafFollowsTab(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFKReversePicker(drv)
	specs := []TabSpec{
		{Label: "orders.user_id", LeafKey: "fk_reverse_0"},
		{Label: "comments.user_id", LeafKey: "fk_reverse_1"},
	}
	leaves := []types.IBaseContext{
		fkReverseLeaf(c, drv, "orders\n~50 rows"),
		fkReverseLeaf(c, drv, "comments\n~12 rows"),
	}
	c.SetTabs(specs, leaves)

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 0: %v", err)
	}
	if !strings.Contains(drv.lastContent, "orders\n~50 rows") {
		t.Errorf("tab 0 body = %q; want orders body with newline preserved", drv.lastContent)
	}

	c.NextTab()
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 1: %v", err)
	}
	if !strings.Contains(drv.lastContent, "comments\n~12 rows") {
		t.Errorf("tab 1 body = %q; want comments body with newline preserved", drv.lastContent)
	}
}

func TestFKReversePickerContext_HandleRender_NoTabsEmptyBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFKReversePicker(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "" {
		t.Errorf("lastContent = %q; want empty with no tabs", drv.lastContent)
	}
}

func TestFKReversePickerContext_HandleRender_NilGuiDriver_NoPanic(t *testing.T) {
	c := newTestFKReversePicker(nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver / no tabs: %v", err)
	}
	c.SetTabs(
		[]TabSpec{{Label: "t", LeafKey: "fk_reverse_0"}},
		[]types.IBaseContext{fkReverseLeaf(c, nil, "x")},
	)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver / one tab: %v", err)
	}
}
