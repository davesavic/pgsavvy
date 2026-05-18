package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeSwapHookContext is the minimal IBaseContext used to drive the
// ContextTree in swap-hook unit tests. Only GetKey/GetKind/lifecycle
// surfaces are exercised; the rest return zero values.
type fakeSwapHookContext struct {
	key  types.ContextKey
	kind types.ContextKind
}

func (f *fakeSwapHookContext) GetKey() types.ContextKey              { return f.key }
func (f *fakeSwapHookContext) GetViewName() string                   { return string(f.key) }
func (f *fakeSwapHookContext) GetWindowName() string                 { return string(f.key) }
func (f *fakeSwapHookContext) GetKind() types.ContextKind            { return f.kind }
func (f *fakeSwapHookContext) HandleFocus(_ types.OnFocusOpts) error { return nil }
func (f *fakeSwapHookContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	return nil
}
func (f *fakeSwapHookContext) HandleRender() error                    { return nil }
func (f *fakeSwapHookContext) HandleRenderToMain() error              { return nil }
func (f *fakeSwapHookContext) HandleQuit() error                      { return nil }
func (f *fakeSwapHookContext) NeedsRerenderOnHeightChange() bool      { return false }
func (f *fakeSwapHookContext) NeedsRerenderOnWidthChange() bool       { return false }
func (f *fakeSwapHookContext) AddKeybindingsFn(_ types.KeybindingsFn) {}
func (f *fakeSwapHookContext) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return nil
}

func (f *fakeSwapHookContext) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}

func newSwapHookContext(key types.ContextKey, kind types.ContextKind) *fakeSwapHookContext {
	return &fakeSwapHookContext{key: key, kind: kind}
}

// pushKey is a small test helper that pushes a fake context with the
// supplied key onto tree. kind is SIDE_CONTEXT for the schema-rail
// keys and MAIN_CONTEXT for QUERY_EDITOR / result-tab keys; the swap
// hook itself doesn't read kind, but the ContextTree's Push routing
// does, so we pick kinds that don't wipe each other unexpectedly
// across this test's transitions (we use Replace to swap top-of-stack
// keys deterministically).
func pushKey(t *testing.T, tree *gui.ContextTree, key types.ContextKey) {
	t.Helper()
	// Replace swaps the top entry without firing pop/push lifecycle
	// hooks on neighbours but DOES fire swap hooks — perfect for
	// driving the swap-hook callback under test.
	if err := tree.Replace(newSwapHookContext(key, types.MAIN_CONTEXT)); err != nil {
		t.Fatalf("Replace(%s) returned error: %v", key, err)
	}
}

// seedTree pushes an initial context as the root so subsequent
// Replace calls have a target. Returns the tree.
func seedTree(t *testing.T, root types.ContextKey) *gui.ContextTree {
	t.Helper()
	tree := gui.NewContextTree()
	if err := tree.Push(newSwapHookContext(root, types.MAIN_CONTEXT)); err != nil {
		t.Fatalf("seed Push(%s): %v", root, err)
	}
	return tree
}

