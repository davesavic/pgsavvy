package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// stubReversePanel is a minimal popup.Panel for the picker tests.
type stubReversePanel struct{ body string }

func (s *stubReversePanel) Body() string             { return s.body }
func (s *stubReversePanel) HandleKey(types.Key) bool { return false }

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

func TestFKReversePickerContext_HandleRender_DelegatesToBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFKReversePicker(drv)
	pop := popup.NewTabbedPopup([]popup.Tab{
		{Title: "orders.user_id", Panel: &stubReversePanel{body: "orders\n~50 rows"}},
	})
	c.SetState(pop)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if !strings.Contains(drv.lastContent, "orders\n~50 rows") {
		t.Errorf("lastContent = %q; want to contain panel body", drv.lastContent)
	}
}

// Mirrors the regression test for TableInspect: the context
// layer must NOT strip ANSI / newlines from the composed body. AD-17
// places SafeText at the leaf panel layer, not here.
func TestFKReversePickerContext_HandleRender_PreservesEscapesAndNewlines(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFKReversePicker(drv)
	pop := popup.NewTabbedPopup([]popup.Tab{
		{Title: "orders.user_id", Panel: &stubReversePanel{body: "orders\n~50 rows"}},
		{Title: "comments.user_id", Panel: &stubReversePanel{body: "comments\n~12 rows"}},
	})
	c.SetState(pop)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.ContainsRune(drv.lastContent, '\x1b') {
		t.Errorf("lastContent missing ESC byte (active-tab color destroyed): %q", drv.lastContent)
	}
	if !strings.Contains(drv.lastContent, "\n\n") {
		t.Errorf("lastContent missing header/body separator newlines: %q", drv.lastContent)
	}
}

func TestFKReversePickerContext_HandleRender_NilStateEmptyBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestFKReversePicker(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "" {
		t.Errorf("lastContent = %q; want empty when state is nil", drv.lastContent)
	}
}

func TestFKReversePickerContext_HandleRender_NilGuiDriver_NoPanic(t *testing.T) {
	c := newTestFKReversePicker(nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver / nil state: %v", err)
	}
	c.SetState(popup.NewTabbedPopup([]popup.Tab{
		{Title: "t", Panel: &stubReversePanel{body: "x"}},
	}))
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver / non-nil state: %v", err)
	}
}

func TestFKReversePickerContext_StateAccessors(t *testing.T) {
	c := newTestFKReversePicker(nil)
	if c.State() != nil {
		t.Errorf("State() = %v; want nil at construction", c.State())
	}
	pop := popup.NewTabbedPopup(nil)
	c.SetState(pop)
	if c.State() != pop {
		t.Error("State() did not round-trip SetState")
	}
}
