package orchestrator

import (
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
					if !reflect.DeepEqual(got, want) || view.OriginY() != 0 || ctx.ScrollY() != wantScroll {
						t.Errorf("page=%v origin=%d scroll=%d; want %v origin=0 scroll=%d", got, view.OriginY(), ctx.ScrollY(), want, wantScroll)
					}
				})
			}
		})
	}
}
