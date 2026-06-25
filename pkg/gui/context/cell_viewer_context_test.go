package context

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newCellViewerBase() BaseContext {
	return NewBaseContext(BaseContextOpts{
		Key:      types.CELL_VIEWER,
		ViewName: string(types.CELL_VIEWER),
		Kind:     types.PERSISTENT_POPUP,
		Title:    "Cell viewer",
	})
}

func newCellViewerForTest(drv types.GuiDriver) *CellViewerContext {
	deps := Deps{GuiDriver: drv}
	return NewCellViewerContext(newCellViewerBase(), deps)
}

func newRecorderWithViewerViews() *testfake.RecorderGuiDriver {
	drv := testfake.NewRecorderGuiDriver()
	drv.SetRealView(string(types.CELL_VIEWER), gocui.NewView(string(types.CELL_VIEWER), 0, 0, 80, 24, gocui.OutputNormal))
	return drv
}

func TestCellViewer_KindIsPersistentPopup(t *testing.T) {
	c := newCellViewerForTest(nil)
	if got := c.GetKind(); got != types.PERSISTENT_POPUP {
		t.Fatalf("GetKind() = %v, want PERSISTENT_POPUP", got)
	}
	if got := c.GetKey(); got != types.CELL_VIEWER {
		t.Fatalf("GetKey() = %q, want %q", got, types.CELL_VIEWER)
	}
}

func TestCellViewer_NeedsRerenderOnWidthChange(t *testing.T) {
	c := newCellViewerForTest(nil)
	if !c.NeedsRerenderOnWidthChange() {
		t.Error("NeedsRerenderOnWidthChange() = false, want true")
	}
}

func TestCellViewer_CursorXY(t *testing.T) {
	c := newCellViewerForTest(nil)
	x, y, ok := c.CursorXY()
	if ok {
		t.Error("CursorXY ok = true, want false")
	}
	if x != 0 || y != 0 {
		t.Errorf("CursorXY = (%d, %d), want (0, 0)", x, y)
	}
}

func TestCellViewer_HandleFocusNoOp(t *testing.T) {
	c := newCellViewerForTest(nil)
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
}

func TestCellViewer_HandleFocusLost(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.view = nil // starts nil
	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if c.view != nil {
		t.Error("view not cleared after HandleFocusLost")
	}
}

func TestCellViewer_OpenClose(t *testing.T) {
	c := newCellViewerForTest(nil)
	if c.Active() {
		t.Error("Active() = true before Open()")
	}

	col := models.ColumnMeta{Name: "email", TypeName: "text"}
	c.Open("test@example.com", col, []any{1, 2})

	if !c.Active() {
		t.Fatal("Active() = false after Open()")
	}
	if c.OriginalValue() != "test@example.com" {
		t.Errorf("OriginalValue() = %q, want %q", c.OriginalValue(), "test@example.com")
	}
	if c.Colname() != "email" {
		t.Errorf("Colname() = %q, want %q", c.Colname(), "email")
	}
	if c.Typename() != "text" {
		t.Errorf("Typename() = %q, want %q", c.Typename(), "text")
	}
	if c.Wrap() != true {
		t.Error("Wrap() = false after Open(), want true")
	}
	if c.Pretty() != true {
		t.Error("Pretty() = false after Open(), want true")
	}
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("scroll = (%d, %d), want (0, 0)", c.ScrollX(), c.ScrollY())
	}

	pk := c.PrimaryKey()
	if len(pk) != 2 || pk[0] != 1 || pk[1] != 2 {
		t.Errorf("PrimaryKey() = %v, want [1 2]", pk)
	}

	c.Close()
	if c.Active() {
		t.Error("Active() = true after Close()")
	}
	if c.OriginalValue() != nil {
		t.Error("OriginalValue() not nil after Close()")
	}
}

func TestCellViewer_ToggleWrap(t *testing.T) {
	c := newCellViewerForTest(nil)
	if !c.Wrap() {
		t.Error("Wrap() = false at init, want true")
	}
	c.ToggleWrap()
	if c.Wrap() {
		t.Error("Wrap() = true after toggle, want false")
	}
	c.ToggleWrap()
	if !c.Wrap() {
		t.Error("Wrap() = false after second toggle, want true")
	}
}

func TestCellViewer_TogglePretty(t *testing.T) {
	c := newCellViewerForTest(nil)
	if !c.Pretty() {
		t.Error("Pretty() = false at init, want true")
	}
	c.TogglePretty()
	if c.Pretty() {
		t.Error("Pretty() = true after toggle, want false")
	}
	c.TogglePretty()
	if !c.Pretty() {
		t.Error("Pretty() = false after second toggle, want true")
	}
}

