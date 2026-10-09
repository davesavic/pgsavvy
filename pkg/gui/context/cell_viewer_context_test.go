package context

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
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
	if !strings.Contains(buf, "hello world") {
		t.Errorf("output missing body: %q", buf)
	}

	title := c.GetTitle()
	if !strings.Contains(title, "greeting :: text") {
		t.Errorf("title missing colname+typename: %q", title)
	}
	if !strings.Contains(title, "[wrap]") {
		t.Errorf("title missing [wrap]: %q", title)
	}
	if !strings.Contains(title, "[pretty]") {
		t.Errorf("title missing [pretty]: %q", title)
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

	title := c.GetTitle()
	if !strings.Contains(title, "[nowrap]") {
		t.Errorf("title missing [nowrap]: %q", title)
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

	title := c.GetTitle()
	if !strings.Contains(title, "[raw]") {
		t.Errorf("title missing [raw]: %q", title)
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
	if !strings.Contains(buf, "(NULL)") {
		t.Errorf("output missing (NULL): %q", buf)
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
	if !strings.Contains(buf, "(empty string)") {
		t.Errorf("output missing (empty string): %q", buf)
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
	if !strings.Contains(buf, "(empty") {
		t.Errorf("output missing empty marker: %q", buf)
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
	bodyLines := strings.Split(buf, "\n")
	if len(bodyLines) > 23 {
		t.Errorf("body has %d lines, want <= 23 (24 height - 1 title)", len(bodyLines))
	}
}

func TestCellViewer_HandleRenderWrapOff(t *testing.T) {
	drv := newRecorderWithViewerViews()
	c := newCellViewerForTest(drv)
	c.SetView(drv.RealView(string(types.CELL_VIEWER)))

	longText := "prefix-" + strings.Repeat("x", 200) + "-suffix"
	c.Open(longText, models.ColumnMeta{Name: "c", TypeName: "text"}, nil)
	c.ToggleWrap()

	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	width := c.view.InnerWidth()
	if got := grid.SanitizeCellEscapes(drv.GetViewBuffer(string(types.CELL_VIEWER))); got != longText[:width] {
		t.Errorf("first nowrap page=%q, want %q", got, longText[:width])
	}
	c.SetScrollX(len(longText))
	if err := c.HandleRender(); err != nil {
		t.Fatal(err)
	}
	if got := grid.SanitizeCellEscapes(drv.GetViewBuffer(string(types.CELL_VIEWER))); got != longText[len(longText)-width:] {
		t.Errorf("last nowrap page=%q, want %q", got, longText[len(longText)-width:])
	}
	if c.OriginalValue() != longText || c.TotalWrappedLines() != 1 {
		t.Fatal("nowrap must preserve the complete underlying value as one logical line")
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

func TestCellViewerLastPageClampedBeforeRendering(t *testing.T) {
	for _, wrap := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrap=%v", wrap), func(t *testing.T) {
			drv := newRecorderWithViewerViews()
			c := newCellViewerForTest(drv)
			view := gocui.NewView(string(types.CELL_VIEWER), 0, 0, 9, 9, gocui.OutputNormal)
			c.SetView(view)
			var lines []string
			for i := range 50 {
				lines = append(lines, fmt.Sprintf("row%03d", i))
			}
			c.Open(strings.Join(lines, "\n"), models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
			if !wrap {
				c.ToggleWrap()
			}
			c.SetScrollY(49)
			if err := c.HandleRender(); err != nil {
				t.Fatal(err)
			}
			view.SetContent(drv.GetViewBuffer(string(types.CELL_VIEWER)))
			bodyHeight := view.InnerHeight() - 1
			want := strings.Join(lines[len(lines)-bodyHeight:], "\n")
			if got := strings.Join(view.ViewBufferLines(), "\n"); got != want {
				t.Errorf("last page=%q, want %q", got, want)
			}
			if c.ScrollY() != len(lines)-bodyHeight {
				t.Errorf("scroll=%d, want %d", c.ScrollY(), len(lines)-bodyHeight)
			}
		})
	}
}

func TestCellViewerHorizontalWindow(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		width, x, y      int
		wantX            int
		tabWidth         int
	}{
		{name: "ASCII middle", body: "abcdefghi", width: 4, x: 2, wantX: 2, want: "cdef"},
		{name: "left boundary", body: "abcdefghi", width: 4, x: -9, wantX: 0, want: "abcd"},
		{name: "right boundary", body: "abcdefghi", width: 4, x: 999, wantX: 5, want: "fghi"},
		{name: "CJK left partial", body: "界ab語cd", width: 4, x: 1, wantX: 1, want: " ab"},
		{name: "CJK aligned", body: "界ab語cd", width: 4, x: 2, wantX: 2, want: "ab語"},
		{name: "CJK right partial", body: "a界bcde", width: 2, want: "a"},
		{name: "CJK one column", body: "界ab", width: 1, x: 1, wantX: 1, want: " "},
		{name: "CJK right boundary", body: "界ab語cd", width: 4, x: 999, wantX: 4, want: "語cd"},
		{name: "combining cluster", body: "e\u0301abcd", width: 3, want: "e\u0301ab"},
		{name: "combining cluster skipped", body: "e\u0301abcd", width: 3, x: 1, wantX: 1, want: "abc"},
		{name: "ZWJ emoji", body: "A👩‍💻BCDE", width: 3, x: 1, wantX: 1, want: "👩‍💻B"},
		{name: "ZWJ left partial", body: "A👩‍💻BCDE", width: 3, x: 2, wantX: 2, want: " BC"},
		{name: "modifier emoji", body: "A👍🏽BCDE", width: 3, x: 1, wantX: 1, want: "👍🏽B"},
		{name: "variation selector", body: "A✈️BCDE", width: 3, x: 1, wantX: 1, want: "✈️B"},
		{name: "short line", body: "hi", width: 4, x: 9, wantX: 9, want: "hi"},
		{name: "short and empty page retains full extent", body: "abcdef\n\nabcdefghijkl\nlast", width: 4, x: 6, wantX: 6, want: "\n"},
		{name: "vertical and horizontal", body: "abcdefghijkl\nxy\n\n0123456789ab", width: 4, x: 5, y: 2, wantX: 5, want: "\n5678"},
		{name: "escapes removed before width", body: "\x1b[31mabcdef\x1b[0m", width: 4, x: 999, wantX: 2, want: "cdef"},
		{name: "ASCII tab", body: "a\tbcd", width: 5, x: 1, wantX: 1, want: "   bc"},
		{name: "CJK tab retains native stops", body: "界\tBCDE", width: 4, x: 2, wantX: 2, want: "   B"},
		{name: "custom tab stop", body: "a\tBCDE", width: 4, x: 6, wantX: 6, tabWidth: 8, want: "  BC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drv := newRecorderWithViewerViews()
			c := newCellViewerForTest(drv)
			view := gocui.NewView(string(types.CELL_VIEWER), 0, 0, tc.width+1, 4, gocui.OutputNormal)
			view.TabWidth = tc.tabWidth
			c.SetView(view)
			c.Open(tc.body, models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
			c.ToggleWrap()
			c.SetScrollX(tc.x)
			c.SetScrollY(tc.y)
			if err := c.HandleRender(); err != nil {
				t.Fatal(err)
			}
			if got := grid.SanitizeCellEscapes(drv.GetViewBuffer(string(types.CELL_VIEWER))); got != tc.want {
				t.Errorf("page=%q, want %q", got, tc.want)
			}
			if c.ScrollX() != tc.wantX {
				t.Errorf("stored X=%d, want %d", c.ScrollX(), tc.wantX)
			}
			if c.OriginalValue() != tc.body {
				t.Fatal("rendering changed the underlying cell value")
			}
		})
	}
}

func BenchmarkCellViewerLargeWindow(b *testing.B) {
	for _, wrap := range []bool{true, false} {
		b.Run(fmt.Sprintf("wrap=%v", wrap), func(b *testing.B) {
			drv := newRecorderWithViewerViews()
			c := newCellViewerForTest(drv)
			c.SetView(drv.RealView(string(types.CELL_VIEWER)))
			body := strings.Repeat("a\tb\n", 1<<16)
			c.Open(body, models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
			if !wrap {
				c.ToggleWrap()
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if err := c.HandleRender(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestCellViewerLargeWindowKeepsFullExtent(t *testing.T) {
	const lines = 1 << 12
	for _, wrap := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrap=%v", wrap), func(t *testing.T) {
			drv := newRecorderWithViewerViews()
			c := newCellViewerForTest(drv)
			c.SetView(gocui.NewView(string(types.CELL_VIEWER), 0, 0, 5, 4, gocui.OutputNormal))
			body := strings.Repeat("a\tb\n", lines) + "0123456789abcdef"
			c.Open(body, models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
			wantLines, wantBody := lines*2+4, "89ab\ncdef"
			if !wrap {
				c.ToggleWrap()
				c.SetScrollX(999)
				wantLines, wantBody = lines+1, "\ncdef"
			}
			c.SetScrollY(1 << 20)
			if err := c.HandleRender(); err != nil {
				t.Fatal(err)
			}
			if got := grid.SanitizeCellEscapes(drv.GetViewBuffer(string(types.CELL_VIEWER))); got != wantBody {
				t.Errorf("last page=%q, want %q", got, wantBody)
			}
			if c.TotalWrappedLines() != wantLines || c.ScrollY() != wantLines-2 || c.OriginalValue() != body {
				t.Errorf("extent=%d scroll=%d; want %d/%d and unchanged original", c.TotalWrappedLines(), c.ScrollY(), wantLines, wantLines-2)
			}
			if !wrap && c.ScrollX() != 12 {
				t.Errorf("full-body X bound=%d, want 12", c.ScrollX())
			}
		})
	}
}
