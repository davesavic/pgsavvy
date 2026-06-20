package controllers_test

import (
	"sync/atomic"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// newCheatsheetContext builds a CHEATSHEET container (composed over
// TabbedRailContext) and installs n DisplayLeafContext tabs via SetTabs so the
// controller's NextTab / PrevTab / scroll handlers have a live multi-tab target.
func newCheatsheetContext(t *testing.T, n int) *context.CheatsheetContext {
	t.Helper()
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.CHEATSHEET,
		ViewName: string(types.CHEATSHEET),
		Kind:     types.DISPLAY_CONTEXT,
	})
	c := context.NewCheatsheetContext(base, types.ContextTreeDeps{})

	specs := make([]context.TabSpec, n)
	leaves := make([]types.IBaseContext, n)
	for i := 0; i < n; i++ {
		specs[i] = context.TabSpec{Label: "Cat", LeafKey: types.CHEATSHEET}
		lb := context.NewBaseContext(context.BaseContextOpts{
			Key:      types.CHEATSHEET,
			ViewName: c.GetViewName(),
			Kind:     types.DISPLAY_CONTEXT,
		})
		leaves[i] = context.NewDisplayLeafContext(lb, types.ContextTreeDeps{}, c.GetViewName(), "body")
	}
	c.SetTabs(specs, leaves)
	return c
}

// cheatsheetFakeTree records Pop() invocations for the Close action test.
type cheatsheetFakeTree struct{ pops atomic.Int32 }

func (f *cheatsheetFakeTree) Pop() error {
	f.pops.Add(1)
	return nil
}

// TestCheatsheetController_TabCycle_PreservesPerTabScroll asserts NextTab /
// PrevTab cycle the container WITHOUT resetting scroll: scrolling tab A, moving
// to tab B (own offset), then cycling back to A restores A's prior offset. This
// proves the controller no longer zeroes scroll on tab switch.
func TestCheatsheetController_TabCycle_PreservesPerTabScroll(t *testing.T) {
	ctx := newCheatsheetContext(t, 2)
	ctrl := controllers.NewCheatsheetController(nil, controllers.CoreDeps{}, ctx, nil)

	// Scroll tab A (index 0).
	if err := ctrl.ScrollDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollDown: %v", err)
	}
	if err := ctrl.ScrollDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollDown: %v", err)
	}
	if got := ctx.ScrollY(); got != 2 {
		t.Fatalf("tabA ScrollY() = %d, want 2", got)
	}

	// NextTab -> tab B: its own offset starts at 0 (independent of A).
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("NextTab: %v", err)
	}
	if got := ctx.ActiveTab(); got != 1 {
		t.Fatalf("after NextTab ActiveTab() = %d, want 1", got)
	}
	if got := ctx.ScrollY(); got != 0 {
		t.Fatalf("tabB ScrollY() = %d, want 0 (independent per-tab offset)", got)
	}

	// PrevTab -> back to tab A: its offset (2) survived the cycle.
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("PrevTab: %v", err)
	}
	if got := ctx.ActiveTab(); got != 0 {
		t.Fatalf("after PrevTab ActiveTab() = %d, want 0", got)
	}
	if got := ctx.ScrollY(); got != 2 {
		t.Fatalf("tabA ScrollY() after A->B->A cycle = %d, want 2 (no reset on cycle)", got)
	}
}

// TestCheatsheetController_TabCycle_WrapsAround asserts NextTab wraps from the
// last tab back to the first and PrevTab wraps from the first to the last.
func TestCheatsheetController_TabCycle_WrapsAround(t *testing.T) {
	ctx := newCheatsheetContext(t, 3)
	ctrl := controllers.NewCheatsheetController(nil, controllers.CoreDeps{}, ctx, nil)

	for want := 1; want <= 2; want++ {
		if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
			t.Fatalf("NextTab: %v", err)
		}
		if got := ctx.ActiveTab(); got != want {
			t.Fatalf("NextTab ActiveTab() = %d, want %d", got, want)
		}
	}
	// Wrap: 2 -> 0.
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("NextTab: %v", err)
	}
	if got := ctx.ActiveTab(); got != 0 {
		t.Fatalf("NextTab wrap ActiveTab() = %d, want 0", got)
	}
	// PrevTab wrap: 0 -> 2.
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("PrevTab: %v", err)
	}
	if got := ctx.ActiveTab(); got != 2 {
		t.Fatalf("PrevTab wrap ActiveTab() = %d, want 2", got)
	}
}

// TestCheatsheetController_Scroll_DrivesPerTabOffset asserts the scroll handlers
// move the active tab's vertical offset and clamp at the top, and that ScrollTop
// / ScrollBottom jump to the first / last page.
func TestCheatsheetController_Scroll_DrivesPerTabOffset(t *testing.T) {
	ctx := newCheatsheetContext(t, 2)
	ctrl := controllers.NewCheatsheetController(nil, controllers.CoreDeps{}, ctx, nil)

	if err := ctrl.PageDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("PageDown: %v", err)
	}
	if got := ctx.ScrollY(); got <= 0 {
		t.Fatalf("after PageDown ScrollY() = %d, want > 0", got)
	}

	if err := ctrl.ScrollTop(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollTop: %v", err)
	}
	if got := ctx.ScrollY(); got != 0 {
		t.Fatalf("after ScrollTop ScrollY() = %d, want 0", got)
	}

	// ScrollUp past the top clamps at 0 (never negative).
	if err := ctrl.ScrollUp(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollUp: %v", err)
	}
	if got := ctx.ScrollY(); got != 0 {
		t.Fatalf("ScrollUp past top ScrollY() = %d, want 0 (clamped)", got)
	}

	// ScrollBottom installs a large sentinel the layout pass later clamps.
	if err := ctrl.ScrollBottom(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollBottom: %v", err)
	}
	if got := ctx.ScrollY(); got <= 0 {
		t.Fatalf("after ScrollBottom ScrollY() = %d, want large sentinel > 0", got)
	}
}

// TestCheatsheetController_Close_PopsStack asserts Close pops the focus stack
// exactly once.
func TestCheatsheetController_Close_PopsStack(t *testing.T) {
	tree := &cheatsheetFakeTree{}
	ctrl := controllers.NewCheatsheetController(nil, controllers.CoreDeps{}, nil, tree)

	if err := ctrl.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := tree.pops.Load(); got != 1 {
		t.Fatalf("Close popped %d times, want 1", got)
	}
}
