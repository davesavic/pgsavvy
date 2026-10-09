package orchestrator

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// testCellViewerScroller satisfies cellViewerScroller and types.IBaseContext
// (via embedded BaseContext) with controllable scroll/line state.
type testCellViewerScroller struct {
	guicontext.BaseContext
	scrollY           int
	totalWrappedLines int
}

func (t *testCellViewerScroller) ScrollY() int           { return t.scrollY }
func (t *testCellViewerScroller) SetScrollY(y int)       { t.scrollY = y }
func (t *testCellViewerScroller) TotalWrappedLines() int { return t.totalWrappedLines }

// cellViewerView returns a real *gocui.View with InnerHeight==innerH holding
// `lines` blank rows.
func cellViewerView(t *testing.T, innerH, lines int) *gocui.View {
	t.Helper()
	v := gocui.NewView(string(types.CELL_VIEWER), 0, 0, 40, innerH+1, gocui.OutputNormal)
	v.SetContent(strings.Repeat("\n", max(lines-1, 0)))
	if got := v.InnerHeight(); got != innerH {
		t.Fatalf("InnerHeight = %d, want %d", got, innerH)
	}
	return v
}

// TestApplyCellViewerScroll_PreservesOffsetOnStaleShortFrame is the
// regression guard for the stale-short-frame bug: when the view momentarily
// holds a shorter body than the stored scroll offset (e.g. after a resize),
// the clamp must NOT zero the stored offset.
func TestApplyCellViewerScroll_PreservesOffsetOnStaleShortFrame(t *testing.T) {
	sc := &testCellViewerScroller{
		BaseContext:       guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.CELL_VIEWER, Kind: types.PERSISTENT_POPUP}),
		scrollY:           4,
		totalWrappedLines: 5,
	}
	v := cellViewerView(t, 28, 5)

	applyCellViewerScroll(v, sc)

	if got := sc.ScrollY(); got != 4 {
		t.Fatalf("stored ScrollY = %d, want 4 (must NOT be zeroed by a stale short-height frame)", got)
	}
	if oy := v.OriginY(); oy != 0 {
		t.Fatalf("display origin Y = %d, want 0 (display clamped this frame only)", oy)
	}
}

// TestApplyCellViewerScroll_PersistsClampWithG proves the legitimate path:
// with real scroll room the `G` sentinel collapses to the last page and
// is written back.
func TestApplyCellViewerScroll_PersistsClampWithG(t *testing.T) {
	sc := &testCellViewerScroller{
		BaseContext:       guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.CELL_VIEWER, Kind: types.PERSISTENT_POPUP}),
		scrollY:           1 << 20,
		totalWrappedLines: 54,
	}
	v := cellViewerView(t, 28, 54)
	wantBottom := max(sc.TotalWrappedLines()-(v.InnerHeight()-1), 0)
	if wantBottom == 0 {
		t.Fatalf("test setup: expected real scroll room, got maxOY 0")
	}

	applyCellViewerScroll(v, sc)

	if got := sc.ScrollY(); got != wantBottom {
		t.Fatalf("stored ScrollY = %d, want %d (G sentinel collapsed to last page)", got, wantBottom)
	}
}

// TestApplyCellViewerScroll_WrapModeZeroClamp verifies that with zero
// wrapped content (no real scroll room), a non-zero offset is displayed at
// zero but the stored offset is NOT zeroed (stale-frame guard, same
// invariant as the cheatsheet and table_inspect scroll tests).
func TestApplyCellViewerScroll_WrapModeZeroClamp(t *testing.T) {
	sc := &testCellViewerScroller{
		BaseContext:       guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.CELL_VIEWER, Kind: types.PERSISTENT_POPUP}),
		scrollY:           10,
		totalWrappedLines: 0,
	}
	v := cellViewerView(t, 28, 20)

	applyCellViewerScroll(v, sc)

	if got := sc.ScrollY(); got != 10 {
		t.Fatalf("stored ScrollY = %d, want 10 (must NOT zero with maxOY==0)", got)
	}
	if oy := v.OriginY(); oy != 0 {
		t.Fatalf("display origin Y = %d, want 0 (clamped to display-only zero)", oy)
	}
}

