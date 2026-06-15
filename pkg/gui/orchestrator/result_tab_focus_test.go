package orchestrator_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestTabCycleRepointsFocusToActiveTab: with a
// grid "results" tab and a "plan analyzer" tab open, cycling between them
// while the result pane holds focus must move gocui's current-view (driven
// off tree.Current().GetViewName()) onto the now-active tab. Before the fix
// Cycle only mutated the helper's activeID, so the focus stack stayed on
// the prior tab and leader chords dispatched under the stale PLAN scope.
func TestTabCycleRepointsFocusToActiveTab(t *testing.T) {
	g, _ := buildTestGui(t)
	h := g.ResultTabsHelper()
	tree := g.ContextTree()

	if err := h.OpenResultTab("results", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	if err := h.OpenPlanTab("plan", models.Plan{}); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	// Enter the result pane on the (active) plan tab, as a rail switch does.
	if err := tree.Push(h.ActiveContext()); err != nil {
		t.Fatalf("Push active: %v", err)
	}
	if got := tree.Current().GetKey(); got != types.PLAN {
		t.Fatalf("precondition: focus key = %q, want PLAN", got)
	}

	// gt -> cycle to the grid tab. Focus must follow onto its view.
	h.Cycle(1)

	if got := tree.Current().GetKey(); got != types.RESULT_GRID {
		t.Fatalf("after Cycle: focus key = %q, want RESULT_GRID (stale plan scope)", got)
	}
	if got, want := tree.Current().GetViewName(), h.ActiveContext().GetViewName(); got != want {
		t.Fatalf("after Cycle: focus view = %q, want active tab view %q", got, want)
	}
}

// TestTabJumpDoesNotStealFocusWhenPaneUnfocused guards the asymmetry:
// <leader>1..9 jump bindings are GLOBAL-scoped and can fire
// from the query editor or a rail. A jump must switch the visible tab
// (activeID) but must NOT hijack the focus stack onto the result pane.
func TestTabJumpDoesNotStealFocusWhenPaneUnfocused(t *testing.T) {
	g, _ := buildTestGui(t)
	h := g.ResultTabsHelper()
	tree := g.ContextTree()

	if err := h.OpenResultTab("a", nil); err != nil {
		t.Fatalf("OpenResultTab a: %v", err)
	}
	if err := h.OpenResultTab("b", nil); err != nil {
		t.Fatalf("OpenResultTab b: %v", err)
	}

	// Focus is on whatever buildTestGui rooted (a non-result context);
	// the result pane is NOT focused.
	before := tree.Current()
	if k := before.GetKey(); k == types.RESULT_GRID || k == types.PLAN {
		t.Fatalf("precondition: root focus = %q, want a non-result context", k)
	}

	h.Jump(1) // switch visible tab from b -> a

	if got := h.Active(); got == nil || got.Label() != "a" {
		t.Fatalf("Jump did not switch active tab: %v", got)
	}
	if tree.Current() != before {
		t.Fatalf("Jump stole focus: top = %q, want unchanged %q",
			tree.Current().GetKey(), before.GetKey())
	}
}
