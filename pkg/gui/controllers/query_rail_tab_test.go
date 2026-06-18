package controllers

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// fakeQueryRailTabber is a minimal QueryRailTabber stand-in: it stores the
// active tab and clamps NOTHING (the wrap is the handler's job — this fake
// only records the value the handler computed).
type fakeQueryRailTabber struct {
	active int
}

func (f *fakeQueryRailTabber) ActiveTab() int     { return f.active }
func (f *fakeQueryRailTabber) SetActiveTab(i int) { f.active = i }

// dispatch fetches the registered handler for id and invokes it with a zero
// ExecCtx, failing the test if the action is missing.
func dispatch(t *testing.T, reg *commands.Registry, id string) {
	t.Helper()
	cmd, ok := reg.Get(id)
	if !ok {
		t.Fatalf("action %q not registered", id)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("handler %q returned error: %v", id, err)
	}
}

// TestRegisterQueryRailTabActions_WrapsAtBothEdges proves the cycle handlers
// wrap over the 3-tab QUERY_RAIL: Next from the LAST tab returns to the
// FIRST, and Prev from the FIRST tab lands on the LAST.
func TestRegisterQueryRailTabActions_WrapsAtBothEdges(t *testing.T) {
	rail := &fakeQueryRailTabber{}
	reg := commands.NewRegistry()
	RegisterQueryRailTabActions(reg, rail)

	// Next: 0 -> 1 -> 2 -> 0 (wrap forward at the last tab).
	for _, want := range []int{1, 2, 0} {
		dispatch(t, reg, commands.QueryRailTabNext)
		if rail.active != want {
			t.Fatalf("after Next: active = %d, want %d", rail.active, want)
		}
	}

	// Prev: 0 -> 2 -> 1 -> 0 (wrap backward at the first tab).
	for _, want := range []int{2, 1, 0} {
		dispatch(t, reg, commands.QueryRailTabPrev)
		if rail.active != want {
			t.Fatalf("after Prev: active = %d, want %d", rail.active, want)
		}
	}
}

// TestRegisterQueryRailTabActions_NilRailIsNoOp confirms the handlers stay
// no-ops (no panic) when the container is nil.
func TestRegisterQueryRailTabActions_NilRailIsNoOp(t *testing.T) {
	reg := commands.NewRegistry()
	RegisterQueryRailTabActions(reg, nil)
	dispatch(t, reg, commands.QueryRailTabNext)
	dispatch(t, reg, commands.QueryRailTabPrev)
}

// TestQueryEditorPublishesTabCycleBindings asserts the QUERY_EDITOR leaf
// publishes the `]`/`[` cycle pair under its OWN scope, Normal mode only
// (never Insert — that would hijack literal brackets in SQL).
func TestQueryEditorPublishesTabCycleBindings(t *testing.T) {
	c := NewQueryEditorController(nil, CoreDeps{}, NavDeps{}, UIDeps{}, QueryDeps{}, ThreadingDeps{})
	assertTabCycleBindings(t, c.GetKeybindings(types.KeybindingsOpts{}), types.QUERY_EDITOR)
}

// TestSavedQueryPublishesTabCycleBindings asserts the SAVED_QUERY leaf
// publishes the cycle pair under its OWN scope.
func TestSavedQueryPublishesTabCycleBindings(t *testing.T) {
	ctx := guicontext.NewSavedQueryContext(
		guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.SAVED_QUERY, ViewName: string(types.SAVED_QUERY)}),
		guicontext.Deps{},
	)
	c := NewSavedQueryController(nil, CoreDeps{}, UIDeps{}, ctx, nil, nil, nil, "")
	assertTabCycleBindings(t, c.GetKeybindings(types.KeybindingsOpts{}), types.SAVED_QUERY)
}

// assertTabCycleBindings checks bindings contains exactly one `]`→Next and
// one `[`→Prev, both under wantScope + Normal mode + ShowInBar.
func assertTabCycleBindings(t *testing.T, bindings []*types.ChordBinding, wantScope types.ContextKey) {
	t.Helper()
	type hit struct {
		key   rune
		id    string
		mode  types.Mode
		scope types.ContextKey
		inBar bool
	}
	var next, prev *hit
	for _, b := range bindings {
		if len(b.Sequence) != 1 {
			continue
		}
		k := b.Sequence[0].Code
		if k != ']' && k != '[' {
			continue
		}
		h := &hit{key: k, id: b.ActionID, mode: b.Mode, scope: b.Scope, inBar: b.ShowInBar}
		if k == ']' {
			next = h
		} else {
			prev = h
		}
	}
	if next == nil || prev == nil {
		t.Fatalf("missing `]` or `[` binding (next=%v prev=%v)", next, prev)
	}
	for _, h := range []*hit{next, prev} {
		if h.scope != wantScope {
			t.Errorf("`%c` scope = %s, want %s", h.key, h.scope, wantScope)
		}
		if h.mode != types.ModeNormal {
			t.Errorf("`%c` mode = %v, want Normal (Insert would hijack literal brackets)", h.key, h.mode)
		}
		if !h.inBar {
			t.Errorf("`%c` ShowInBar = false, want true", h.key)
		}
	}
	if next.id != commands.QueryRailTabNext {
		t.Errorf("`]` action = %q, want %q", next.id, commands.QueryRailTabNext)
	}
	if prev.id != commands.QueryRailTabPrev {
		t.Errorf("`[` action = %q, want %q", prev.id, commands.QueryRailTabPrev)
	}
}
