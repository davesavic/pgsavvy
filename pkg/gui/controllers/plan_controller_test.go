package controllers

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// planControllerFixture wires a PlanController with a resolvable
// PlanContext over a small plan tree.
type planControllerFixture struct {
	ctrl *PlanController
	pc   *context.PlanContext
}

func newPlanControllerFixture(_ *testing.T) *planControllerFixture {
	gc := &models.PlanNode{Op: "Seq Scan grand", Cost: 15, EstRows: 1}
	a := &models.PlanNode{Op: "Seq Scan A", Cost: 4, EstRows: 1}
	b := &models.PlanNode{Op: "Hash", Cost: 20, EstRows: 1, Children: []*models.PlanNode{gc}}
	root := &models.PlanNode{Op: "Result", Cost: 10, EstRows: 1, Children: []*models.PlanNode{a, b}}
	plan := models.Plan{Node: root, RawText: "raw"}
	pc := context.NewPlanContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.PLAN,
			ViewName: string(types.PLAN),
			Kind:     types.MAIN_CONTEXT,
		}),
		context.Deps{},
		plan,
	)
	ctrl := NewPlanController(nil, CoreDeps{}, func() *context.PlanContext { return pc })
	return &planControllerFixture{ctrl: ctrl, pc: pc}
}

// TestPlanController_GetKeybindings_Scope confirms every binding is
// PLAN-scoped — AC requirement "<C-a> cannot leak into grid-filter
// context".
func TestPlanController_GetKeybindings_Scope(t *testing.T) {
	ctrl := NewPlanController(nil, CoreDeps{}, nil)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	if len(bindings) == 0 {
		t.Fatal("GetKeybindings returned no bindings")
	}
	for _, b := range bindings {
		if b.Scope != types.PLAN {
			t.Errorf("binding %s has Scope %s, want PLAN", b.ActionID, b.Scope)
		}
	}
}

// TestPlanController_GetKeybindings_CoversAllActions confirms we emit
// bindings for every Plan action ID.
func TestPlanController_GetKeybindings_CoversAllActions(t *testing.T) {
	ctrl := NewPlanController(nil, CoreDeps{}, nil)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	want := map[string]bool{
		commands.PlanToggle:       false,
		commands.PlanExpandAll:    false,
		commands.PlanCollapseAll:  false,
		commands.PlanJumpHeaviest: false,
		commands.PlanToggleRaw:    false,
		commands.PlanCursorDown:   false,
		commands.PlanCursorUp:     false,
	}
	for _, b := range bindings {
		if _, ok := want[b.ActionID]; ok {
			want[b.ActionID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("binding for action %s missing", id)
		}
	}
}

// TestPlanController_Toggle_DelegatesToContext exercises the <CR>
// dispatch path.
func TestPlanController_Toggle_DelegatesToContext(t *testing.T) {
	f := newPlanControllerFixture(t)
	f.pc.MoveCursor(2) // B
	if err := f.ctrl.handleToggle(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleToggle: %v", err)
	}
	if !f.pc.IsCollapsed(f.pc.CursorNode()) {
		t.Error("after handleToggle, cursor node not collapsed")
	}
}

// TestPlanController_ExpandAll_DelegatesToContext exercises <C-a>.
func TestPlanController_ExpandAll_DelegatesToContext(t *testing.T) {
	f := newPlanControllerFixture(t)
	f.pc.MoveCursor(2)
	f.pc.Toggle() // collapse B
	if err := f.ctrl.handleExpandAll(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleExpandAll: %v", err)
	}
	if f.pc.CollapsedCount() != 0 {
		t.Errorf("after handleExpandAll, %d nodes collapsed; want 0", f.pc.CollapsedCount())
	}
}

// TestPlanController_CollapseAll_DelegatesToContext exercises <C-x>.
func TestPlanController_CollapseAll_DelegatesToContext(t *testing.T) {
	f := newPlanControllerFixture(t)
	if err := f.ctrl.handleCollapseAll(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleCollapseAll: %v", err)
	}
	if len(f.pc.VisibleNodes()) != 3 {
		t.Errorf("after handleCollapseAll, %d visible; want 3", len(f.pc.VisibleNodes()))
	}
}

// TestPlanController_JumpHeaviest_DelegatesToContext exercises H.
func TestPlanController_JumpHeaviest_DelegatesToContext(t *testing.T) {
	f := newPlanControllerFixture(t)
	if err := f.ctrl.handleJumpHeaviest(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleJumpHeaviest: %v", err)
	}
	if f.pc.CursorNode().Op != "Hash" {
		t.Errorf("cursor at %q, want Hash", f.pc.CursorNode().Op)
	}
}

// TestPlanController_ToggleRaw_DelegatesToContext exercises o.
func TestPlanController_ToggleRaw_DelegatesToContext(t *testing.T) {
	f := newPlanControllerFixture(t)
	if err := f.ctrl.handleToggleRaw(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleToggleRaw: %v", err)
	}
	if !f.pc.ShowRaw() {
		t.Error("after handleToggleRaw, ShowRaw still false")
	}
}

// TestPlanController_CursorDown_Up_Delegate exercises j/k.
func TestPlanController_CursorDown_Up_Delegate(t *testing.T) {
	f := newPlanControllerFixture(t)
	if err := f.ctrl.handleCursorDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleCursorDown: %v", err)
	}
	if f.pc.Cursor() != 1 {
		t.Errorf("cursor = %d, want 1 after CursorDown", f.pc.Cursor())
	}
	if err := f.ctrl.handleCursorUp(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleCursorUp: %v", err)
	}
	if f.pc.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0 after CursorUp", f.pc.Cursor())
	}
}

// TestPlanController_NilResolver_NoOps exercises the no-op path when
// no plan tab is active.
func TestPlanController_NilResolver_NoOps(t *testing.T) {
	ctrl := NewPlanController(nil, CoreDeps{}, nil)
	if err := ctrl.handleToggle(commands.ExecCtx{}); err != nil {
		t.Errorf("handleToggle on nil resolver returned err: %v", err)
	}
	if err := ctrl.handleExpandAll(commands.ExecCtx{}); err != nil {
		t.Errorf("handleExpandAll on nil resolver returned err: %v", err)
	}
}

// TestPlanController_ResolverReturnsNil_NoOps exercises the no-op path
// when the resolver returns nil (e.g. focused tab isn't a plan tab).
func TestPlanController_ResolverReturnsNil_NoOps(t *testing.T) {
	ctrl := NewPlanController(nil, CoreDeps{}, func() *context.PlanContext { return nil })
	if err := ctrl.handleToggle(commands.ExecCtx{}); err != nil {
		t.Errorf("handleToggle on nil-returning resolver: %v", err)
	}
}
