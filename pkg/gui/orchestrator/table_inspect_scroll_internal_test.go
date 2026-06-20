package orchestrator

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// newTableInspectScrollCtx builds a TABLE_INSPECT container. Its constructor
// sizes the per-tab scroll store to the (fixed) tab count, so ScrollX/Y are
// ready to drive applyTableInspectScroll.
func newTableInspectScrollCtx() *guicontext.TableInspectContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.TABLE_INSPECT,
		ViewName: string(types.TABLE_INSPECT),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewTableInspectContext(base, types.ContextTreeDeps{})
}

// tableInspectView returns a real *gocui.View with InnerHeight==innerH holding
// `lines` blank rows (LinesHeight==lines, narrow content -> maxOX 0).
func tableInspectView(t *testing.T, innerH, lines int) *gocui.View {
	t.Helper()
	v := gocui.NewView(string(types.TABLE_INSPECT), 0, 0, 40, innerH+1, gocui.OutputNormal)
	v.SetContent(strings.Repeat("\n", max(lines-1, 0)))
	if got := v.InnerHeight(); got != innerH {
		t.Fatalf("InnerHeight = %d, want %d", got, innerH)
	}
	return v
}

// TestApplyTableInspectScroll_PreservesOffsetOnStaleShortFrame is the
// regression guard for the per-tab-scroll-reset bug shared with the cheatsheet:
// on a tab switch the shared TABLE_INSPECT view holds the PREVIOUS (shorter)
// tab's body for one frame, so view extents read small and maxO*==0. The
// destructive clamp-writeback must NOT zero the just-restored per-tab offsets.
func TestApplyTableInspectScroll_PreservesOffsetOnStaleShortFrame(t *testing.T) {
	ctx := newTableInspectScrollCtx()
	ctx.SetScrollY(4) // active tab scrolled down 4 lines
	ctx.SetScrollX(3) // ...and right 3 columns

	// Stale frame: view momentarily carries a short/narrow 5-line body.
	v := tableInspectView(t, 28, 5)

	applyTableInspectScroll(v, ctx)

	if got := ctx.ScrollY(); got != 4 {
		t.Fatalf("stored ScrollY = %d, want 4 (must NOT be zeroed by a stale short-height frame)", got)
	}
	if got := ctx.ScrollX(); got != 3 {
		t.Fatalf("stored ScrollX = %d, want 3 (must NOT be zeroed by a stale narrow frame)", got)
	}
	if ox, oy := v.OriginX(), v.OriginY(); ox != 0 || oy != 0 {
		t.Fatalf("display origin = (%d,%d), want (0,0) (display clamped this frame only)", ox, oy)
	}
}

// TestApplyTableInspectScroll_PersistsClampWithRealContent proves the
// legitimate path is intact: with real vertical scroll room the `G` sentinel
// still collapses to the last page and is written back.
func TestApplyTableInspectScroll_PersistsClampWithRealContent(t *testing.T) {
	ctx := newTableInspectScrollCtx()
	ctx.SetScrollY(1 << 20) // the `G` sentinel

	v := tableInspectView(t, 28, 54) // tall body -> real scroll room
	wantBottom := max(v.LinesHeight()-v.InnerHeight(), 0)
	if wantBottom == 0 {
		t.Fatalf("test setup: expected real scroll room, got maxOY 0")
	}

	applyTableInspectScroll(v, ctx)

	if got := ctx.ScrollY(); got != wantBottom {
		t.Fatalf("stored ScrollY = %d, want %d (G sentinel collapsed to last page)", got, wantBottom)
	}
}
