package plandoctor

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// analyzedPlan builds a *models.Plan from a hand-constructed root node, marks it
// analyzed, and runs ComputeDerived so SelfTime/SelfCost/RowEstimateError match
// production parsing.
func analyzedPlan(root *models.PlanNode) *models.Plan {
	p := &models.Plan{Node: root, Analyzed: true}
	p.ComputeDerived()
	return p
}

func estimatePlan(root *models.PlanNode) *models.Plan {
	p := &models.Plan{Node: root, Analyzed: false}
	p.ComputeDerived()
	return p
}

func TestAnalyzeNilSafe(t *testing.T) {
	cases := map[string]*models.Plan{
		"nil plan": nil,
		"nil node": {Node: nil},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			got := Analyze(p)
			if got == nil {
				t.Fatal("Analyze returned nil, want non-nil empty slice")
			}
			if len(got) != 0 {
				t.Fatalf("want 0 findings, got %d", len(got))
			}
		})
	}
}

func TestNoForbiddenImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, imp := range file.Imports {
				if strings.Contains(imp.Path.Value, "pkg/gui") {
					t.Errorf("%s imports forbidden package %s", path, imp.Path.Value)
				}
			}
		}
	}
}

// --- Bad row estimate ---------------------------------------------------------

func TestBadRowEstimateBoundaries(t *testing.T) {
	// RowEstimateError = actualRows / max(EstRows,1). Drive it with EstRows=10
	// so error = actualRows/10.
	tests := []struct {
		name       string
		estRows    int64
		actualRows int64
		wantFire   bool
	}{
		{"exactly 10x over fires", 10, 100, true},
		{"9.99x over no fire", 100, 999, false},
		{"exactly 0.1x under fires", 100, 10, true},
		{"0.101x under no fire", 1000, 101, false},
		{"healthy 1x no fire", 100, 100, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Big self-time so materiality always passes.
			n := &models.PlanNode{
				Op:              "Seq Scan",
				RelationName:    "orders",
				EstRows:         tt.estRows,
				ActualRows:      tt.actualRows,
				Loops:           1,
				ActualTotalTime: 50,
			}
			p := analyzedPlan(n)
			_, ok := findRule(Analyze(p), "Bad row estimate")
			if ok != tt.wantFire {
				t.Fatalf("fire=%v want %v (err=%.4f)", ok, tt.wantFire, n.RowEstimateError)
			}
		})
	}
}

func TestBadRowEstimateNamesRelation(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "orders",
		EstRows: 1, ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Bad row estimate")
	if !ok {
		t.Fatal("expected finding")
	}
	if !strings.Contains(f.SuggestedFix, "ANALYZE orders") {
		t.Fatalf("fix should name relation: %q", f.SuggestedFix)
	}
}

func TestBadRowEstimateNoRelationNoEmptyName(t *testing.T) {
	n := &models.PlanNode{
		Op: "Aggregate", EstRows: 1, ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Bad row estimate")
	if !ok {
		t.Fatal("expected finding")
	}
	if strings.Contains(f.SuggestedFix, "ANALYZE ;") || strings.HasSuffix(f.SuggestedFix, "ANALYZE ") {
		t.Fatalf("fix prints empty relation: %q", f.SuggestedFix)
	}
}

// --- Materiality floor --------------------------------------------------------

func TestMaterialityFloorSuppressesTrivialNode(t *testing.T) {
	// Ratio trips hard (1000x) but the node is below the self-time floor
	// (0.05ms < minSelfTimeMs) — must produce nothing.
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "tiny",
		EstRows: 1, ActualRows: 1000, Loops: 1,
		ActualTotalTime:     0.05,
		RowsRemovedByFilter: 100000,
	}
	got := Analyze(analyzedPlan(n))
	if len(got) != 0 {
		t.Fatalf("trivial node should yield 0 findings, got %d: %+v", len(got), got)
	}
}

// TestMaterialityFloorSurfacesSubMillisecondNode pins the loosened floor: a node
// whose self-time sits between the old 1.0ms and the new 0.1ms floors (0.3ms)
// now clears materiality, so a real bad-row-estimate finding is surfaced rather
// than suppressed for being fast.
func TestMaterialityFloorSurfacesSubMillisecondNode(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "small",
		EstRows: 1, ActualRows: 1000, Loops: 1,
		ActualTotalTime: 0.3,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Bad row estimate"); !ok {
		t.Fatal("a 0.3ms node with a 1000x estimate error should surface a finding under the loosened floor")
	}
}

// --- Seq scan -----------------------------------------------------------------

