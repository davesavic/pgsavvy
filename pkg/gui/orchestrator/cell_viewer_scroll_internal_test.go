package orchestrator

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// testCellViewerScroller satisfies cellViewerScroller and types.IBaseContext
// (via embedded BaseContext) with controllable scroll/line state.
type testCellViewerScroller struct {
	guicontext.BaseContext
	scrollY           int
	totalWrappedLines int
}

func (t *testCellViewerScroller) ScrollY() int          { return t.scrollY }
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
	wantBottom := max(sc.TotalWrappedLines()-v.InnerHeight(), 0)
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