func TestCellViewer_OpenResetsToggles(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.ToggleWrap()
	c.TogglePretty()
	c.Open("val", models.ColumnMeta{Name: "a"}, nil)
	if !c.Wrap() {
		t.Error("Wrap() = false after Open(), want reset to true")
	}
	if !c.Pretty() {
		t.Error("Pretty() = false after Open(), want reset to true")
	}
}

func TestCellViewer_ScrollClamp(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.SetScrollX(-5)
	c.SetScrollY(-10)
	if c.ScrollX() != 0 {
		t.Errorf("ScrollX() = %d after negative set, want 0", c.ScrollX())
	}
	if c.ScrollY() != 0 {
		t.Errorf("ScrollY() = %d after negative set, want 0", c.ScrollY())
	}

	c.SetScrollX(10)
	c.SetScrollY(20)
	if c.ScrollX() != 10 {
		t.Errorf("ScrollX() = %d, want 10", c.ScrollX())
	}
	if c.ScrollY() != 20 {
		t.Errorf("ScrollY() = %d, want 20", c.ScrollY())
	}
}

func TestCellViewer_ScrollRelative(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.Scroll(3, 5)
	if c.ScrollX() != 3 || c.ScrollY() != 5 {
		t.Errorf("Scroll(3,5) → (%d,%d), want (3,5)", c.ScrollX(), c.ScrollY())
	}
	c.Scroll(-2, -1)
	if c.ScrollX() != 1 || c.ScrollY() != 4 {
		t.Errorf("Scroll(-2,-1) → (%d,%d), want (1,4)", c.ScrollX(), c.ScrollY())
	}
	c.Scroll(-3, -6)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("Scroll(-3,-6) → (%d,%d), want (0,0) (clamped)", c.ScrollX(), c.ScrollY())
	}
}

func TestCellViewer_PrimaryKeyDefensiveCopy(t *testing.T) {
	c := newCellViewerForTest(nil)
	orig := []any{42, "key"}
	c.Open("x", models.ColumnMeta{}, orig)
	orig[0] = 999

	pk := c.PrimaryKey()
	if pk[0] != 42 {
		t.Errorf("PrimaryKey()[0] = %v, want 42 (defensive copy)", pk[0])
	}
}

func TestCellViewer_PrimaryKeyNilSafe(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.Open("x", models.ColumnMeta{}, nil)
	if c.PrimaryKey() != nil {
		t.Error("PrimaryKey() with nil input should return nil")
	}

	c.Open("x", models.ColumnMeta{}, []any{})
	if c.PrimaryKey() != nil {
		t.Error("PrimaryKey() with empty input should return nil")
	}
}

func TestCellViewer_HandleRenderInactive(t *testing.T) {
	drv := &captureDriver{}
	c := newCellViewerForTest(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (inactive): %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times while inactive, want 0", drv.writes)
	}
}

func TestCellViewer_HandleRenderNilDriver(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.Open("hello", models.ColumnMeta{Name: "greeting", TypeName: "text"}, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestCellViewer_HandleRenderWithView(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open("hello world", models.ColumnMeta{Name: "greeting", TypeName: "text"}, nil)

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, "greeting :: text") {
		t.Errorf("output missing colname+typename: %q", buf)
	}
	if !strings.Contains(buf, "[wrap]") {
		t.Errorf("output missing [wrap]: %q", buf)
	}
	if !strings.Contains(buf, "[pretty]") {
		t.Errorf("output missing [pretty]: %q", buf)
	}
	if !strings.Contains(buf, "hello world") {
		t.Errorf("output missing body: %q", buf)
	}
}

func TestCellViewer_HandleRenderTitleNoWrap(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open("x", models.ColumnMeta{Name: "c", TypeName: "int4"}, nil)
	c.ToggleWrap()

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, "[nowrap]") {
		t.Errorf("output missing [nowrap]: %q", buf)
	}
}

func TestCellViewer_HandleRenderTitleNoPretty(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open("x", models.ColumnMeta{Name: "c", TypeName: "int4"}, nil)
	c.TogglePretty()

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, "[raw]") {
		t.Errorf("output missing [raw]: %q", buf)
	}
}

func TestCellViewer_HandleRenderNullState(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open(nil, models.ColumnMeta{Name: "c", TypeName: "text"}, nil)

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, "[ NULL ]") {
		t.Errorf("output missing [ NULL ]: %q", buf)
	}
}

