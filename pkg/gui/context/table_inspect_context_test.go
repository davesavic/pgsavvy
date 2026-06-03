package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// stubInspectPanel is a minimal popup.Panel impl for tests: returns a
// fixed body and ignores keys.
type stubInspectPanel struct{ body string }

func (s *stubInspectPanel) Body() string             { return s.body }
func (s *stubInspectPanel) HandleKey(types.Key) bool { return false }

// newTestTableInspect builds a TableInspectContext bound to TABLE_INSPECT
// with the supplied driver in its deps.
func newTestTableInspect(drv types.GuiDriver) *TableInspectContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.TABLE_INSPECT,
		ViewName: string(types.TABLE_INSPECT),
		Kind:     types.TEMPORARY_POPUP,
		Title:    "Table inspect",
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewTableInspectContext(base, deps)
}

func TestNewTableInspectContext_Kinds(t *testing.T) {
	c := newTestTableInspect(nil)
	if got := c.GetKey(); got != types.TABLE_INSPECT {
		t.Errorf("GetKey() = %q, want %q", got, types.TABLE_INSPECT)
	}
	if got := c.GetKind(); got != types.TEMPORARY_POPUP {
		t.Errorf("GetKind() = %d, want %d", got, types.TEMPORARY_POPUP)
	}
	if got := c.GetViewName(); got != "table_inspect" {
		t.Errorf("GetViewName() = %q, want %q", got, "table_inspect")
	}
}

func TestTableInspectContext_HandleRender_LoadingPrefix(t *testing.T) {
	drv := &captureDriver{}
	c := newTestTableInspect(drv)
	c.SetLoading(true)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if !strings.HasPrefix(drv.lastContent, "Loading") {
		t.Errorf("lastContent = %q; want prefix \"Loading\"", drv.lastContent)
	}
	if drv.lastView != string(types.TABLE_INSPECT) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.TABLE_INSPECT))
	}
}

func TestTableInspectContext_HandleRender_DelegatesToBody(t *testing.T) {
	drv := &captureDriver{}
	c := newTestTableInspect(drv)
	c.SetLoading(false)
	pop := popup.NewTabbedPopup([]popup.Tab{
		{Title: "Cols", Panel: &stubInspectPanel{body: "column-rows"}},
	})
	c.SetState(pop)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	// The TabbedPopup body emits "<header>\n\n<panel-body>" — assert
	// the panel body is included verbatim (SafeText is a no-op on
	// safe input).
	if !strings.Contains(drv.lastContent, "column-rows") {
		t.Errorf("lastContent = %q; want to contain panel body", drv.lastContent)
	}
}

// Regression test for dbsavvy-3vf: the context layer must NOT strip
// control bytes from the composed body. AD-17 places SafeText at the
// leaf panel layer (columnCells / indexCells). Stripping at this
// layer destroys the legitimate \x1b color escape on the active-tab
// header and every \n between header and rows, collapsing the popup
// into a single unreadable line. See bug report 2026-05-23.
func TestTableInspectContext_HandleRender_PreservesEscapesAndNewlines(t *testing.T) {
	drv := &captureDriver{}
	c := newTestTableInspect(drv)
	pop := popup.NewTabbedPopup([]popup.Tab{
		{Title: "Cols", Panel: &stubInspectPanel{body: "row-1\nrow-2"}},
		{Title: "Idx", Panel: &stubInspectPanel{body: "x"}},
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
	if !strings.Contains(drv.lastContent, "row-1\nrow-2") {
		t.Errorf("lastContent missing inter-row newline: %q", drv.lastContent)
	}
}

func TestTableInspectContext_Target_RoundTrip(t *testing.T) {
	c := newTestTableInspect(nil)
	s, tb := c.Target()
	if s != "" || tb != "" {
		t.Errorf("zero Target() = (%q, %q); want (\"\", \"\")", s, tb)
	}
	c.SetTarget("public", "users")
	s, tb = c.Target()
	if s != "public" || tb != "users" {
		t.Errorf("Target() = (%q, %q); want (\"public\", \"users\")", s, tb)
	}
}

func TestTableInspectContext_HandleRender_NilGuiDriver_NoPanic(t *testing.T) {
	c := newTestTableInspect(nil)
	c.SetLoading(true)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver (loading): %v", err)
	}
	c.SetLoading(false)
	c.SetState(popup.NewTabbedPopup([]popup.Tab{
		{Title: "T", Panel: &stubInspectPanel{body: "x"}},
	}))
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver (state): %v", err)
	}
}

func TestTableInspectContext_StateAccessors(t *testing.T) {
	c := newTestTableInspect(nil)
	if c.State() != nil {
		t.Errorf("State() = %v; want nil at construction", c.State())
	}
	if c.IsLoading() {
		t.Error("IsLoading() = true at construction; want false")
	}
	pop := popup.NewTabbedPopup(nil)
	c.SetState(pop)
	if c.State() != pop {
		t.Error("State() did not round-trip SetState")
	}
	c.SetLoading(true)
	if !c.IsLoading() {
		t.Error("IsLoading() = false after SetLoading(true)")
	}
}
