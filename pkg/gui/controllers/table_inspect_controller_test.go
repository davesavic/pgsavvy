package controllers_test

import (
	"sync/atomic"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// newInspectContext builds a TABLE_INSPECT context (composed over
// TabbedRailContext with the two fixed Columns/Indexes tabs) so NextTab /
// PrevTab can advance. The `n` parameter is retained for call-site stability; the
// composed context always has exactly two tabs.
func newInspectContext(t *testing.T, _ int) *context.TableInspectContext {
	t.Helper()
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.TABLE_INSPECT,
		ViewName: context.TableInspectViewName,
		Kind:     types.TEMPORARY_POPUP,
		Title:    "Table inspect",
	})
	return context.NewTableInspectContext(base, types.ContextTreeDeps{})
}

// fakeTree records Pop() invocations for the Close action test.
type fakeTree struct{ pops atomic.Int32 }

func (f *fakeTree) Pop() error {
	f.pops.Add(1)
	return nil
}

func TestTableInspectController_GetKeybindings_AllNormalScoped(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})
	// 5 tab/close + 12 scroll (j/down, k/up, h/left, l/right, c-d, c-u, gg, G).
	if got, want := len(kbs), 17; got != want {
		t.Fatalf("len(GetKeybindings()) = %d, want %d", got, want)
	}
	for i, kb := range kbs {
		if kb.Scope != types.TABLE_INSPECT {
			t.Errorf("kbs[%d].Scope = %q, want %q", i, kb.Scope, types.TABLE_INSPECT)
		}
		if kb.Mode != types.ModeNormal {
			t.Errorf("kbs[%d].Mode = %v, want ModeNormal", i, kb.Mode)
		}
	}
}

func TestTableInspectController_VerticalScroll_MovesOrigin(t *testing.T) {
	ic := newInspectContext(t, 2)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)

	if err := ctrl.ScrollDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollDown: %v", err)
	}
	if got := ic.ScrollY(); got != 1 {
		t.Errorf("after ScrollDown ScrollY() = %d, want 1", got)
	}
	if err := ctrl.ScrollUp(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollUp: %v", err)
	}
	if got := ic.ScrollY(); got != 0 {
		t.Errorf("after ScrollUp ScrollY() = %d, want 0", got)
	}
	// ScrollUp past the top clamps at 0, never negative.
	if err := ctrl.ScrollUp(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollUp: %v", err)
	}
	if got := ic.ScrollY(); got != 0 {
		t.Errorf("ScrollUp past top ScrollY() = %d, want 0 (clamped)", got)
	}
}

func TestTableInspectController_HorizontalScroll_MovesOrigin(t *testing.T) {
	ic := newInspectContext(t, 1)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)

	if err := ctrl.ScrollRight(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollRight: %v", err)
	}
	if got := ic.ScrollX(); got <= 0 {
		t.Errorf("after ScrollRight ScrollX() = %d, want > 0", got)
	}
	if err := ctrl.ScrollLeft(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollLeft: %v", err)
	}
	if got := ic.ScrollX(); got != 0 {
		t.Errorf("after ScrollLeft ScrollX() = %d, want 0", got)
	}
}

func TestTableInspectController_TabChange_PreservesPerTabScroll(t *testing.T) {
	ic := newInspectContext(t, 2)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)
	// Tab 0 scrolled; switching to tab 1 lands at (0,0) (independent store), and
	// switching back restores tab 0's offset (per-tab scroll, not reset).
	ic.SetScrollX(20)
	ic.SetScrollY(5)
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("NextTab: %v", err)
	}
	if x, y := ic.ScrollX(), ic.ScrollY(); x != 0 || y != 0 {
		t.Errorf("tab1 scroll = (%d,%d), want (0,0) (independent)", x, y)
	}
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("PrevTab: %v", err)
	}
	if x, y := ic.ScrollX(), ic.ScrollY(); x != 20 || y != 5 {
		t.Errorf("tab0 scroll after switch-back = (%d,%d), want (20,5)", x, y)
	}
}

// TestTableInspectController_GetKeybindings_NoShiftTabBinding asserts no
// binding maps a Shift+Tab chord. gocui has no Backtab; AMD-5b explicitly
// dropped the <S-tab> binding for this scope.
func TestTableInspectController_GetKeybindings_NoShiftTabBinding(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Special == types.KeyTab && k.Mod&types.ChordModShift != 0 {
			t.Fatalf("found <S-tab> binding (ActionID=%q); AMD-5b forbids it", kb.ActionID)
		}
	}
}

func TestTableInspectController_GetKeybindings_ActionIDs(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	counts := map[string]int{}
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		counts[kb.ActionID]++
	}
	if counts[commands.TableInspectNextTab] != 2 {
		t.Errorf("TableInspectNextTab bindings = %d, want 2 (Tab + ])", counts[commands.TableInspectNextTab])
	}
	if counts[commands.TableInspectPrevTab] != 1 {
		t.Errorf("TableInspectPrevTab bindings = %d, want 1 ([)", counts[commands.TableInspectPrevTab])
	}
	if counts[commands.TableInspectClose] != 2 {
		t.Errorf("TableInspectClose bindings = %d, want 2 (Esc + q)", counts[commands.TableInspectClose])
	}
}

func TestTableInspectController_NextTabAction_AdvancesActiveTab(t *testing.T) {
	ic := newInspectContext(t, 2)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)
	if got := ic.ActiveTab(); got != 0 {
		t.Fatalf("pre-NextTab ActiveTab() = %d, want 0", got)
	}
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("NextTab: %v", err)
	}
	if got := ic.ActiveTab(); got != 1 {
		t.Errorf("post-NextTab ActiveTab() = %d, want 1", got)
	}
	// Wrap-around: NextTab from the last tab returns to 0.
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("NextTab (wrap): %v", err)
	}
	if got := ic.ActiveTab(); got != 0 {
		t.Errorf("post-NextTab wrap ActiveTab() = %d, want 0", got)
	}
}

func TestTableInspectController_PrevTabAction_RewindsActiveTab(t *testing.T) {
	ic := newInspectContext(t, 2)
	ic.SetActiveTab(1)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("PrevTab: %v", err)
	}
	if got := ic.ActiveTab(); got != 0 {
		t.Errorf("post-PrevTab ActiveTab() = %d, want 0", got)
	}
	// Wrap-around: PrevTab from tab 0 goes to the last tab (1).
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("PrevTab (wrap): %v", err)
	}
	if got := ic.ActiveTab(); got != 1 {
		t.Errorf("post-PrevTab wrap ActiveTab() = %d, want 1", got)
	}
}

func TestTableInspectController_CloseAction_PopsContext(t *testing.T) {
	tree := &fakeTree{}
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, tree)
	if err := ctrl.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := tree.pops.Load(); got != 1 {
		t.Errorf("tree.Pop calls = %d, want 1", got)
	}
}

func TestTableInspectController_NextPrevAction_NoStateNoPanic(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Errorf("NextTab nil ctx: %v", err)
	}
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Errorf("PrevTab nil ctx: %v", err)
	}
	if err := ctrl.Close(commands.ExecCtx{}); err != nil {
		t.Errorf("Close nil tree: %v", err)
	}
}