func TestCellViewerNativeVerticalWindow(t *testing.T) {
	for _, wrap := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrap=%v", wrap), func(t *testing.T) {
			drv := testfake.NewRecorderGuiDriver()
			ctx := guicontext.NewCellViewerContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{
				Key: types.CELL_VIEWER, ViewName: string(types.CELL_VIEWER), Kind: types.PERSISTENT_POPUP,
			}), guicontext.Deps{GuiDriver: drv})
			var source []string
			for i := range 40 {
				source = append(source, fmt.Sprintf("a%03db%03d", i, i))
			}
			ctx.Open(strings.Join(source, "\n"), models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
			if !wrap {
				ctx.ToggleWrap()
			}
			ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, ctx, nil, nil)
			registry := commands.NewRegistry()
			ctrl.RegisterActions(registry)
			width, height, wantScroll := 4, 8, 0
			for _, step := range []struct {
				name, action  string
				width, height int
			}{
				{name: "initial"},
				{name: "j once", action: "scroll_down"},
				{name: "j twice", action: "scroll_down"},
				{name: "j thrice", action: "scroll_down"},
				{name: "page down", action: "page_down"},
				{name: "page up", action: "page_up"},
				{name: "G", action: "jump_bottom"},
				{name: "grow height", height: 20},
				{name: "shrink height", height: 4},
				{name: "grow width", width: 8},
				{name: "shrink width", width: 4},
				{name: "temporarily no scroll room", height: 200},
				{name: "restore height", height: 8},
				{name: "gg", action: "jump_top"},
			} {
				t.Run(step.name, func(t *testing.T) {
					if step.width != 0 {
						width = step.width
					}
					if step.height != 0 {
						height = step.height
					}
					var allLines []string
					for _, line := range source {
						if wrap && width == 4 {
							allLines = append(allLines, line[:4], line[4:])
						} else {
							allLines = append(allLines, line)
						}
					}
					if step.action != "" {
						cmd, ok := registry.Get("cell_viewer." + step.action)
						if !ok {
							t.Fatalf("missing action %s", step.action)
						}
						if err := cmd.Handler(commands.ExecCtx{}); err != nil {
							t.Fatal(err)
						}
						switch step.action {
						case "scroll_down":
							wantScroll++
						case "page_down":
							wantScroll += 24 // Preserve the controller's existing page motion.
						case "page_up":
							wantScroll = max(wantScroll-24, 0)
						case "jump_bottom":
							wantScroll = len(allLines) - 1
						case "jump_top":
							wantScroll = 0
						}
					}
					bodyHeight := height - 1
					maxScroll := max(len(allLines)-bodyHeight, 0)
					start := min(wantScroll, maxScroll)
					if maxScroll > 0 {
						wantScroll = start
					}
					view := gocui.NewView(string(types.CELL_VIEWER), 0, 0, width+1, height+1, gocui.OutputNormal)
					view.Wrap = false
					view.SetOriginY(5) // A reused native view can retain an old origin.
					drv.SetRealView(string(types.CELL_VIEWER), view)
					ctx.SetView(view)
					if err := ctx.HandleRender(); err != nil {
						t.Fatal(err)
					}
					view.SetContent(drv.GetViewBuffer(string(types.CELL_VIEWER)))
					applyCellViewerScroll(view, ctx)
					lines := view.ViewBufferLines()
					if len(lines) == 0 {
						t.Fatal("native view has no lines")
					}
					// Match gocui's draw start: origin is relative to the rendered
					// buffer, not the complete original cell.
					nativeStart := min(view.OriginY(), len(lines)-1)
					got := lines[nativeStart:min(nativeStart+bodyHeight, len(lines))]
					want := allLines[start:min(start+bodyHeight, len(allLines))]
					if !wrap {
						want = append([]string(nil), want...)
						for i, line := range want {
							want[i] = line[:min(width, len(line))]
						}
					}
					if !reflect.DeepEqual(got, want) || view.OriginY() != 0 || ctx.ScrollY() != wantScroll {
						t.Errorf("page=%v origin=%d scroll=%d; want %v origin=0 scroll=%d", got, view.OriginY(), ctx.ScrollY(), want, wantScroll)
					}
				})
			}
		})
	}
}

