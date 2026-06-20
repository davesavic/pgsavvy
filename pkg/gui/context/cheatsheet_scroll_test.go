package context

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newTestCheatsheet() *CheatsheetContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CHEATSHEET,
		ViewName: string(types.CHEATSHEET),
		Kind:     types.DISPLAY_CONTEXT,
	})
	return NewCheatsheetContext(base, types.ContextTreeDeps{})
}

// cheatsheetTestTabs builds n DisplayLeafContext leaves and matching specs so a
// test can install a multi-tab cheatsheet container without the orchestrator.
// The leaves render nothing meaningful (no driver in test wiring); only the
// per-tab scroll bookkeeping is exercised.
func cheatsheetTestTabs(c *CheatsheetContext, n int) ([]TabSpec, []types.IBaseContext) {
	specs := make([]TabSpec, n)
	leaves := make([]types.IBaseContext, n)
	for i := 0; i < n; i++ {
		specs[i] = TabSpec{Label: "Cat", LeafKey: types.CHEATSHEET}
		base := NewBaseContext(BaseContextOpts{
			Key:      types.CHEATSHEET,
			ViewName: c.GetViewName(),
			Kind:     types.DISPLAY_CONTEXT,
		})
		leaves[i] = NewDisplayLeafContext(base, types.ContextTreeDeps{}, c.GetViewName(), "body")
	}
	return specs, leaves
}

// TestCheatsheetScroll_PerTab asserts the vertical scroll offset is stored
// PER-TAB: scrolling tab 0 does not leak into tab 1, and returning to tab 0
// restores its prior offset (no reset on tab switch). Also asserts the top
// clamp (never negative).
func TestCheatsheetScroll_PerTab(t *testing.T) {
	c := newTestCheatsheet()
	c.SetTabs(cheatsheetTestTabs(c, 2))

	// Tab 0 starts at top.
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("fresh tab0 ScrollY() = %d, want 0", got)
	}

	c.Scroll(3)
	c.Scroll(2)
	if got := c.ScrollY(); got != 5 {
		t.Fatalf("tab0 after Scroll(+3,+2) ScrollY() = %d, want 5", got)
	}

	// Clamp at top (never negative).
	c.Scroll(-100)
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("tab0 Scroll past top ScrollY() = %d, want 0 (clamped)", got)
	}
	c.SetScrollY(8)

	// Switch to tab 1: it carries its OWN offset, starting at 0.
	c.SetActiveTab(1)
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("tab1 ScrollY() = %d, want 0 (independent of tab0)", got)
	}
	c.SetScrollY(4)
	if got := c.ScrollY(); got != 4 {
		t.Fatalf("tab1 after SetScrollY(4) = %d, want 4", got)
	}

	// Return to tab 0: its offset (8) survived the cycle — no reset on switch.
	c.SetActiveTab(0)
	if got := c.ScrollY(); got != 8 {
		t.Fatalf("tab0 ScrollY() after return = %d, want 8 (per-tab offset preserved)", got)
	}
}

// TestCheatsheetSetTabs_ZeroesEveryOffset asserts a fresh SetTabs zeroes every
// tab's scroll offset, so re-opening `?` always lands at the top on every tab.
func TestCheatsheetSetTabs_ZeroesEveryOffset(t *testing.T) {
	c := newTestCheatsheet()
	c.SetTabs(cheatsheetTestTabs(c, 2))

	c.SetScrollY(6)
	c.SetActiveTab(1)
	c.SetScrollY(9)

	// Rebuild the tabs (a fresh `?` press).
	c.SetTabs(cheatsheetTestTabs(c, 2))

	if got := c.ScrollY(); got != 0 {
		t.Fatalf("tab0 ScrollY() after fresh SetTabs = %d, want 0", got)
	}
	c.SetActiveTab(1)
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("tab1 ScrollY() after fresh SetTabs = %d, want 0", got)
	}
}

// TestCheatsheetSetTabs_ForcesManagesOwnOrigin asserts SetTabs forces
// ManagesOwnOrigin=true on every spec regardless of the caller's value, so the
// core's restoreActiveOrigin is a no-op and applyCheatsheetScroll (layout.go)
// is the sole origin writer.
func TestCheatsheetSetTabs_ForcesManagesOwnOrigin(t *testing.T) {
	c := newTestCheatsheet()
	specs, leaves := cheatsheetTestTabs(c, 2)
	for i := range specs {
		specs[i].ManagesOwnOrigin = false // caller leaves it false
	}
	c.SetTabs(specs, leaves)
	for i := range specs {
		if !specs[i].ManagesOwnOrigin {
			t.Errorf("spec[%d].ManagesOwnOrigin = false, want true (forced by SetTabs)", i)
		}
	}
}

// TestCheatsheetScroll_ShrinkingTabSetNoPanic asserts a scroll call after the
// tab set shrinks below the previously-active index does NOT panic: the
// bounds-guarded activeScroll() returns nil for an out-of-range index. (The
// core resets activeTab to 0 on SetTabs, but the guard is the safety net.)
func TestCheatsheetScroll_ShrinkingTabSetNoPanic(t *testing.T) {
	c := newTestCheatsheet()
	c.SetTabs(cheatsheetTestTabs(c, 3))
	c.SetActiveTab(2)

	// Shrink to 2 tabs, then directly drive the active index out of range to
	// exercise the bounds guard (defensive: the store is len 2, index 2 is OOB).
	c.SetTabs(cheatsheetTestTabs(c, 2))
	c.SetActiveTab(1)
	c.scroll = c.scroll[:1] // force the store shorter than the active index

	// Must not panic; guarded activeScroll() returns nil → no-op.
	c.Scroll(5)
	if got := c.ScrollY(); got != 0 {
		t.Fatalf("ScrollY() with OOB active index = %d, want 0 (guarded no-op)", got)
	}
}
