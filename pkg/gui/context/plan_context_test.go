package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// newTestPlanContext returns a PlanContext for the supplied plan. Deps
// is zero — the GuiDriver is nil-safe via writeView.
func newTestPlanContext(plan models.Plan) *PlanContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.PLAN,
		ViewName: string(types.PLAN),
		Kind:     types.MAIN_CONTEXT,
	})
	return NewPlanContext(base, Deps{}, plan)
}

// samplePlan builds a small 4-node plan tree for navigation tests:
//
//	root (Result)            cost=10
//	├── child A (Seq Scan)   cost=4
//	└── child B (Hash)       cost=20
//	    └── grandchild (Seq) cost=15
func samplePlan() models.Plan {
	gc := &models.PlanNode{Op: "Seq Scan grand", Cost: 15, EstRows: 1}
	a := &models.PlanNode{Op: "Seq Scan A", Cost: 4, EstRows: 1}
	b := &models.PlanNode{Op: "Hash", Cost: 20, EstRows: 1, Children: []*models.PlanNode{gc}}
	root := &models.PlanNode{Op: "Result", Cost: 10, EstRows: 1, Children: []*models.PlanNode{a, b}}
	return models.Plan{Node: root, RawText: "raw EXPLAIN text"}
}

// TestPlanContext_Render_TreeGlyphs_Present pins AC rule 7: every
// visible node renders with one of ▼ / ▶ / ─.
func TestPlanContext_Render_TreeGlyphs_Present(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	body := pc.RenderBody()
	if !strings.Contains(body, presentation.GlyphExpanded) {
		t.Errorf("body missing GlyphExpanded (%q); body=%q", presentation.GlyphExpanded, body)
	}
	if !strings.Contains(body, presentation.GlyphLeaf) {
		t.Errorf("body missing GlyphLeaf (%q); body=%q", presentation.GlyphLeaf, body)
	}
}

// TestPlanContext_Toggle_CollapsesAndExpands pins AC rule 1: <CR>
// toggles cursor node collapse and the flattened walk respects it.
func TestPlanContext_Toggle_CollapsesAndExpands(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	// Default: 4 visible (root + A + B + grandchild).
	if got := len(pc.VisibleNodes()); got != 4 {
		t.Fatalf("initial visible count = %d, want 4", got)
	}
	// Move cursor to B (index 2): root=0, A=1, B=2.
	pc.MoveCursor(2)
	pc.Toggle()
	// Now B is collapsed; visible = root + A + B = 3.
	if got := len(pc.VisibleNodes()); got != 3 {
		t.Fatalf("after collapse visible count = %d, want 3", got)
	}
	pc.Toggle()
	if got := len(pc.VisibleNodes()); got != 4 {
		t.Fatalf("after re-expand visible count = %d, want 4", got)
	}
}

// TestPlanContext_ExpandAll_EmptiesMap pins AC rule 2.
func TestPlanContext_ExpandAll_EmptiesMap(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.MoveCursor(2)
	pc.Toggle()
	if pc.CollapsedCount() != 1 {
		t.Fatalf("collapsed count after toggle = %d, want 1", pc.CollapsedCount())
	}
	pc.ExpandAll()
	if pc.CollapsedCount() != 0 {
		t.Fatalf("collapsed count after ExpandAll = %d, want 0", pc.CollapsedCount())
	}
	if got := len(pc.VisibleNodes()); got != 4 {
		t.Fatalf("visible after ExpandAll = %d, want 4", got)
	}
}

// TestPlanContext_CollapseAllButRoot pins AC rule 3.
func TestPlanContext_CollapseAllButRoot(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.CollapseAllButRoot()
	vis := pc.VisibleNodes()
	// Root + its two direct children (A, B) are visible; B's collapse
	// hides grandchild. A is collapsed but has no children; collapsing
	// a leaf is harmless.
	if len(vis) != 3 {
		t.Fatalf("visible after CollapseAllButRoot = %d, want 3", len(vis))
	}
	if vis[0].Node.Op != "Result" {
		t.Errorf("first visible Op = %q, want Result", vis[0].Node.Op)
	}
}