func TestCellViewer_HandleRenderEmptyState(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open("", models.ColumnMeta{Name: "c", TypeName: "text"}, nil)

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, "[ empty ]") {
		t.Errorf("output missing [ empty ]: %q", buf)
	}
}

func TestCellViewer_HandleRenderEmptyBytes(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open([]byte{}, models.ColumnMeta{Name: "c", TypeName: "bytea"}, nil)

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, "[ empty ]") {
		t.Errorf("output missing [ empty ]: %q", buf)
	}
}

func TestCellViewer_WiringInTree(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree.CellViewer == nil {
		t.Fatal("tree.CellViewer is nil after NewContextTree")
	}
	if tree.CellViewer.GetKey() != types.CELL_VIEWER {
		t.Errorf("GetKey() = %q, want %q", tree.CellViewer.GetKey(), types.CELL_VIEWER)
	}
	if tree.CellViewer.GetKind() != types.PERSISTENT_POPUP {
		t.Errorf("GetKind() = %v, want PERSISTENT_POPUP", tree.CellViewer.GetKind())
	}

	found := false
	for _, c := range tree.Flatten() {
		if c == tree.CellViewer {
			found = true
			break
		}
	}
	if !found {
		t.Error("CellViewer missing from Flatten()")
	}
}

func TestCellViewer_PopupRectSpec(t *testing.T) {
	spec := PopupRectSpecFor(types.CELL_VIEWER)
	if spec.Kind != types.PopupSizeCentered {
		t.Errorf("PopupRectSpec kind = %v, want PopupSizeCentered", spec.Kind)
	}
	if spec.WidthFrac != 0.7 {
		t.Errorf("PopupRectSpec WidthFrac = %v, want 0.7", spec.WidthFrac)
	}
	if spec.HeightFrac != 0.7 {
		t.Errorf("PopupRectSpec HeightFrac = %v, want 0.7", spec.HeightFrac)
	}
}

func TestCellViewer_ColnameTypeName(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.Open("v", models.ColumnMeta{Name: "my_col", TypeName: "varchar(255)"}, nil)
	if c.Colname() != "my_col" {
		t.Errorf("Colname() = %q, want %q", c.Colname(), "my_col")
	}
	if c.Typename() != "varchar(255)" {
		t.Errorf("Typename() = %q, want %q", c.Typename(), "varchar(255)")
	}
}

func TestCellViewer_ByteCountLineCount(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.Open("hello\nworld", models.ColumnMeta{Name: "a", TypeName: "text"}, nil)
	if c.ByteCount() != 11 {
		t.Errorf("ByteCount() = %d, want 11", c.ByteCount())
	}
	if c.LineCount() != 2 {
		t.Errorf("LineCount() = %d, want 2", c.LineCount())
	}
}

func TestCellViewer_TotalWrappedLines(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))
	c.Open("line1\nline2\nline3", models.ColumnMeta{Name: "c", TypeName: "text"}, nil)

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	if c.TotalWrappedLines() != 3 {
		t.Errorf("TotalWrappedLines() = %d, want 3", c.TotalWrappedLines())
	}
}

func TestCellViewer_HandleRenderScroll(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))

	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "line"
	}
	c.Open(strings.Join(lines, "\n"), models.ColumnMeta{Name: "c", TypeName: "text"}, nil)

	c.SetScrollY(10)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	titleAndBody := strings.SplitN(buf, "\n", 2)
	if len(titleAndBody) < 2 {
		t.Fatalf("expected title + body, got lines=%d", strings.Count(buf, "\n")+1)
	}
	bodyLines := strings.Split(titleAndBody[1], "\n")
	if len(bodyLines) > 23 {
		t.Errorf("body has %d lines, want <= 23 (24 height - 1 title)", len(bodyLines))
	}
}

func TestCellViewer_HandleRenderWrapOff(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))

	longText := strings.Repeat("x", 200)
	c.Open(longText, models.ColumnMeta{Name: "c", TypeName: "text"}, nil)
	c.ToggleWrap()

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	buf := drv.GetViewBuffer(string(types.CELL_VIEWER))
	if !strings.Contains(buf, longText) {
		t.Errorf("output missing full long text with nowrap: %q", buf)
	}
}

func TestCellViewer_OpenResetsScroll(t *testing.T) {
	c := newCellViewerForTest(nil)
	c.SetScrollX(5)
	c.SetScrollY(10)
	c.Open("v", models.ColumnMeta{Name: "a"}, nil)
	if c.ScrollX() != 0 || c.ScrollY() != 0 {
		t.Errorf("scroll = (%d,%d) after Open(), want (0,0)", c.ScrollX(), c.ScrollY())
	}
}