func TestSeqScanSelectiveFires(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "events",
		ActualRows: 100, Loops: 1, ActualTotalTime: 50,
		RowsRemovedByFilter: 990000, EstRows: 100,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Selective sequential scan")
	if !ok {
		t.Fatal("expected selective seq scan finding")
	}
	if !strings.Contains(f.SuggestedFix, "events") {
		t.Fatalf("fix should name relation: %q", f.SuggestedFix)
	}
}

func TestSeqScanFullyConsumedNoFire(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "events",
		ActualRows: 1000000, Loops: 1, ActualTotalTime: 50,
		RowsRemovedByFilter: 0, EstRows: 1000000,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Selective sequential scan"); ok {
		t.Fatal("fully-consumed scan should not fire")
	}
}

func TestSeqScanSmallNoFire(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "events",
		ActualRows: 5, Loops: 1, ActualTotalTime: 50,
		RowsRemovedByFilter: 100, EstRows: 5,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Selective sequential scan"); ok {
		t.Fatal("small scan should not fire")
	}
}

// --- Nested loop --------------------------------------------------------------

func TestNestedLoopBlowupFires(t *testing.T) {
	inner := &models.PlanNode{Op: "Index Scan", Loops: 5000, ActualRows: 1, ActualTotalTime: 0.01}
	outer := &models.PlanNode{Op: "Seq Scan", Loops: 1, ActualRows: 5000, ActualTotalTime: 1}
	root := &models.PlanNode{
		Op: "Nested Loop", Loops: 1, ActualRows: 5000, ActualTotalTime: 80,
		Children: []*models.PlanNode{outer, inner},
	}
	f, ok := findRule(Analyze(analyzedPlan(root)), "Nested loop blowup")
	if !ok {
		t.Fatal("expected nested loop finding")
	}
	if !strings.Contains(f.Explanation, "5000") {
		t.Fatalf("explanation should name loop count: %q", f.Explanation)
	}
}

func TestNestedLoopLowLoopsNoFire(t *testing.T) {
	inner := &models.PlanNode{Op: "Index Scan", Loops: 10, ActualRows: 1, ActualTotalTime: 0.01}
	outer := &models.PlanNode{Op: "Seq Scan", Loops: 1, ActualRows: 10, ActualTotalTime: 1}
	root := &models.PlanNode{
		Op: "Nested Loop", Loops: 1, ActualRows: 10, ActualTotalTime: 80,
		Children: []*models.PlanNode{outer, inner},
	}
	if _, ok := findRule(Analyze(analyzedPlan(root)), "Nested loop blowup"); ok {
		t.Fatal("low-loop nested loop should not fire")
	}
}

func TestNestedLoopEstimateOnlyNoPanic(t *testing.T) {
	inner := &models.PlanNode{Op: "Index Scan", EstRows: 1, Cost: 50}
	outer := &models.PlanNode{Op: "Seq Scan", EstRows: 5000, Cost: 50}
	root := &models.PlanNode{
		Op: "Nested Loop", EstRows: 5000, Cost: 5000,
		Children: []*models.PlanNode{outer, inner},
	}
	// Must not panic; Loops unavailable so blowup must not fire.
	if _, ok := findRule(Analyze(estimatePlan(root)), "Nested loop blowup"); ok {
		t.Fatal("estimate-only nested loop should not fire")
	}
}

// --- Ranking ------------------------------------------------------------------

func TestAnalyzeRanksBySeverityThenMateriality(t *testing.T) {
	// Two warn-level findings on nodes with different self-time; the heavier
	// node must rank first.
	heavy := &models.PlanNode{
		Op: "Seq Scan", RelationName: "big",
		EstRows: 1, ActualRows: 100000, Loops: 1, ActualTotalTime: 200,
	}
	light := &models.PlanNode{
		Op: "Seq Scan", RelationName: "small",
		EstRows: 1, ActualRows: 100000, Loops: 1, ActualTotalTime: 5,
	}
	root := &models.PlanNode{
		Op: "Append", ActualTotalTime: 210, Loops: 1,
		Children: []*models.PlanNode{heavy, light},
	}
	got := Analyze(analyzedPlan(root))
	if len(got) < 2 {
		t.Fatalf("expected >=2 findings, got %d", len(got))
	}
	if got[0].NodeRef != heavy {
		t.Fatalf("heavier node should rank first")
	}
}

func TestSeverityOrdering(t *testing.T) {
	if SeverityBlocker <= SeverityWarn || SeverityWarn <= SeverityInfo {
		t.Fatal("severity ordering is wrong")
	}
}