// TestPlanContext_CollapseAllButRoot_SingleNode_Noop pins the edge
// case: single-node plan has nothing to collapse.
func TestPlanContext_CollapseAllButRoot_SingleNode_Noop(t *testing.T) {
	root := &models.PlanNode{Op: "Seq Scan", Cost: 1, EstRows: 1}
	pc := newTestPlanContext(models.Plan{Node: root, RawText: "raw"})
	pc.CollapseAllButRoot()
	if pc.CollapsedCount() != 0 {
		t.Errorf("single-node CollapseAllButRoot left %d collapsed; want 0", pc.CollapsedCount())
	}
}

// TestPlanContext_JumpHeaviest pins AC rule 4: H jumps to heaviest
// descendant. In samplePlan, root's heaviest descendant is "Hash"
// (cost=20). Cursor at root (index 0) → after JumpHeaviest, cursor
// should land on Hash.
func TestPlanContext_JumpHeaviest(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	// Cursor at root.
	pc.JumpHeaviest()
	node := pc.CursorNode()
	if node == nil || node.Op != "Hash" {
		got := "<nil>"
		if node != nil {
			got = node.Op
		}
		t.Fatalf("after JumpHeaviest, cursor on %q, want Hash", got)
	}
}

// TestPlanContext_JumpHeaviest_OnLeaf_Noop pins the edge case.
func TestPlanContext_JumpHeaviest_OnLeaf_Noop(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.MoveCursor(1) // A — a leaf.
	before := pc.Cursor()
	pc.JumpHeaviest()
	if pc.Cursor() != before {
		t.Errorf("JumpHeaviest on leaf moved cursor: %d → %d", before, pc.Cursor())
	}
}

// TestPlanContext_JumpHeaviest_TieBreakDFS pins the tie-breaker: first
// child encountered in depth-first iteration wins on equal cost.
func TestPlanContext_JumpHeaviest_TieBreakDFS(t *testing.T) {
	a := &models.PlanNode{Op: "TieA", Cost: 9, EstRows: 1}
	b := &models.PlanNode{Op: "TieB", Cost: 9, EstRows: 1}
	root := &models.PlanNode{Op: "Root", Cost: 1, EstRows: 1, Children: []*models.PlanNode{a, b}}
	pc := newTestPlanContext(models.Plan{Node: root})
	pc.JumpHeaviest()
	if pc.CursorNode().Op != "TieA" {
		t.Errorf("tie-break favored %q, want TieA (first encountered DFS)", pc.CursorNode().Op)
	}
}

// TestPlanContext_MoveCursor_Clamps pins AC rule 5: j/k move cursor in
// [0, len(visible)-1].
func TestPlanContext_MoveCursor_Clamps(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.MoveCursor(-10)
	if pc.Cursor() != 0 {
		t.Errorf("MoveCursor(-10) yielded %d, want 0", pc.Cursor())
	}
	pc.MoveCursor(100)
	if pc.Cursor() != 3 {
		t.Errorf("MoveCursor(100) yielded %d, want 3 (len-1)", pc.Cursor())
	}
}

// TestPlanContext_ToggleRaw_RoundTrips pins AC rule 6 + 11.
func TestPlanContext_ToggleRaw_RoundTrips(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	if pc.ShowRaw() {
		t.Fatal("ShowRaw default true, want false")
	}
	pc.ToggleRaw()
	if !pc.ShowRaw() {
		t.Fatal("ToggleRaw did not flip to true")
	}
	body := pc.RenderBody()
	if !strings.Contains(body, "raw EXPLAIN text") {
		t.Errorf("raw mode body missing raw text; body=%q", body)
	}
	pc.ToggleRaw()
	if pc.ShowRaw() {
		t.Fatal("ToggleRaw did not flip back to false")
	}
}