func TestIsResultPaneKey(t *testing.T) {
	cases := []struct {
		key  types.ContextKey
		want bool
	}{
		{types.QUERY_EDITOR, true},
		{types.ResultTabKey(0), true},
		{types.ResultTabKey(7), true},
		{types.ResultTabActiveKey, true}, // "result_tab_active" — matches prefix
		{types.SCHEMAS, false},
		{types.TABLES, false},
		{types.CONNECTIONS, false},
		{"", false},
		{"result_tab", false}, // missing trailing underscore
	}
	for _, tc := range cases {
		if got := isResultPaneKey(tc.key); got != tc.want {
			t.Errorf("isResultPaneKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// makeRunningTab returns a fresh helper with a single tab in
// StateRunning. The tab has no RunHandle attached, so CancelActive on
// a Running tab will flip its state to Cancelled without touching any
// driver. dbsavvy-66p.17.
func makeRunningTab(t *testing.T) (*ui.ResultTabsHelper, *ui.Tab) {
	t.Helper()
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenResultTab("SELECT 1", nil); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}
	tab := h.Active()
	if tab == nil {
		t.Fatalf("Active() returned nil after OpenResultTab")
	}
	if got := tab.State(); got != ui.StateRunning {
		t.Fatalf("seeded tab state = %v, want StateRunning", got)
	}
	return h, tab
}

func TestSwapHookBootstrapNoCancel(t *testing.T) {
	// Hook is registered, helper has a Running tab, but no swap has
	// fired YET — the very first fire is the bootstrap push of the
	// root context. prev is empty, so the hook must not cancel.
	h, tab := makeRunningTab(t)
	tree := gui.NewContextTree()
	installResultTabsSwapHook(tree, h)

	// First push: this is the bootstrap fire. prev is "" inside the
	// closure → no cancel even though current=CONNECTIONS is outside
	// the pane.
	if err := tree.Push(newSwapHookContext(types.CONNECTIONS, types.SIDE_CONTEXT)); err != nil {
		t.Fatalf("Push(CONNECTIONS): %v", err)
	}
	if tab.State() != ui.StateRunning {
		t.Fatalf("tab state after bootstrap = %v, want StateRunning (no cancel)", tab.State())
	}
}

func TestSwapHookWithinPaneNoCancel(t *testing.T) {
	h, tab := makeRunningTab(t)
	tree := seedTree(t, types.QUERY_EDITOR)
	installResultTabsSwapHook(tree, h)
	// Bootstrap: prev is "" → was=="" sets prev=QUERY_EDITOR, no cancel.
	// We need to trigger one swap so prev becomes QUERY_EDITOR.
	pushKey(t, tree, types.QUERY_EDITOR) // Replace top with same key — Replace fires hooks unconditionally
	if tab.State() != ui.StateRunning {
		t.Fatalf("after bootstrap-swap state = %v, want Running", tab.State())
	}

	// QUERY_EDITOR -> result_tab_0: within-pane, no cancel.
	pushKey(t, tree, types.ResultTabKey(0))
	if tab.State() != ui.StateRunning {
		t.Errorf("QUERY_EDITOR -> result_tab_0 cancelled tab; state = %v, want Running", tab.State())
	}
}

func TestSwapHookWithinPaneReverseNoCancel(t *testing.T) {
	h, tab := makeRunningTab(t)
	tree := seedTree(t, types.ResultTabKey(0))
	installResultTabsSwapHook(tree, h)
	pushKey(t, tree, types.ResultTabKey(0)) // settle prev

	// result_tab_0 -> QUERY_EDITOR: within-pane reverse, no cancel.
	pushKey(t, tree, types.QUERY_EDITOR)
	if tab.State() != ui.StateRunning {
		t.Errorf("result_tab_0 -> QUERY_EDITOR cancelled tab; state = %v, want Running", tab.State())
	}
}

func TestSwapHookLeavingFromEditorCancels(t *testing.T) {
	h, tab := makeRunningTab(t)
	tree := seedTree(t, types.QUERY_EDITOR)
	installResultTabsSwapHook(tree, h)
	pushKey(t, tree, types.QUERY_EDITOR) // settle prev=QUERY_EDITOR

	// QUERY_EDITOR -> SCHEMAS: leaving pane, Running tab → cancel.
	pushKey(t, tree, types.SCHEMAS)
	if tab.State() != ui.StateCancelled {
		t.Errorf("QUERY_EDITOR -> SCHEMAS did not cancel; state = %v, want Cancelled", tab.State())
	}
}

func TestSwapHookLeavingFromResultTabCancels(t *testing.T) {
	h, tab := makeRunningTab(t)
	tree := seedTree(t, types.ResultTabKey(0))
	installResultTabsSwapHook(tree, h)
	pushKey(t, tree, types.ResultTabKey(0)) // settle prev=result_tab_0

	// result_tab_0 -> SCHEMAS: leaving pane, Running tab → cancel.
	pushKey(t, tree, types.SCHEMAS)
	if tab.State() != ui.StateCancelled {
		t.Errorf("result_tab_0 -> SCHEMAS did not cancel; state = %v, want Cancelled", tab.State())
	}
}

func TestSwapHookEnteringFromOutsideNoCancel(t *testing.T) {
	h, tab := makeRunningTab(t)
	tree := seedTree(t, types.SCHEMAS)
	installResultTabsSwapHook(tree, h)
	pushKey(t, tree, types.SCHEMAS) // settle prev=SCHEMAS

	// SCHEMAS -> QUERY_EDITOR: entering pane from outside, no cancel.
	pushKey(t, tree, types.QUERY_EDITOR)
	if tab.State() != ui.StateRunning {
		t.Errorf("SCHEMAS -> QUERY_EDITOR cancelled tab; state = %v, want Running", tab.State())
	}
}

func TestSwapHookLeavingWithNonRunningActiveNoCancel(t *testing.T) {
	// Active tab is in StatePlan (not Running). Leaving the pane must
	// NOT call CancelActive — the hook's state-guard rules out every
	// non-Running state. dbsavvy-66p.17 only preempts running streams;
	// queued / complete / cancelled / plan / error tabs are untouched.
	//
	// We use StatePlan as the representative non-Running state. The
	// production-relevant case is StateQueued, but constructing a
	// Queued tab through the helper's public API requires a real (or
	// fake) *session.RunHandle, which the orchestrator package does
	// not import. StatePlan exercises the same `active.State() !=
	// StateRunning` branch with no Cancel side effect (Plan tabs have
	// no stream to cancel).
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := h.OpenPlanTab("EXPLAIN SELECT 1", models.Plan{RawText: "plan"}); err != nil {
		t.Fatalf("OpenPlanTab: %v", err)
	}
	tab := h.Active()
	if tab == nil || tab.State() != ui.StatePlan {
		t.Fatalf("seed tab state = %v, want StatePlan", tab.State())
	}

	tree := seedTree(t, types.QUERY_EDITOR)
	installResultTabsSwapHook(tree, h)
	pushKey(t, tree, types.QUERY_EDITOR) // settle prev

	pushKey(t, tree, types.SCHEMAS)
	if tab.State() != ui.StatePlan {
		t.Errorf("Plan tab state after pane-leave = %v, want StatePlan (unchanged)", tab.State())
	}
}

func TestSwapHookLeavingWithNoTabsNoCancel(t *testing.T) {
	// Helper has zero tabs → Active() returns nil → hook no-ops.
	h := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if h.Active() != nil {
		t.Fatalf("expected nil Active on empty helper")
	}

	tree := seedTree(t, types.QUERY_EDITOR)
	installResultTabsSwapHook(tree, h)
	pushKey(t, tree, types.QUERY_EDITOR) // settle prev

	// Should not panic, should not toast (no Toast wired anyway), no
	// observable effect.
	pushKey(t, tree, types.SCHEMAS)
	if h.Count() != 0 {
		t.Errorf("Count after pane-leave on empty helper = %d, want 0", h.Count())
	}
}

func TestInstallResultTabsSwapHookNilTreeIsSafe(t *testing.T) {
	// Defensive: a nil tree must not panic. The hook simply isn't
	// installed.
	installResultTabsSwapHook(nil, nil)
}

func TestSwapHookNilHelperIsSafe(t *testing.T) {
	// nil helper: leaving the pane must not panic.
	tree := seedTree(t, types.QUERY_EDITOR)
	installResultTabsSwapHook(tree, nil)
	pushKey(t, tree, types.QUERY_EDITOR) // settle prev
	pushKey(t, tree, types.SCHEMAS)
}