// TestSortFindingsEstimateClampsNegativeSelfCost pins the estimate-only
// tie-break through the selfCost clamp. A node with a small POSITIVE SelfCost
// must rank above a node with a NEGATIVE raw SelfCost (Append/parallel/
// InitPlan). It also guards against raw-magnitude inversion: a deeply negative
// SelfCost must not outrank a shallowly negative one — both clamp to 0 and keep
// stable order rather than ranking the more-negative node higher.
func TestSortFindingsEstimateClampsNegativeSelfCost(t *testing.T) {
	posSelf := &models.PlanNode{Op: "Seq Scan", SelfCost: 50}
	negShallow := &models.PlanNode{Op: "Seq Scan", SelfCost: -10}
	negDeep := &models.PlanNode{Op: "Seq Scan", SelfCost: -500}

	// Scrambled so a correct clamped sort must reorder: positive first, then the
	// two negatives in their original (stable) order since both clamp to 0.
	findings := []Finding{
		{NodeRef: negDeep, Severity: SeverityWarn, Title: "negDeep"},
		{NodeRef: negShallow, Severity: SeverityWarn, Title: "negShallow"},
		{NodeRef: posSelf, Severity: SeverityWarn, Title: "pos"},
	}
	sortFindings(findings, false /* analyzed */)

	if findings[0].NodeRef != posSelf {
		t.Fatalf("positive-self node must rank above negative-self nodes; got %q first", findings[0].Title)
	}
	// Without the clamp, raw -10 > -500 would float negShallow above negDeep,
	// inverting their input order; the clamp collapses both to 0 and stable sort
	// preserves negDeep-before-negShallow.
	if findings[1].NodeRef != negDeep || findings[2].NodeRef != negShallow {
		t.Fatalf("clamp must collapse negatives to 0 and keep stable order; got %q then %q",
			findings[1].Title, findings[2].Title)
	}
}

// TestSortFindingsSeverityDescending pins the severity-desc comparator
// independently of materiality: scrambled severities with arbitrary self values
// must come out Blocker, Warn, Info regardless of those values.
func TestSortFindingsSeverityDescending(t *testing.T) {
	info := &models.PlanNode{SelfTime: 999, SelfCost: 999}
	warn := &models.PlanNode{SelfTime: 1, SelfCost: 1}
	blocker := &models.PlanNode{SelfTime: 0, SelfCost: 0}

	findings := []Finding{
		{NodeRef: info, Severity: SeverityInfo, Title: "info"},
		{NodeRef: blocker, Severity: SeverityBlocker, Title: "blocker"},
		{NodeRef: warn, Severity: SeverityWarn, Title: "warn"},
	}
	sortFindings(findings, true /* analyzed */)

	want := []Severity{SeverityBlocker, SeverityWarn, SeverityInfo}
	for i, f := range findings {
		if f.Severity != want[i] {
			t.Fatalf("position %d: got severity %d want %d (order=%v)", i, f.Severity, want[i], severities(findings))
		}
	}
}

func severities(findings []Finding) []Severity {
	out := make([]Severity, len(findings))
	for i, f := range findings {
		out[i] = f.Severity
	}
	return out
}

// --- Pointer identity ---------------------------------------------------------

func TestFindingPointerIdentity(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "orders",
		EstRows: 1, ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	p := analyzedPlan(n)
	got := Analyze(p)
	if len(got) == 0 {
		t.Fatal("expected a finding")
	}
	if got[0].NodeRef != p.Node {
		t.Fatal("NodeRef must point at the same node instance in the plan")
	}
}

// --- Healthy plan -------------------------------------------------------------

func TestHealthyPlanNoFindings(t *testing.T) {
	idx := &models.PlanNode{
		Op: "Index Scan", IndexName: "orders_pkey", RelationName: "orders",
		EstRows: 100, ActualRows: 100, Loops: 1, ActualTotalTime: 30,
	}
	root := &models.PlanNode{
		Op: "Limit", EstRows: 100, ActualRows: 100, Loops: 1, ActualTotalTime: 31,
		Children: []*models.PlanNode{idx},
	}
	got := Analyze(analyzedPlan(root))
	if len(got) != 0 {
		t.Fatalf("healthy plan should yield 0 findings, got %d: %+v", len(got), got)
	}
}

// --- Disk spill ---------------------------------------------------------------