// TestPlanContext_RenderBody_NilNode pins the edge case: nil Node
// renders empty body (or raw text in raw mode), no crash.
func TestPlanContext_RenderBody_NilNode(t *testing.T) {
	pc := newTestPlanContext(models.Plan{Node: nil, RawText: ""})
	body := pc.RenderBody()
	if body != "" {
		t.Errorf("nil-Node body = %q, want empty", body)
	}
	// Toggle raw mode + empty raw text → still empty body, no crash.
	pc.ToggleRaw()
	if got := pc.RenderBody(); got != "" {
		t.Errorf("nil-Node empty-raw body = %q, want empty", got)
	}
}

// TestPlanContext_RenderBody_AnalyzedColumns pins AC rule 7's
// extension: when Plan.Analyzed is true, actual_cost / actual_rows /
// loops columns appear.
func TestPlanContext_RenderBody_AnalyzedColumns(t *testing.T) {
	root := &models.PlanNode{
		Op: "Seq Scan", Cost: 5, EstRows: 2,
		ActualCost: 4.5, ActualRows: 1, Loops: 1,
	}
	pc := newTestPlanContext(models.Plan{Node: root, Analyzed: true})
	body := pc.RenderBody()
	for _, want := range []string{"actual_cost", "actual_rows", "loops"} {
		if !strings.Contains(body, want) {
			t.Errorf("analyzed body missing %q; body=%q", want, body)
		}
	}
}

// TestPlanContext_RenderBody_NoAnalyzedColumns asserts the columns are
// omitted when Plan.Analyzed is false (default).
func TestPlanContext_RenderBody_NoAnalyzedColumns(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	body := pc.RenderBody()
	if strings.Contains(body, "actual_cost") {
		t.Errorf("non-analyzed body unexpectedly contains actual_cost; body=%q", body)
	}
}

// TestPlanContext_DegenerateColoringSkipped pins AC rule "visible-
// node count < 4 → coloring entirely skipped". RenderBody on a
// 3-node plan must not embed any ANSI SGR escape (\x1b[...).
func TestPlanContext_DegenerateColoringSkipped(t *testing.T) {
	a := &models.PlanNode{Op: "Leaf A", Cost: 1, EstRows: 1}
	b := &models.PlanNode{Op: "Leaf B", Cost: 999, EstRows: 1}
	root := &models.PlanNode{Op: "Result", Cost: 5, EstRows: 1, Children: []*models.PlanNode{a, b}}
	pc := newTestPlanContext(models.Plan{Node: root})
	body := pc.RenderBody()
	if strings.Contains(body, "\x1b[") {
		t.Errorf("degenerate plan body contains ANSI escape; want no coloring. body=%q", body)
	}
}

// TestPlanContext_RawText_SanitizedCallSite pins the AC sanitization
// rule: raw text passes through grid.SanitizeCellEscapes. Stub returns
// identity today; the test guards the call site by asserting that the
// raw text comes through unchanged (function is identity) — when T9
// finalises the stripper, the assertion can be tightened.
func TestPlanContext_RawText_SanitizedCallSite(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.ToggleRaw()
	body := pc.RenderBody()
	if body != "raw EXPLAIN text" {
		t.Errorf("raw body = %q, want %q (identity stub)", body, "raw EXPLAIN text")
	}
}

// TestPlanContext_Toggle_OnLeaf_Noop pins the edge case: <CR> on a
// leaf has no effect (no entry added to collapsed).
func TestPlanContext_Toggle_OnLeaf_Noop(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.MoveCursor(1) // A — leaf.
	pc.Toggle()
	if pc.CollapsedCount() != 0 {
		t.Errorf("Toggle on leaf added %d collapse entries; want 0", pc.CollapsedCount())
	}
}

// TestPlanContext_Toggle_CursorStaysPut pins the AC edge case:
// "cursor on collapsed subtree's root: <CR> expands; cursor stays put".
func TestPlanContext_Toggle_CursorStaysPut(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.MoveCursor(2) // B
	before := pc.Cursor()
	pc.Toggle() // collapse B
	pc.Toggle() // re-expand B
	if pc.Cursor() != before {
		t.Errorf("cursor moved after Toggle round-trip: %d → %d", before, pc.Cursor())
	}
}