func TestCellViewerHorizontalDrawnCells(t *testing.T) {
	type drawnCell struct {
		text  string
		width int
	}
	for _, tc := range []struct {
		name, body string
		width, x   int
		want       []drawnCell
		wrap       bool
		secondRow  []drawnCell
	}{
		{name: "ASCII", body: "abcdefghi", width: 3, x: 2, want: []drawnCell{{"c", 1}, {"d", 1}, {"e", 1}}},
		{name: "CJK aligned", body: "A界BCDE", width: 3, x: 1, want: []drawnCell{{"界", 2}, {"B", 1}}},
		{name: "CJK left partial", body: "A界BCDE", width: 3, x: 2, want: []drawnCell{{" ", 1}, {"B", 1}, {"C", 1}}},
		{name: "CJK right partial", body: "A界BCDE", width: 2, want: []drawnCell{{"A", 1}, {" ", 1}}},
		{name: "combining", body: "e\u0301abcde", width: 2, want: []drawnCell{{"e\u0301", 1}, {"a", 1}}},
		{name: "ZWJ", body: "A👩‍💻BCDE", width: 3, x: 1, want: []drawnCell{{"👩‍💻", 2}, {"B", 1}}},
		{name: "modifier", body: "A👍🏽BCDE", width: 3, x: 1, want: []drawnCell{{"👍🏽", 2}, {"B", 1}}},
		{name: "VS16", body: "A☺️BCDE", width: 3, x: 1, want: []drawnCell{{"☺️", 2}, {"B", 1}}},
		{name: "VS15 aligned", body: "A⌛︎BCDE", width: 3, x: 1, want: []drawnCell{{"⌛︎", 2}, {"B", 1}}},
		{name: "VS15 left partial", body: "A⌛︎BCDE", width: 3, x: 2, want: []drawnCell{{" ", 1}, {"B", 1}, {"C", 1}}},
		{name: "VS15 right partial", body: "A⌛︎BCDE", width: 2, want: []drawnCell{{"A", 1}, {" ", 1}}},
		{name: "VS15 one column", body: "⌛︎x", width: 1, want: []drawnCell{{" ", 1}}},
		{name: "VS15 exact fit", body: "⌛︎x", width: 2, want: []drawnCell{{"⌛︎", 2}}},
		{name: "VS15 followed by x", body: "⌛︎x", width: 3, want: []drawnCell{{"⌛︎", 2}, {"x", 1}}},
		{name: "VS15 adjacent", body: "⌛︎⌛︎x", width: 5, want: []drawnCell{{"⌛︎", 2}, {"⌛︎", 2}, {"x", 1}}},
		{name: "VS15 highlighted", body: `"⌛︎x"`, width: 5, want: []drawnCell{{`"`, 1}, {"⌛︎", 2}, {"x", 1}, {`"`, 1}}},
		{name: "VS15 wrap exact fit", body: "⌛︎x", width: 2, wrap: true, want: []drawnCell{{"⌛︎", 2}}, secondRow: []drawnCell{{"x", 1}, {" ", 1}}},
		{name: "VS15 wrap one column", body: "⌛︎x", width: 1, wrap: true, want: []drawnCell{{" ", 1}}, secondRow: []drawnCell{{"x", 1}}},
		{name: "ZWJ wrap whole cluster", body: "A👩‍💻B", width: 2, wrap: true, want: []drawnCell{{"A", 1}, {" ", 1}}, secondRow: []drawnCell{{"👩‍💻", 2}}},
		{name: "CJK tab", body: "界\tBCDE", width: 4, x: 2, want: []drawnCell{{" ", 1}, {" ", 1}, {" ", 1}, {"B", 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gui := newCellViewerHeadlessGUI(t, 30, 10)
			ctx := guicontext.NewCellViewerContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{
				Key: types.CELL_VIEWER, ViewName: string(types.CELL_VIEWER), Kind: types.PERSISTENT_POPUP,
			}), guicontext.Deps{GuiDriver: &cellViewerHeadlessDriver{gui: gui}})
			ctx.Open(tc.body, models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
			if !tc.wrap {
				ctx.ToggleWrap()
			}
			ctx.SetScrollX(tc.x)
			gui.SetManagerFunc(func(g *gocui.Gui) error {
				view, err := g.SetView(string(types.CELL_VIEWER), 0, 0, tc.width+1, 4, 0)
				if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
					return err
				}
				view.Wrap = false
				view.SetOrigin(7, 5)
				ctx.SetView(view)
				if err := ctx.HandleRender(); err != nil {
					return err
				}
				applyCellViewerScroll(view, ctx)
				if x, y := view.Origin(); x != 0 || y != 0 {
					t.Errorf("native origin=(%d,%d), want (0,0)", x, y)
				}
				return nil
			})
			if err := gui.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			for row, cells := range [][]drawnCell{tc.want, tc.secondRow} {
				x := 1
				for _, want := range cells {
					got, _, width := gocui.Screen.Get(x, row+1)
					if got != want.text || width != want.width {
						t.Errorf("screen(%d,%d)=(%q,%d), want (%q,%d)", x, row+1, got, width, want.text, want.width)
					}
					if x-1+width > tc.width {
						t.Error("grapheme extends beyond the viewport")
					}
					x += want.width
				}
			}
			if got, _, width := gocui.Screen.Get(tc.width+1, 1); got != "│" || width != 1 {
				t.Errorf("right border=(%q,%d), want intact vertical border", got, width)
			}
		})
	}
}

