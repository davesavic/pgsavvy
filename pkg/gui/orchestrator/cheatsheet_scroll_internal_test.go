package orchestrator

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// newCheatsheetScrollCtx builds a CHEATSHEET container with n tabs and a sized
// per-tab scroll store, ready to drive applyCheatsheetScroll. Leaves are nil
// (applyCheatsheetScroll never renders them).
func newCheatsheetScrollCtx(n int) *guicontext.CheatsheetContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.CHEATSHEET,
		ViewName: string(types.CHEATSHEET),
		Kind:     types.DISPLAY_CONTEXT,
	})
	ctx := guicontext.NewCheatsheetContext(base, types.ContextTreeDeps{})
	specs := make([]guicontext.TabSpec, n)
	for i := range specs {
		specs[i] = guicontext.TabSpec{Label: "Cat", LeafKey: types.CHEATSHEET}
	}
	ctx.SetTabs(specs, nil)
	return ctx
}

// cheatsheetView returns a real *gocui.View with InnerHeight==innerH populated
// with `lines` blank rows (so LinesHeight==lines).
func cheatsheetView(t *testing.T, innerH, lines int) *gocui.View {
	t.Helper()
	v := gocui.NewView(string(types.CHEATSHEET), 0, 0, 40, innerH+1, gocui.OutputNormal)
	v.SetContent(strings.Repeat("\n", max(lines-1, 0)))
	if got := v.InnerHeight(); got != innerH {
		t.Fatalf("InnerHeight = %d, want %d", got, innerH)
	}
	return v
}

// TestApplyCheatsheetScroll_PreservesOffsetOnStaleShortFrame is the regression
// guard for the per-tab-scroll-reset bug found via tmux: on a tab switch the
// shared CHEATSHEET view holds the PREVIOUS (shorter) tab's body for one frame,
// so view.LinesHeight() reports a small height and maxOY==0. The destructive
// clamp-writeback (sc.SetScrollY(maxOY)) used to zero the just-restored per-tab
// offset; the fix skips the writeback when maxOY==0. Here a context reporting
// offset 4 must KEEP it even though the view momentarily shows a 5-line body.
func TestApplyCheatsheetScroll_PreservesOffsetOnStaleShortFrame(t *testing.T) {
	ctx := newCheatsheetScrollCtx(2)
	ctx.SetScrollY(4) // active tab (0) scrolled to line 4

	// Stale frame: the shared view still carries the prior short tab's 5 lines
	// while InnerHeight (28) exceeds it -> maxOY == 0.
	v := cheatsheetView(t, 28, 5)

	applyCheatsheetScroll(v, ctx)

	if got := ctx.ScrollY(); got != 4 {
		t.Fatalf("stored ScrollY = %d, want 4 (must NOT be zeroed by a stale short-height frame)", got)
	}
	if got := v.OriginY(); got != 0 {
		t.Fatalf("display OriginY = %d, want 0 (display clamped this frame only)", got)
	}
}

// TestApplyCheatsheetScroll_PersistsClampWithRealContent proves the legitimate
// path is intact: with real scroll room (maxOY>0) the `G` bottom-sentinel still
// collapses to the last page and is written back so subsequent relative scrolls
// start from the real bottom.
func TestApplyCheatsheetScroll_PersistsClampWithRealContent(t *testing.T) {
	ctx := newCheatsheetScrollCtx(1)
	ctx.SetScrollY(1 << 20) // the `G` sentinel

	v := cheatsheetView(t, 28, 54) // tall body -> real scroll room (maxOY>0)
	wantBottom := max(v.LinesHeight()-v.InnerHeight(), 0)
	if wantBottom == 0 {
		t.Fatalf("test setup: expected real scroll room, got maxOY 0")
	}

	applyCheatsheetScroll(v, ctx)

	if got := ctx.ScrollY(); got != wantBottom {
		t.Fatalf("stored ScrollY = %d, want %d (G sentinel collapsed to last page)", got, wantBottom)
	}
	if got := v.OriginY(); got != wantBottom {
		t.Fatalf("display OriginY = %d, want %d", got, wantBottom)
	}
}
