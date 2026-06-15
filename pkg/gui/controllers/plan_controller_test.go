package controllers

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
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

// newPlanControllerFixtureWithFinding wires a controller over an ANALYZED plan
// that yields exactly one plan-doctor finding (bad row estimate on the child),
// so insights navigation can be exercised through the controller handlers.
func newPlanControllerFixtureWithFinding(_ *testing.T) *planControllerFixture {
	child := &models.PlanNode{
		Op: "Seq Scan flagged", RelationName: "events",
		Cost: 80, EstRows: 1,
		ActualTotalTime: 8, ActualRows: 1000, Loops: 1,
	}
	root := &models.PlanNode{
		Op: "Result", Cost: 100, EstRows: 100,
		ActualTotalTime: 10, ActualRows: 100, Loops: 1,
		Children: []*models.PlanNode{child},
	}
	plan := models.Plan{Node: root, Analyzed: true, RawText: "raw"}
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
		commands.PlanToggle:         false,
		commands.PlanExpandAll:      false,
		commands.PlanCollapseAll:    false,
		commands.PlanJumpHeaviest:   false,
		commands.PlanToggleRaw:      false,
		commands.PlanToggleInsights: false,
		commands.PlanCursorDown:     false,
		commands.PlanCursorUp:       false,
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

// TestPlanController_GetKeybindings_PublishesNavigationEscape pins the
// regression fix: the PLAN master editor dispatches only under PLAN (+ GLOBAL),
// so without these bindings a plan tab traps the user — every rail-switch /
// tab-cycle / directional key FellThrough and there was no way back to a grid
// tab. PLAN must republish the same navigation chords RESULT_GRID carries
// (mirrors the rationale on ResultTabsController).
func TestPlanController_GetKeybindings_PublishesNavigationEscape(t *testing.T) {
	ctrl := NewPlanController(nil, CoreDeps{}, nil)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	want := map[string]bool{
		commands.RailSwitchSchemas:     false, // 1
		commands.RailSwitchTables:      false, // 2
		commands.RailSwitchQueryEditor: false, // 3
		commands.RailSwitchResults:     false, // 4
		commands.RailSwitchNext:        false, // <tab>
		commands.ResultTabNext:         false, // gt
		commands.ResultTabPrev:         false, // gT
	}
	for _, b := range bindings {
		if _, ok := want[b.ActionID]; ok {
			want[b.ActionID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("PLAN scope missing navigation-escape binding for %s", id)
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
	// Heat/H rank by self-cost (T3): grandchild self=15 beats Hash self=5.
	if f.pc.CursorNode().Op != "Seq Scan grand" {
		t.Errorf("cursor at %q, want Seq Scan grand", f.pc.CursorNode().Op)
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

// TestPlanController_ToggleInsights_DelegatesToContext exercises i: shows the
// strip when findings exist.
func TestPlanController_ToggleInsights_DelegatesToContext(t *testing.T) {
	f := newPlanControllerFixtureWithFinding(t)
	if err := f.ctrl.handleToggleInsights(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleToggleInsights: %v", err)
	}
	if !f.pc.InsightsActive() {
		t.Error("after handleToggleInsights, insights not active")
	}
}

// TestPlanController_InsightsNav_JKEnter pins the routing: while the strip is
// active, j/k move the strip selection and Enter jumps the tree cursor to the
// finding's node (here, expanding from the collapsed root).
func TestPlanController_InsightsNav_JKEnter(t *testing.T) {
	f := newPlanControllerFixtureWithFinding(t)
	// Collapse the root so the finding's node starts hidden.
	f.pc.Toggle()
	if len(f.pc.VisibleNodes()) != 1 {
		t.Fatalf("want root-only visible, got %d", len(f.pc.VisibleNodes()))
	}
	// Activate the strip; j/k now own navigation.
	if err := f.ctrl.handleToggleInsights(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleToggleInsights: %v", err)
	}
	// j/k stay in range over the single finding (does not touch the tree cursor).
	if err := f.ctrl.handleCursorDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleCursorDown: %v", err)
	}
	if f.pc.InsightCursor() != 0 {
		t.Errorf("strip cursor = %d, want 0 (single finding)", f.pc.InsightCursor())
	}
	// Enter jumps the tree cursor to the finding node, expanding ancestors.
	if err := f.ctrl.handleToggle(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleToggle (Enter): %v", err)
	}
	if got := f.pc.CursorNode(); got == nil || got.Op != "Seq Scan flagged" {
		t.Fatalf("Enter did not land cursor on finding node; got=%v", got)
	}
}

// TestPlanController_NoFindings_KeysDriveTree pins the empty-state contract:
// with no findings the i toggle still opens an (empty) strip for feedback, but
// it never owns navigation — j/Enter still drive the tree.
func TestPlanController_NoFindings_KeysDriveTree(t *testing.T) {
	f := newPlanControllerFixture(t) // estimate-only sample plan, no findings
	if err := f.ctrl.handleToggleInsights(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleToggleInsights: %v", err)
	}
	if !f.pc.ShowInsights() {
		t.Fatal("toggle should open the strip even with no findings (no silent no-op)")
	}
	if f.pc.InsightsActive() {
		t.Fatal("an empty strip must not own navigation")
	}
	// j drives the TREE cursor, not a strip.
	if err := f.ctrl.handleCursorDown(commands.ExecCtx{}); err != nil {
		t.Fatalf("handleCursorDown: %v", err)
	}
	if f.pc.Cursor() != 1 {
		t.Errorf("tree cursor = %d, want 1 (j drives tree with no findings)", f.pc.Cursor())
	}
}