func TestCellViewerHorizontalMotionAndResize(t *testing.T) {
	gui := newCellViewerHeadlessGUI(t, 40, 10)
	ctx := guicontext.NewCellViewerContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key: types.CELL_VIEWER, ViewName: string(types.CELL_VIEWER), Kind: types.PERSISTENT_POPUP,
	}), guicontext.Deps{GuiDriver: &cellViewerHeadlessDriver{gui: gui}})
	ctx.Open("abcdefg\nxy\n\n0123456789abcdef", models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
	ctx.ToggleWrap()
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, ctx, nil, nil)
	registry := commands.NewRegistry()
	ctrl.RegisterActions(registry)
	width := 4
	gui.SetManagerFunc(func(g *gocui.Gui) error {
		view, err := g.SetView(string(types.CELL_VIEWER), 0, 0, width+1, 4, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		view.Wrap = false
		view.SetOrigin(3, 5)
		ctx.SetView(view)
		if err := ctx.HandleRender(); err != nil {
			return err
		}
		applyCellViewerScroll(view, ctx)
		return nil
	})
	for _, step := range []struct {
		name, action        string
		count, width, x, y  int
		firstRow, secondRow string
	}{
		{name: "initial", firstRow: "abcd", secondRow: "xy  "},
		{name: "l reveals next column", action: "scroll_right", x: 1, firstRow: "bcde", secondRow: "y   "},
		{name: "h restores first column", action: "scroll_left", firstRow: "abcd", secondRow: "xy  "},
		{name: "widest row outside page", action: "scroll_right", count: 6, x: 6, firstRow: "g   ", secondRow: "    "},
		{name: "vertical to short and empty", action: "scroll_down", x: 6, y: 1, firstRow: "    ", secondRow: "    "},
		{name: "vertical to widest", action: "scroll_down", x: 6, y: 2, firstRow: "    ", secondRow: "6789"},
		{name: "right boundary", action: "scroll_right", count: 100, x: 12, y: 2, firstRow: "    ", secondRow: "cdef"},
		{name: "left from boundary", action: "scroll_left", x: 11, y: 2, firstRow: "    ", secondRow: "bcde"},
		{name: "wrap ignores X", action: "toggle_wrap", x: 11, y: 2, firstRow: "xy  ", secondRow: "    "},
		{name: "wrapped l no-op", action: "scroll_right", x: 11, y: 2, firstRow: "xy  ", secondRow: "    "},
		{name: "nowrap restores X", action: "toggle_wrap", x: 11, y: 2, firstRow: "    ", secondRow: "bcde"},
		{name: "temporarily no horizontal room", width: 20, x: 11, y: 2, firstRow: "                    ", secondRow: "0123456789abcdef    "},
		{name: "resize clamps last column", width: 6, x: 10, y: 2, firstRow: "      ", secondRow: "abcdef"},
		{name: "left boundary", action: "scroll_left", count: 100, y: 2, firstRow: "      ", secondRow: "012345"},
		{name: "top", action: "jump_top", firstRow: "abcdef", secondRow: "xy    "},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.width > 0 {
				width = step.width
			}
			if step.action != "" {
				cmd, ok := registry.Get("cell_viewer." + step.action)
				if !ok {
					t.Fatalf("missing action %s", step.action)
				}
				for range max(step.count, 1) {
					if err := cmd.Handler(commands.ExecCtx{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := gui.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			for row, want := range []string{step.firstRow, step.secondRow} {
				var got strings.Builder
				for x := 1; x <= width; x++ {
					text, _, cellWidth := gocui.Screen.Get(x, row+1)
					if cellWidth != 1 {
						t.Errorf("ASCII fixture contains width %d cell", cellWidth)
					}
					got.WriteString(text)
				}
				if got.String() != want {
					t.Errorf("row %d=%q, want %q", row, got.String(), want)
				}
			}
			if ctx.ScrollX() != step.x || ctx.ScrollY() != step.y {
				t.Errorf("stored offset=(%d,%d), want (%d,%d)", ctx.ScrollX(), ctx.ScrollY(), step.x, step.y)
			}
		})
	}
}

func TestCellViewerVS15WidthAcrossRedraws(t *testing.T) {
	gui := newCellViewerHeadlessGUI(t, 20, 8)
	ctx := guicontext.NewCellViewerContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key: types.CELL_VIEWER, ViewName: string(types.CELL_VIEWER), Kind: types.PERSISTENT_POPUP,
	}), guicontext.Deps{GuiDriver: &cellViewerHeadlessDriver{gui: gui}})
	ctx.Open("⌛︎x", models.ColumnMeta{Name: "value", TypeName: "text"}, nil)
	width := 3
	gui.SetManagerFunc(func(g *gocui.Gui) error {
		view, err := g.SetView(string(types.CELL_VIEWER), 0, 0, width+1, 4, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		view.Wrap = false
		ctx.SetView(view)
		if err := ctx.HandleRender(); err != nil {
			return err
		}
		applyCellViewerScroll(view, ctx)
		return nil
	})
	for _, nextWidth := range []int{3, 2, 1, 2, 3} {
		width = nextWidth
		for range 2 {
			ctx.ToggleWrap()
			if err := gui.ForceLayoutAndRedraw(); err != nil {
				t.Fatal(err)
			}
			wantText, wantWidth := "⌛︎", 2
			if width == 1 {
				wantText, wantWidth = " ", 1
			}
			if got, _, gotWidth := gocui.Screen.Get(1, 1); got != wantText || gotWidth != wantWidth {
				t.Errorf("viewport=%d wrap=%v first cell=(%q,%d), want (%q,%d)", width, ctx.Wrap(), got, gotWidth, wantText, wantWidth)
			}
			if width == 3 {
				if got, _, gotWidth := gocui.Screen.Get(3, 1); got != "x" || gotWidth != 1 {
					t.Errorf("suffix=(%q,%d), want (x,1)", got, gotWidth)
				}
			}
			if got, _, gotWidth := gocui.Screen.Get(width+1, 1); got != "│" || gotWidth != 1 {
				t.Errorf("viewport=%d border=(%q,%d), want intact border", width, got, gotWidth)
			}
		}
	}
}

type cellViewerHeadlessDriver struct {
	types.GuiDriver
	gui *gocui.Gui
}

func newCellViewerHeadlessGUI(t *testing.T, width, height int) *gocui.Gui {
	t.Helper()
	previous := gocui.Screen
	gui, err := gocui.NewGui(gocui.NewGuiOpts{OutputMode: gocui.OutputNormal, Headless: true, Width: width, Height: height})
	if err != nil {
		gocui.Screen = previous
		t.Fatal(err)
	}
	t.Cleanup(func() {
		gui.Close()
		// Close leaves the global pointing at a finalized screen; subsequent
		// recorder-based layout tests can otherwise call Sync on that screen.
		gocui.Screen = previous
	})
	return gui
}

func (d *cellViewerHeadlessDriver) SetContent(name, content string) error {
	view, err := d.gui.View(name)
	if err != nil {
		return err
	}
	view.SetContent(content)
	return nil
}

func (*cellViewerHeadlessDriver) Update(fn func() error) { _ = fn() }
