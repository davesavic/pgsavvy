package context

import (
	"regexp"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query/plandoctor"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// newTestPlanContext returns a PlanContext for the supplied plan. Deps
// is zero — the GuiDriver is nil-safe via writeView.
func newTestPlanContext(plan models.Plan) *PlanContext {
	// Hand-built test plans skip the parser, so trigger the derived-field
	// computation here — else the GUI heat/JumpHeaviest logic sees all-zero
	// Self* fields. Mirrors parsePlanJSON calling ComputeDerived.
	plan.ComputeDerived()
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

// TestPlanContext_JumpHeaviest pins AC rule 4 under the SELF-basis: H
// jumps to the descendant with the highest exclusive (self) magnitude,
// not the highest inclusive Cost. samplePlan is estimate-only so the
// basis is SelfCost:
//
//	root "Result" SelfCost = 10 - (4+20)  = -14 (clamped 0)
//	A    "Seq Scan A"      SelfCost = 4
//	B    "Hash"            SelfCost = 20 - 15 = 5
//	gc   "Seq Scan grand"  SelfCost = 15  ← winner
//
// The old inclusive winner was "Hash" (Cost=20); under self-cost the
// grandchild "Seq Scan grand" (SelfCost=15) is the real bottleneck.
func TestPlanContext_JumpHeaviest(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	// Cursor at root.
	pc.JumpHeaviest()
	node := pc.CursorNode()
	if node == nil || node.Op != "Seq Scan grand" {
		got := "<nil>"
		if node != nil {
			got = node.Op
		}
		t.Fatalf("after JumpHeaviest, cursor on %q, want Seq Scan grand", got)
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
// extension: when Plan.Analyzed is true, actual_time / actual_rows /
// loops columns appear. The actual_time column renders ActualTotalTime
// (the real ANALYZE timing); the DEAD ActualCost (always 0) is no
// longer displayed.
func TestPlanContext_RenderBody_AnalyzedColumns(t *testing.T) {
	root := &models.PlanNode{
		Op: "Seq Scan", Cost: 5, EstRows: 2,
		ActualTotalTime: 4.5, ActualRows: 1, Loops: 1,
	}
	pc := newTestPlanContext(models.Plan{Node: root, Analyzed: true})
	body := pc.RenderBody()
	for _, want := range []string{"actual_time", "actual_rows", "loops"} {
		if !strings.Contains(body, want) {
			t.Errorf("analyzed body missing %q; body=%q", want, body)
		}
	}
	if strings.Contains(body, "actual_cost") {
		t.Errorf("analyzed body still renders dead actual_cost; body=%q", body)
	}
	if !strings.Contains(body, "actual_time=4.5") {
		t.Errorf("analyzed body missing actual_time=4.5 (ActualTotalTime); body=%q", body)
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

// TestPlanContext_DegenerateColoringSkipped pins the rule "visible-node
// count < MinVisibleForColoring → heat coloring entirely skipped". A
// single-node plan is too degenerate to bucket (the lone node would always
// land in the top percentile), so RenderBody must embed no ANSI SGR escape.
func TestPlanContext_DegenerateColoringSkipped(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	root := &models.PlanNode{Op: "Result", Cost: 5, EstRows: 1}
	pc := newTestPlanContext(models.Plan{Node: root})
	body := pc.RenderBody()
	if strings.Contains(body, "\x1b[") {
		t.Errorf("single-node plan body contains ANSI escape; want no coloring. body=%q", body)
	}
}

// TestPlanContext_SmallPlanTints pins that a 2-node plan (the smallest
// non-degenerate case) DOES get heat coloring now that the gate is lowered.
// This is the case that previously rendered fully monochrome.
func TestPlanContext_SmallPlanTints(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	child := &models.PlanNode{Op: "SeqScan", Cost: 999, EstRows: 1}
	root := &models.PlanNode{Op: "Result", Cost: 1, EstRows: 1, Children: []*models.PlanNode{child}}
	pc := newTestPlanContext(models.Plan{Node: root})
	body := pc.RenderBody()
	if !strings.Contains(body, "\x1b[") {
		t.Errorf("2-node plan body has no ANSI escape; want heat coloring. body=%q", body)
	}
}

// TestPlanContext_WholeRowHeatColored pins that the heat color wraps the
// WHOLE row (glyph + op name + metrics), not just the cost token. The
// hottest node's bold-red escape must appear BEFORE its op name in the line.
func TestPlanContext_WholeRowHeatColored(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	leaf := &models.PlanNode{Op: "DeepLeaf", Cost: 5, EstRows: 1, ActualTotalTime: 90, Loops: 1}
	mid := &models.PlanNode{Op: "Mid", Cost: 8, EstRows: 1, ActualTotalTime: 99, Loops: 1, Children: []*models.PlanNode{leaf}}
	sib1 := &models.PlanNode{Op: "Sib1", Cost: 2, EstRows: 1, ActualTotalTime: 1, Loops: 1}
	sib2 := &models.PlanNode{Op: "Sib2", Cost: 2, EstRows: 1, ActualTotalTime: 1, Loops: 1}
	root := &models.PlanNode{
		Op: "Root", Cost: 20, EstRows: 1, ActualTotalTime: 102, Loops: 1,
		Children: []*models.PlanNode{mid, sib1, sib2},
	}
	pc := newTestPlanContext(models.Plan{Node: root, Analyzed: true})
	body := pc.RenderBody()

	var line string
	for ln := range strings.SplitSeq(body, "\n") {
		if strings.Contains(ln, "DeepLeaf") {
			line = ln
			break
		}
	}
	if line == "" {
		t.Fatalf("no DeepLeaf line in body=%q", body)
	}
	escIdx := strings.Index(line, "\x1b[1;31m")
	opIdx := strings.Index(line, "DeepLeaf")
	if escIdx < 0 {
		t.Fatalf("DeepLeaf line carries no bold-red escape; line=%q", line)
	}
	if escIdx > opIdx {
		t.Errorf("bold-red escape (%d) comes after op name (%d): heat is not whole-row; line=%q", escIdx, opIdx, line)
	}
}

// TestPlanContext_SeverityGlyphColored_Strip pins that the INSIGHTS strip
// colors each finding by severity. An Info finding must carry cyan (\x1b[36m),
// a color the heat buckets never emit.
func TestPlanContext_SeverityGlyphColored_Strip(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	pc := newTestPlanContext(samplePlan())
	pc.findings = []plandoctor.Finding{{Severity: plandoctor.SeverityInfo, Title: "Heads up", Explanation: "fyi"}}
	pc.showInsights = true
	body := pc.RenderBody()
	if !strings.Contains(body, "\x1b[36m") {
		t.Errorf("insights strip has no cyan severity color for an Info finding; body=%q", body)
	}
}

// TestPlanContext_SeverityGlyphColored_Badge pins that the inline per-row
// badge colors by severity even when the strip is off.
func TestPlanContext_SeverityGlyphColored_Badge(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	root := &models.PlanNode{Op: "Result", Cost: 5, EstRows: 1}
	pc := newTestPlanContext(models.Plan{Node: root})
	f := plandoctor.Finding{NodeRef: root, Severity: plandoctor.SeverityInfo, Title: "Heads up"}
	pc.findingByNode = map[*models.PlanNode]plandoctor.Finding{root: f}
	body := pc.RenderBody()
	if !strings.Contains(body, "\x1b[36m") {
		t.Errorf("inline badge has no cyan severity color for an Info finding; body=%q", body)
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

// boldRedOps returns the set of node Ops whose rendered line carries the
// bold-red escape (\x1b[1;31m = P95 / hottest bucket). The percentile
// bucketing can place several near-tied nodes in the top bucket; what
// matters for these tests is that the real self-hog IS in the set and the
// trivial-self root is NOT.
func boldRedOps(body string) map[string]bool {
	const bred = "\x1b[1;31m"
	out := map[string]bool{}
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.Contains(line, bred) {
			continue
		}
		for _, g := range []string{presentation.GlyphLeaf, presentation.GlyphExpanded, presentation.GlyphCollapsed} {
			if _, after, ok := strings.Cut(line, g); ok {
				rest := strings.TrimSpace(after)
				if before, _, ok := strings.Cut(rest, "  cost="); ok {
					out[strings.TrimSpace(before)] = true
				}
				break
			}
		}
	}
	return out
}

// TestPlanContext_Heat_AnalyzedDeepChildHottest is the headline scenario:
// an ANALYZE plan whose ROOT has huge inclusive total time but trivial
// self-time, with a deep child of high self-time. The deep child must be
// the reddest node; the root must NOT be hottest.
//
// Wall-time budget (ms, all Loops=1). Several low-self siblings spread the
// percentile distribution so the trivial-self root falls below P95 while
// the deep leaf alone sits in the bold-red bucket:
//
//	root  total=100, children total ~99 → SelfTime≈1  (trivial)
//	mid   total=99,  child total 90     → SelfTime=9
//	leaf  total=90                      → SelfTime=90 (the real hog)
//	sib1..sib3 small self-times
func TestPlanContext_Heat_AnalyzedDeepChildHottest(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	leaf := &models.PlanNode{Op: "DeepLeaf", Cost: 5, EstRows: 1, ActualTotalTime: 90, Loops: 1}
	mid := &models.PlanNode{Op: "Mid", Cost: 8, EstRows: 1, ActualTotalTime: 99, Loops: 1, Children: []*models.PlanNode{leaf}}
	sib1 := &models.PlanNode{Op: "Sib1", Cost: 2, EstRows: 1, ActualTotalTime: 1, Loops: 1}
	sib2 := &models.PlanNode{Op: "Sib2", Cost: 2, EstRows: 1, ActualTotalTime: 1, Loops: 1}
	sib3 := &models.PlanNode{Op: "Sib3", Cost: 2, EstRows: 1, ActualTotalTime: 1, Loops: 1}
	root := &models.PlanNode{
		Op: "Root", Cost: 20, EstRows: 1, ActualTotalTime: 102, Loops: 1,
		Children: []*models.PlanNode{mid, sib1, sib2, sib3},
	}
	pc := newTestPlanContext(models.Plan{Node: root, Analyzed: true})
	body := pc.RenderBody()
	hot := boldRedOps(body)
	if !hot["DeepLeaf"] {
		t.Fatalf("DeepLeaf not in hottest bucket; body=%q", body)
	}
	if hot["Root"] {
		t.Errorf("root is in hottest bucket, want not-hottest; body=%q", body)
	}
	// And JumpHeaviest agrees.
	pc.JumpHeaviest()
	if n := pc.CursorNode(); n == nil || n.Op != "DeepLeaf" {
		got := "<nil>"
		if n != nil {
			got = n.Op
		}
		t.Errorf("JumpHeaviest landed on %q, want DeepLeaf", got)
	}
}

// TestPlanContext_Heat_ParallelNoNegativeBasis covers the negative-self
// clamp under a parallel plan: child time summed across workers exceeds
// the leader wall time, so the parent's raw SelfTime is negative. The
// clamp must keep the basis >=0 (no re-inverted heat) and the true
// self-heaviest node stays hottest.
//
//	Gather  total=50, Loops=1, child total=120 → raw SelfTime=-70 (clamp 0)
//	  PSeq  total=120 (summed across 4 workers), Loops=1, SelfTime=120
//	Sib     total=10  → SelfTime=10
//	Leaf2   total=5   → SelfTime=5
func TestPlanContext_Heat_ParallelNoNegativeBasis(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	pseq := &models.PlanNode{Op: "ParallelSeqScan", Cost: 30, EstRows: 1, ActualTotalTime: 120, Loops: 1}
	gather := &models.PlanNode{
		Op: "Gather", Cost: 40, EstRows: 1, ActualTotalTime: 50, Loops: 1,
		WorkersLaunched: 4, Children: []*models.PlanNode{pseq},
	}
	leaf2 := &models.PlanNode{Op: "Leaf2", Cost: 3, EstRows: 1, ActualTotalTime: 5, Loops: 1}
	sib := &models.PlanNode{Op: "Sib", Cost: 8, EstRows: 1, ActualTotalTime: 10, Loops: 1, Children: []*models.PlanNode{leaf2}}
	root := &models.PlanNode{Op: "Root", Cost: 60, EstRows: 1, ActualTotalTime: 70, Loops: 1, Children: []*models.PlanNode{gather, sib}}
	pc := newTestPlanContext(models.Plan{Node: root, Analyzed: true})

	// Raw stored SelfTime on Gather is negative — clamp is at the coloring
	// site, not at store time.
	if gather.SelfTime >= 0 {
		t.Fatalf("expected raw negative Gather.SelfTime, got %v", gather.SelfTime)
	}
	// No negative basis feeds the percentile buckets.
	for _, v := range pc.VisibleNodes() {
		if b := heatBasis(v.Node, true); b < 0 {
			t.Errorf("node %q feeds negative basis %v into coloring", v.Node.Op, b)
		}
	}
	body := pc.RenderBody()
	if !boldRedOps(body)["ParallelSeqScan"] {
		t.Fatalf("ParallelSeqScan not in hottest bucket; body=%q", body)
	}
	// JumpHeaviest must pick the real self-hog, not the always-largest root.
	pc.JumpHeaviest()
	if n := pc.CursorNode(); n == nil || n.Op != "ParallelSeqScan" {
		got := "<nil>"
		if n != nil {
			got = n.Op
		}
		t.Errorf("JumpHeaviest landed on %q, want ParallelSeqScan", got)
	}
}

// TestPlanContext_Heat_InitPlanNoNegativeBasis covers the InitPlan/SubPlan
// shape where summed child totals exceed the parent. Same invariant: no
// negative basis feeds coloring, real self-heaviest stays hottest.
//
//	Root  total=30, Loops=1, children total 80 → raw SelfTime=-50 (clamp 0)
//	  InitPlan  total=70 → SelfTime=70 (the hog)
//	  Main      total=10 → SelfTime=10
//	  Extra     total=4  → SelfTime=4
func TestPlanContext_Heat_InitPlanNoNegativeBasis(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	initp := &models.PlanNode{Op: "InitPlanSubquery", Cost: 25, EstRows: 1, ActualTotalTime: 70, Loops: 1}
	main := &models.PlanNode{Op: "MainScan", Cost: 12, EstRows: 1, ActualTotalTime: 10, Loops: 1}
	extra := &models.PlanNode{Op: "Extra", Cost: 4, EstRows: 1, ActualTotalTime: 4, Loops: 1}
	root := &models.PlanNode{
		Op: "Root", Cost: 50, EstRows: 1, ActualTotalTime: 30, Loops: 1,
		Children: []*models.PlanNode{initp, main, extra},
	}
	pc := newTestPlanContext(models.Plan{Node: root, Analyzed: true})

	if root.SelfTime >= 0 {
		t.Fatalf("expected raw negative Root.SelfTime, got %v", root.SelfTime)
	}
	for _, v := range pc.VisibleNodes() {
		if b := heatBasis(v.Node, true); b < 0 {
			t.Errorf("node %q feeds negative basis %v into coloring", v.Node.Op, b)
		}
	}
	body := pc.RenderBody()
	if !boldRedOps(body)["InitPlanSubquery"] {
		t.Fatalf("InitPlanSubquery not in hottest bucket; body=%q", body)
	}
	pc.JumpHeaviest()
	if n := pc.CursorNode(); n == nil || n.Op != "InitPlanSubquery" {
		got := "<nil>"
		if n != nil {
			got = n.Op
		}
		t.Errorf("JumpHeaviest landed on %q, want InitPlanSubquery", got)
	}
}

// TestPlanContext_Heat_EstimateOnlyUsesSelfCost pins that an estimate-only
// plan (no actuals) colors by SelfCost, never touching the zero
// ActualTotalTime. The deep self-cost child must be hottest, not the root.
func TestPlanContext_Heat_EstimateOnlyUsesSelfCost(t *testing.T) {
	if theme.IsMonochrome() {
		t.Skip("monochrome theme suppresses coloring")
	}
	leaf := &models.PlanNode{Op: "DeepLeaf", Cost: 90, EstRows: 1}
	mid := &models.PlanNode{Op: "Mid", Cost: 95, EstRows: 1, Children: []*models.PlanNode{leaf}}
	sib := &models.PlanNode{Op: "Sib", Cost: 2, EstRows: 1}
	root := &models.PlanNode{Op: "Root", Cost: 100, EstRows: 1, Children: []*models.PlanNode{mid, sib}}
	pc := newTestPlanContext(models.Plan{Node: root}) // not Analyzed
	body := pc.RenderBody()
	hot := boldRedOps(body)
	if !hot["DeepLeaf"] {
		t.Fatalf("DeepLeaf not in hottest bucket (estimate-only); body=%q", body)
	}
	if hot["Root"] {
		t.Errorf("root in hottest bucket (estimate-only), want not; body=%q", body)
	}
	// Estimate-only: actual_time column must be absent (no ANALYZE).
	if strings.Contains(body, "actual_time") {
		t.Errorf("estimate-only body unexpectedly renders actual_time; body=%q", body)
	}
}

// --- plan-doctor insights surfacing -----------------------------------------

// findingPlan builds an ANALYZED two-node plan that produces exactly one
// plan-doctor finding (a bad-row-estimate) on the child "Seq Scan flagged":
// EstRows=1 but ActualRows=1000 -> RowEstimateError=1000, SelfTime=8ms (well
// above the materiality floor). The root "Result" estimates correctly and is
// never flagged. The root has a child, so it can be collapsed to hide the
// finding's node for the Enter-jump test.
func findingPlan() models.Plan {
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
	return models.Plan{Node: root, Analyzed: true, RawText: "raw"}
}

// TestPlanContext_Insights_ComputedAtConstruction confirms the finding fixture
// yields exactly one finding so the rest of the T6 tests rest on solid ground.
func TestPlanContext_Insights_ComputedAtConstruction(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	if got := len(pc.Findings()); got != 1 {
		t.Fatalf("findingPlan should yield 1 finding, got %d: %+v", got, pc.Findings())
	}
	if pc.Findings()[0].NodeRef.Op != "Seq Scan flagged" {
		t.Errorf("finding NodeRef = %q, want Seq Scan flagged", pc.Findings()[0].NodeRef.Op)
	}
}

// TestPlanContext_InsightsStrip_RenderAndToggle pins the strip render + toggle:
// hidden by default, shown after ToggleInsights, listing the finding.
func TestPlanContext_InsightsStrip_RenderAndToggle(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	if strings.Contains(pc.RenderBody(), "INSIGHTS") {
		t.Fatal("strip should be hidden before ToggleInsights")
	}
	pc.ToggleInsights()
	if !pc.ShowInsights() || !pc.InsightsActive() {
		t.Fatal("ToggleInsights should show the strip when findings exist")
	}
	body := pc.RenderBody()
	if !strings.Contains(body, "INSIGHTS (1)") {
		t.Errorf("strip header missing; body=%q", body)
	}
	if !strings.Contains(body, "Bad row estimate") {
		t.Errorf("strip should list the finding title; body=%q", body)
	}
	pc.ToggleInsights()
	if pc.ShowInsights() {
		t.Error("second ToggleInsights should hide the strip")
	}
}

// TestPlanContext_InsightsStrip_ShowsSuggestedFix pins that the strip renders a
// finding's SuggestedFix, not just its Explanation. The fix is the actionable
// half of a finding; omitting it leaves the diagnostic without a remedy.
func TestPlanContext_InsightsStrip_ShowsSuggestedFix(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	pc.ToggleInsights()
	body := pc.RenderBody()
	if !strings.Contains(body, "Fix:") {
		t.Errorf("strip should label the suggested fix; body=%q", body)
	}
	if !strings.Contains(body, "run ANALYZE events") {
		t.Errorf("strip should render the SuggestedFix text; body=%q", body)
	}
}

// TestPlanContext_InsightsStrip_EmptyState pins the no-findings path: the
// toggle still flips the strip on (so i is never a silent no-op) and renders an
// explicit empty state, but insights never owns navigation (InsightsActive is
// false) so tree keys are not hijacked over an empty strip.
func TestPlanContext_InsightsStrip_EmptyState(t *testing.T) {
	pc := newTestPlanContext(samplePlan()) // estimate-only, no findings
	if len(pc.Findings()) != 0 {
		t.Fatalf("samplePlan should yield no findings, got %d", len(pc.Findings()))
	}
	pc.ToggleInsights()
	if !pc.ShowInsights() {
		t.Error("ToggleInsights must flip the strip on even with no findings (no silent no-op)")
	}
	if pc.InsightsActive() {
		t.Error("an empty strip must not own navigation")
	}
	body := pc.RenderBody()
	if !strings.Contains(body, "INSIGHTS (0)") {
		t.Errorf("empty-state body should render the INSIGHTS (0) header; body=%q", body)
	}
	if !strings.Contains(body, "no issues detected") {
		t.Errorf("empty-state body should explain there are no findings; body=%q", body)
	}
	pc.ToggleInsights()
	if pc.ShowInsights() {
		t.Error("second ToggleInsights should hide the empty strip")
	}
}

// TestPlanContext_InsightsStrip_WrapsToViewport pins that long finding
// explanations wrap to the panel's inner width rather than spilling past it.
func TestPlanContext_InsightsStrip_WrapsToViewport(t *testing.T) {
	pc := newTestPlanContext(samplePlan())
	pc.findings = []plandoctor.Finding{{
		Severity:    plandoctor.SeverityWarn,
		Title:       "Selective sequential scan",
		Explanation: "sequential scan read 1000000 rows and discarded 999980 via filter (100% rejected)",
	}}
	pc.showInsights = true
	const width = 40
	pc.SetViewportWidth(width)
	body := pc.RenderBody()

	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	var explanationLines int
	for line := range strings.SplitSeq(body, "\n") {
		plain := ansi.ReplaceAllString(line, "")
		if w := len([]rune(plain)); w > width {
			t.Errorf("strip line exceeds viewport width %d: %q (%d cols)", width, plain, w)
		}
		if strings.Contains(plain, "sequential scan") || strings.Contains(plain, "rejected") {
			explanationLines++
		}
	}
	if explanationLines < 2 {
		t.Errorf("long explanation should wrap onto >=2 lines, got %d", explanationLines)
	}
}

// TestPlanContext_Badge_OnFlaggedNodeOnly pins review amendment #2: the flagged
// node's tree row carries the badge; an unflagged node's row does not.
func TestPlanContext_Badge_OnFlaggedNodeOnly(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	body := pc.RenderBody() // tree mode; badges render regardless of strip toggle
	var flaggedRow, rootRow string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, "Seq Scan flagged") {
			flaggedRow = line
		}
		if strings.Contains(line, "Result") {
			rootRow = line
		}
	}
	if !strings.Contains(flaggedRow, "⚠ Bad row estimate") {
		t.Errorf("flagged row missing badge; row=%q", flaggedRow)
	}
	if strings.Contains(rootRow, "⚠") {
		t.Errorf("unflagged root row should carry no badge; row=%q", rootRow)
	}
}

// TestPlanContext_MoveInsightCursor_Clamps pins strip selection bounds: k at
// the first finding and j past the last both stay in range.
func TestPlanContext_MoveInsightCursor_Clamps(t *testing.T) {
	pc := newTestPlanContext(findingPlan()) // exactly one finding
	pc.MoveInsightCursor(-1)
	if pc.InsightCursor() != 0 {
		t.Errorf("k at first finding: cursor=%d, want 0", pc.InsightCursor())
	}
	pc.MoveInsightCursor(5)
	if pc.InsightCursor() != 0 {
		t.Errorf("j past last finding (only 1): cursor=%d, want 0", pc.InsightCursor())
	}
}

// TestPlanContext_JumpToSelectedFinding_ExpandsAncestors pins the Enter-jump:
// with the root collapsed (finding node hidden), JumpToSelectedFinding expands
// ancestors and lands the tree cursor on the finding's node.
func TestPlanContext_JumpToSelectedFinding_ExpandsAncestors(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	pc.CollapseAllButRoot() // hide the child under the collapsed root
	pc.Toggle()             // cursor is on root; collapse it so the child is not visible
	if len(pc.VisibleNodes()) != 1 {
		t.Fatalf("after collapsing root, want 1 visible node, got %d", len(pc.VisibleNodes()))
	}
	pc.JumpToSelectedFinding()
	if got := pc.CursorNode(); got == nil || got.Op != "Seq Scan flagged" {
		t.Fatalf("cursor did not land on the finding node; got=%v", got)
	}
}

// TestPlanContext_JumpToSelectedFinding_AlreadyAtCursor pins AC #7: when the
// cursor already sits on the finding's node, Enter is a no-op — the cursor
// index does not move and nothing panics.
func TestPlanContext_JumpToSelectedFinding_AlreadyAtCursor(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	pc.MoveCursor(1) // visible [root, child]; land on the flagged child
	if pc.CursorNode().Op != "Seq Scan flagged" {
		t.Fatalf("setup: cursor not on finding node; got %q", pc.CursorNode().Op)
	}
	before := pc.Cursor()
	pc.JumpToSelectedFinding()
	if pc.Cursor() != before {
		t.Errorf("cursor moved from %d to %d; Enter-on-current should be a no-op", before, pc.Cursor())
	}
	if pc.CursorNode().Op != "Seq Scan flagged" {
		t.Errorf("cursor left the finding node; got %q", pc.CursorNode().Op)
	}
}

// TestPlanContext_RawAndInsights_Independent pins AC #7: raw-text mode and the
// insights toggle are independent — with both on, RenderBody returns the
// sanitized raw text (strip suppressed); turning raw off reveals the strip.
func TestPlanContext_RawAndInsights_Independent(t *testing.T) {
	pc := newTestPlanContext(findingPlan())
	pc.ToggleInsights() // strip on
	pc.ToggleRaw()      // raw on — raw wins for rendering
	body := pc.RenderBody()
	if strings.Contains(body, "INSIGHTS") {
		t.Errorf("raw mode must not render the insights strip; body=%q", body)
	}
	if !strings.Contains(body, "raw") {
		t.Errorf("raw mode should render RawText; body=%q", body)
	}
	pc.ToggleRaw() // raw off — strip state preserved, now visible again
	if !strings.Contains(pc.RenderBody(), "INSIGHTS") {
		t.Errorf("strip state should survive a raw-mode round-trip; body=%q", pc.RenderBody())
	}
}