func TestDiskSpillExternalMergeFires(t *testing.T) {
	n := &models.PlanNode{
		Op: "Sort", SortMethod: "external merge", SortSpaceUsed: 20480, // 20 MB
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Disk spill")
	if !ok {
		t.Fatal("external merge sort should fire")
	}
	if !strings.Contains(f.Explanation, "20.0 MB") {
		t.Fatalf("explanation should state spill size from sort space used: %q", f.Explanation)
	}
	if !strings.Contains(f.Explanation, "sort space used") {
		t.Fatalf("explanation should name the size source: %q", f.Explanation)
	}
}

func TestDiskSpillTempBlocksFallback(t *testing.T) {
	// No SortSpaceUsed -> size comes from temp blocks written (1280 * 8KB = 10MB).
	n := &models.PlanNode{
		Op: "Hash", TempWrittenBlocks: 1280,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Disk spill")
	if !ok {
		t.Fatal("hash with temp blocks written should fire")
	}
	if !strings.Contains(f.Explanation, "10.0 MB") {
		t.Fatalf("explanation should compute size from temp blocks: %q", f.Explanation)
	}
	if !strings.Contains(f.Explanation, "temp blocks written") {
		t.Fatalf("explanation should name the size source: %q", f.Explanation)
	}
}

func TestDiskSpillInMemorySortNoFire(t *testing.T) {
	n := &models.PlanNode{
		Op: "Sort", SortMethod: "quicksort", TempWrittenBlocks: 0,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Disk spill"); ok {
		t.Fatal("in-memory quicksort should not fire")
	}
}

func TestDiskSpillWorkMemFromSettings(t *testing.T) {
	n := &models.PlanNode{
		Op: "Sort", SortMethod: "external merge", SortSpaceUsed: 20480,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	p := analyzedPlan(n)
	p.Settings = map[string]string{"work_mem": "4MB"}
	f, ok := findRule(Analyze(p), "Disk spill")
	if !ok {
		t.Fatal("expected disk spill finding")
	}
	if !strings.Contains(f.SuggestedFix, "currently 4MB") {
		t.Fatalf("fix should cite work_mem from Settings: %q", f.SuggestedFix)
	}
}

func TestDiskSpillWorkMemAbsentNoPlaceholder(t *testing.T) {
	n := &models.PlanNode{
		Op: "Sort", SortMethod: "external merge", SortSpaceUsed: 20480,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Disk spill")
	if !ok {
		t.Fatal("expected disk spill finding")
	}
	if strings.Contains(f.SuggestedFix, "currently") {
		t.Fatalf("fix must not fabricate a work_mem value: %q", f.SuggestedFix)
	}
	if !strings.Contains(f.SuggestedFix, "work_mem") {
		t.Fatalf("fix should still mention work_mem: %q", f.SuggestedFix)
	}
}

func TestDiskSpillZeroValuedNoPanicNoFire(t *testing.T) {
	n := &models.PlanNode{Op: "Sort", ActualRows: 100000, Loops: 1, ActualTotalTime: 50}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Disk spill"); ok {
		t.Fatal("sort with no spill signals should not fire")
	}
}

// --- Cache miss ---------------------------------------------------------------

func TestCacheMissHeavyFires(t *testing.T) {
	// read:hit = 5000:1000 = 5:1 (>= ratio 2.0), read well above the floor.
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "events",
		SharedReadBlocks: 5000, SharedHitBlocks: 1000,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Cache-miss heavy")
	if !ok {
		t.Fatal("cache-miss heavy scan should fire")
	}
	if !strings.Contains(f.Explanation, "83% miss") {
		t.Fatalf("explanation should report miss percentage: %q", f.Explanation)
	}
	if !strings.Contains(f.SuggestedFix, "events") {
		t.Fatalf("fix should name relation: %q", f.SuggestedFix)
	}
}

func TestCacheMissBoundaryReadBlocks(t *testing.T) {
	// AT the absolute floor (1000 read, 0 hit) fires; one below does not.
	at := &models.PlanNode{
		Op: "Seq Scan", SharedReadBlocks: cacheMissMinReadBlocks, SharedHitBlocks: 0,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	below := &models.PlanNode{
		Op: "Seq Scan", SharedReadBlocks: cacheMissMinReadBlocks - 1, SharedHitBlocks: 0,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(at)), "Cache-miss heavy"); !ok {
		t.Fatal("at absolute floor should fire")
	}
	if _, ok := findRule(Analyze(analyzedPlan(below)), "Cache-miss heavy"); ok {
		t.Fatal("just below absolute floor should not fire")
	}
}

func TestCacheMissRatioBoundary(t *testing.T) {
	// AT the 2:1 ratio (read 2000, hit 1000) fires; one read-block below the
	// ratio (read 1999, hit 1000) does not. Both clear the absolute floor, so
	// this isolates the ratio gate (the floor-boundary test uses hit=0, which
	// makes the ratio check vacuous).
	at := &models.PlanNode{
		Op: "Seq Scan", SharedReadBlocks: 2000, SharedHitBlocks: 1000,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	below := &models.PlanNode{
		Op: "Seq Scan", SharedReadBlocks: 1999, SharedHitBlocks: 1000,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(at)), "Cache-miss heavy"); !ok {
		t.Fatal("at exactly the 2:1 ratio should fire")
	}
	if _, ok := findRule(Analyze(analyzedPlan(below)), "Cache-miss heavy"); ok {
		t.Fatal("just below the 2:1 ratio should not fire")
	}
}

func TestCacheMissTinyHundredPercentNoFire(t *testing.T) {
	// 3 disk blocks read, 0 hits -> 100% miss by ratio, but absolute volume is
	// below the floor, so it must produce NO finding.
	n := &models.PlanNode{
		Op: "Seq Scan", RelationName: "tiny",
		SharedReadBlocks: 3, SharedHitBlocks: 0,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Cache-miss heavy"); ok {
		t.Fatal("tiny 100%-miss scan should not fire (below absolute floor)")
	}
}

func TestCacheMissFullyCachedNoFire(t *testing.T) {
	n := &models.PlanNode{
		Op: "Seq Scan", SharedReadBlocks: 0, SharedHitBlocks: 100000,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Cache-miss heavy"); ok {
		t.Fatal("fully cache-hit scan should not fire")
	}
}

func TestCacheMissRatioBelowNoFire(t *testing.T) {
	// 2000 read, 5000 hit: above the absolute floor but ratio 0.4 < 2.0.
	n := &models.PlanNode{
		Op: "Seq Scan", SharedReadBlocks: 2000, SharedHitBlocks: 5000,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Cache-miss heavy"); ok {
		t.Fatal("mostly-cached scan should not fire")
	}
}

// --- Heap refetch -------------------------------------------------------------

func TestHeapRefetchFires(t *testing.T) {
	n := &models.PlanNode{
		Op: "Index Only Scan", RelationName: "orders", IndexName: "orders_pkey",
		HeapFetches: 5000, ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	f, ok := findRule(Analyze(analyzedPlan(n)), "Heap refetch")
	if !ok {
		t.Fatal("index-only scan with many heap fetches should fire")
	}
	if !strings.Contains(f.Explanation, "5000") {
		t.Fatalf("explanation should report heap fetch count: %q", f.Explanation)
	}
	if !strings.Contains(f.SuggestedFix, "VACUUM") || !strings.Contains(f.SuggestedFix, "orders") {
		t.Fatalf("fix should recommend VACUUM and name relation: %q", f.SuggestedFix)
	}
}

func TestHeapRefetchBoundary(t *testing.T) {
	at := &models.PlanNode{
		Op: "Index Only Scan", HeapFetches: heapFetchThreshold,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	below := &models.PlanNode{
		Op: "Index Only Scan", HeapFetches: heapFetchThreshold - 1,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(at)), "Heap refetch"); !ok {
		t.Fatal("at threshold should fire")
	}
	if _, ok := findRule(Analyze(analyzedPlan(below)), "Heap refetch"); ok {
		t.Fatal("just below threshold should not fire")
	}
}

func TestHeapRefetchFewFetchesNoFire(t *testing.T) {
	// A few heap fetches on a freshly-loaded table: low absolute -> no finding.
	n := &models.PlanNode{
		Op: "Index Only Scan", HeapFetches: 12,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Heap refetch"); ok {
		t.Fatal("few heap fetches should not fire")
	}
}

func TestHeapRefetchZeroNoFire(t *testing.T) {
	n := &models.PlanNode{
		Op: "Index Only Scan", HeapFetches: 0,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Heap refetch"); ok {
		t.Fatal("zero heap fetches should not fire")
	}
}

func TestHeapRefetchOnlyIndexOnlyScan(t *testing.T) {
	// A plain Index Scan with HeapFetches set (shouldn't normally happen) must
	// not trip the rule — it is gated on Op.
	n := &models.PlanNode{
		Op: "Index Scan", HeapFetches: 5000,
		ActualRows: 100000, Loops: 1, ActualTotalTime: 50,
	}
	if _, ok := findRule(Analyze(analyzedPlan(n)), "Heap refetch"); ok {
		t.Fatal("non-index-only scan should not fire heap refetch")
	}
}

// findRule returns the first finding whose Title matches.
func findRule(findings []Finding, title string) (Finding, bool) {
	for _, f := range findings {
		if f.Title == title {
			return f, true
		}
	}
	return Finding{}, false
}
