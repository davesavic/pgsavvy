package plandoctor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// Severity ranks a Finding's importance. Higher = more severe. The explicit
// iota ordering lets Analyze sort findings deterministically (SeverityBlocker
// before SeverityWarn before SeverityInfo).
type Severity int

const (
	// SeverityInfo is an advisory observation worth noting but rarely urgent.
	SeverityInfo Severity = iota
	// SeverityWarn is a likely performance problem worth investigating.
	SeverityWarn
	// SeverityBlocker is a high-confidence, high-impact problem.
	SeverityBlocker
)

// Finding is one diagnostic produced by a rule. NodeRef points at the SAME
// *models.PlanNode instance inside the analyzed Plan (no defensive copy) so
// callers can locate it by pointer identity.
type Finding struct {
	NodeRef      *models.PlanNode
	Severity     Severity
	Title        string
	Explanation  string
	SuggestedFix string
}

// Materiality floors. A rule must not fire on a node too trivial to matter,
// even when a ratio technically trips. We gate on EITHER the analyzed self-time
// or, for estimate-only plans, the estimated self-cost.
const (
	// minSelfTimeMs is the smallest exclusive (self) wall-time, in
	// milliseconds, for a node to be worth flagging in an analyzed plan. The
	// floor exists only to drop genuinely trivial sub-0.1ms nodes; it was
	// loosened from 1.0ms because fast-but-imperfect plans (small/cached
	// tables) kept every node under 1ms and so surfaced no findings at all —
	// including predictive ones like a bad row estimate that matter regardless
	// of how fast the node ran today.
	minSelfTimeMs = 0.1
	// minSelfCost is the smallest exclusive (self) planner cost for a node to
	// be worth flagging in an estimate-only plan. Postgres seq-scans a handful
	// of pages for well under this; it corresponds to roughly a few hundred
	// sequential-page reads at default cost settings.
	minSelfCost = 100.0
)

// rule is a pure predicate over a single node (with access to the plan for the
// Analyzed flag). It returns a Finding and true when it fires.
type rule func(n *models.PlanNode, p *models.Plan) (Finding, bool)

// rules is the dispatch table. Order here does not affect output ordering;
// Analyze sorts the collected findings.
var rules = []rule{
	ruleBadRowEstimate,
	ruleSeqScanLarge,
	ruleNestedLoopBlowup,
	ruleDiskSpill,
	ruleCacheMiss,
	ruleHeapRefetch,
}

// Analyze walks the plan tree, runs every rule on every node, and returns the
// findings ranked by Severity (desc), tie-broken by clamped self-time (analyzed
// plans) or self-cost (estimate-only plans), both desc. It never returns nil
// and never panics on a nil plan or nil root node.
func Analyze(p *models.Plan) []Finding {
	findings := make([]Finding, 0)
	if p == nil || p.Node == nil {
		return findings
	}

	walk(p.Node, func(n *models.PlanNode) {
		for _, r := range rules {
			if f, ok := r(n, p); ok {
				findings = append(findings, f)
			}
		}
	})

	sortFindings(findings, p.Analyzed)
	return findings
}

func sortFindings(findings []Finding, analyzed bool) {
	materiality := func(n *models.PlanNode) float64 {
		if analyzed {
			return selfTime(n)
		}
		return selfCost(n)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return findings[i].Severity > findings[j].Severity
		}
		return materiality(findings[i].NodeRef) > materiality(findings[j].NodeRef)
	})
}

// walk applies fn to n and every descendant, pre-order.
func walk(n *models.PlanNode, fn func(*models.PlanNode)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}

// selfTime returns the node's exclusive wall time clamped to >= 0. SelfTime is
// stored raw and may be negative (parallel workers, InitPlan/SubPlan, Append),
// so any ranking or materiality use must clamp it locally — the GUI heat-map's
// clamp is not available in this package.
func selfTime(n *models.PlanNode) float64 {
	if n.SelfTime < 0 {
		return 0
	}
	return n.SelfTime
}

// selfCost returns the node's exclusive planner cost clamped to >= 0. SelfCost
// is stored raw and may be negative (Append, parallel workers, InitPlan/
// SubPlan), so any ranking or materiality use must clamp it locally — mirrors
// selfTime for the estimate-only path.
func selfCost(n *models.PlanNode) float64 {
	if n.SelfCost < 0 {
		return 0
	}
	return n.SelfCost
}

// material reports whether the node clears the materiality floor for the given
// plan: self-time floor for analyzed plans, self-cost floor otherwise.
func material(n *models.PlanNode, p *models.Plan) bool {
	if p.Analyzed {
		return selfTime(n) >= minSelfTimeMs
	}
	return selfCost(n) >= minSelfCost
}

// actualRows returns ActualRows corrected for loop count (PG reports per-loop
// averages), with a floor of the raw ActualRows when Loops is unset.
func actualRows(n *models.PlanNode) int64 {
	loops := max(n.Loops, 1)
	return n.ActualRows * loops
}

// withRelation appends " <RelationName>" to base when the relation name is
// present, and returns base unchanged otherwise — never prints an empty name.
func withRelation(base, relation string) string {
	if relation == "" {
		return base
	}
	return base + " " + relation
}

// --- Rule 1: bad row estimate -------------------------------------------------

const (
	// rowEstimateOverFactor: the planner under-estimated rows by >= this factor
	// (actual is >= 10x estimate). 10x is the canonical "an order of magnitude
	// off" threshold where join/scan strategy choices start going wrong.
	rowEstimateOverFactor = 10.0
	// rowEstimateUnderFactor: the planner over-estimated rows by >= this factor
	// (actual is <= 1/10 of estimate). Symmetric with rowEstimateOverFactor.
	rowEstimateUnderFactor = 0.1
)

func ruleBadRowEstimate(n *models.PlanNode, p *models.Plan) (Finding, bool) {
	if !material(n, p) {
		return Finding{}, false
	}
	err := n.RowEstimateError
	if err < rowEstimateOverFactor && err > rowEstimateUnderFactor {
		return Finding{}, false
	}

	expected := n.EstRows
	actual := actualRows(n)
	fix := "the planner's row estimate is far from reality, suggesting stale statistics"
	if n.RelationName != "" {
		fix = withRelation(fix+"; run ANALYZE", n.RelationName)
	} else {
		fix += "; run ANALYZE on the underlying relation"
	}

	return Finding{
		NodeRef:  n,
		Severity: SeverityWarn,
		Title:    "Bad row estimate",
		Explanation: fmt.Sprintf(
			"estimated %d rows but produced %d (off by %.1fx)",
			expected, actual, factor(err),
		),
		SuggestedFix: fix,
	}, true
}

// factor renders a row-estimate error as a human "Nx" multiplier always >= 1:
// for under-estimates it is the error itself, for over-estimates its inverse.
func factor(err float64) float64 {
	if err >= 1 {
		return err
	}
	if err == 0 {
		return 0
	}
	return 1 / err
}

// --- Rule 2: selective seq scan on a large table ------------------------------

const (
	// seqScanLargeRows: a Seq Scan producing fewer than this many rows is too
	// small to be worth an index regardless of selectivity. ~10k rows is a
	// reasonable floor below which a sequential scan is typically fine.
	seqScanLargeRows = 10_000
	// seqScanSelectiveRatio: the scan is "selective" when rows removed by the
	// filter are >= this multiple of the rows kept — i.e. the filter throws
	// away the large majority of what it reads, which an index could avoid.
	// 9.0 means >= 90% of scanned rows are discarded.
	seqScanSelectiveRatio = 9.0
)

func ruleSeqScanLarge(n *models.PlanNode, p *models.Plan) (Finding, bool) {
	if n.Op != "Seq Scan" {
		return Finding{}, false
	}
	if !material(n, p) {
		return Finding{}, false
	}

	kept := actualRows(n)
	if !p.Analyzed {
		kept = n.EstRows
	}

	// "Large" gate: the scan must process a meaningful number of rows.
	removed := n.RowsRemovedByFilter
	scanned := kept + removed
	if scanned < seqScanLargeRows {
		return Finding{}, false
	}

	// "Selective" gate: a fully-consumed scan (~0 removed) gets no benefit from
	// an index. Require the filter to discard the large majority of rows.
	if removed == 0 {
		return Finding{}, false
	}
	if float64(removed) < seqScanSelectiveRatio*float64(maxKept(kept)) {
		return Finding{}, false
	}

	return Finding{
		NodeRef:  n,
		Severity: SeverityWarn,
		Title:    "Selective sequential scan",
		Explanation: fmt.Sprintf(
			"sequential scan read %d rows and discarded %d via filter (%.0f%% rejected)",
			scanned, removed, 100*float64(removed)/float64(scanned),
		),
		SuggestedFix: withRelation("a selective index on the filtered column may help; relation", n.RelationName),
	}, true
}

// maxKept floors the kept-row count at 1 so the selectivity ratio is defined
// even when every scanned row is filtered out.
func maxKept(kept int64) int64 {
	if kept < 1 {
		return 1
	}
	return kept
}

// --- Rule 3: nested-loop blowup ----------------------------------------------

const (
	// nestedLoopHighLoops: a Nested Loop whose inner side executes at least
	// this many times is a likely blowup — the per-iteration cost compounds and
	// a hash or merge join usually wins. 1000 inner iterations is the point
	// where repeated index probes typically lose to a single bulk join.
	nestedLoopHighLoops = 1000
)

func ruleNestedLoopBlowup(n *models.PlanNode, p *models.Plan) (Finding, bool) {
	if n.Op != "Nested Loop" {
		return Finding{}, false
	}
	if !material(n, p) {
		return Finding{}, false
	}
	// Loop count comes from ANALYZE actuals. Without them (estimate-only plans
	// report Loops as 0/1) the rule cannot judge the blowup, so it stays quiet.
	loops := innerLoops(n)
	if loops < nestedLoopHighLoops {
		return Finding{}, false
	}

	return Finding{
		NodeRef:  n,
		Severity: SeverityWarn,
		Title:    "Nested loop blowup",
		Explanation: fmt.Sprintf(
			"inner side executed %d times; a hash/merge join may be cheaper",
			loops,
		),
		SuggestedFix: "consider a join condition that enables a hash or merge join instead of repeated inner lookups",
	}, true
}

// innerLoops returns the number of times the inner (second) child of a nested
// loop was executed, or 0 when actuals are unavailable.
func innerLoops(n *models.PlanNode) int64 {
	if len(n.Children) < 2 {
		return 0
	}
	return n.Children[1].Loops
}

// --- Rule 4: disk spill on a Sort/Hash node ----------------------------------

const (
	// pgBlockBytes is Postgres's default block size (8 KiB). EXPLAIN reports
	// temp block counts, not bytes, so we multiply by this to recover a size.
	pgBlockBytes = 8 * 1024
	// bytesPerMB / kbPerMB convert raw byte / KB magnitudes to MB for the
	// human-readable spill size. PG reports Sort Space Used in KB.
	bytesPerMB = 1024 * 1024
	kbPerMB    = 1024
)

func ruleDiskSpill(n *models.PlanNode, p *models.Plan) (Finding, bool) {
	if !isSortOrHash(n.Op) {
		return Finding{}, false
	}
	if !material(n, p) {
		return Finding{}, false
	}
	// A node spilled to disk if it wrote temp blocks or PG reported an external
	// merge sort. An in-memory quicksort with no temp writes never trips this.
	if n.TempWrittenBlocks == 0 && n.SortMethod != "external merge" {
		return Finding{}, false
	}

	sizeMB, source := spillSizeMB(n)
	explanation := fmt.Sprintf(
		"%s node spilled to disk (~%.1f MB, from %s); it exceeded work_mem and used temporary files",
		n.Op, sizeMB, source,
	)

	return Finding{
		NodeRef:      n,
		Severity:     SeverityWarn,
		Title:        "Disk spill",
		Explanation:  explanation,
		SuggestedFix: workMemFix(p),
	}, true
}

// isSortOrHash reports whether the op denotes a memory-bounded operation that
// can spill: anything whose name contains "Sort" or "Hash".
func isSortOrHash(op string) bool {
	return strings.Contains(op, "Sort") || strings.Contains(op, "Hash")
}

// spillSizeMB returns an honest spill size in MB and names its source. It
// prefers PG's reported Sort Space Used (KB) when present, falling back to the
// temp blocks written times the default block size. Returns 0 / "unknown" when
// neither signal is available (caller still fires on SortMethod alone).
func spillSizeMB(n *models.PlanNode) (float64, string) {
	if n.SortSpaceUsed > 0 {
		return float64(n.SortSpaceUsed) / kbPerMB, "sort space used"
	}
	if n.TempWrittenBlocks > 0 {
		return float64(n.TempWrittenBlocks*pgBlockBytes) / bytesPerMB, "temp blocks written"
	}
	return 0, "unknown"
}

// workMemFix builds the SuggestedFix for a disk spill. It appends the current
// work_mem value ONLY when the parsed plan Settings carry it — never a
// fabricated placeholder.
func workMemFix(p *models.Plan) string {
	wm, ok := p.Settings["work_mem"]
	if !ok || wm == "" {
		return "raise work_mem so the operation fits in memory"
	}
	return fmt.Sprintf("raise work_mem (currently %s) so the operation fits in memory", wm)
}

// --- Rule 5: cache-miss heavy scan -------------------------------------------

const (
	// cacheMissRatio: shared blocks read from disk must be at least this multiple
	// of blocks served from cache for the node to look cache-miss heavy. 2.0
	// means disk reads outnumber cache hits 2:1 — the buffer cache is not holding
	// this relation's hot pages.
	cacheMissRatio = 2.0
	// cacheMissMinReadBlocks: absolute floor on shared blocks read from disk.
	// Ratio alone is not enough (a 3-block "100% miss" is noise); the node must
	// actually move a meaningful volume off disk. 1000 blocks (~8 MB at the
	// default block size) is a conservative "this is real I/O" threshold.
	cacheMissMinReadBlocks = 1000
)

func ruleCacheMiss(n *models.PlanNode, p *models.Plan) (Finding, bool) {
	if !material(n, p) {
		return Finding{}, false
	}
	read := n.SharedReadBlocks
	hit := n.SharedHitBlocks
	// Fully cache-hit scan (no disk reads) can never be cache-miss heavy.
	if read == 0 {
		return Finding{}, false
	}
	// Absolute-volume floor: a tiny "100% miss" (e.g. 3 disk blocks) is noise.
	if read < cacheMissMinReadBlocks {
		return Finding{}, false
	}
	// Ratio gate: disk reads must dominate cache hits.
	if float64(read) < cacheMissRatio*float64(hit) {
		return Finding{}, false
	}

	total := read + hit
	missPct := 100 * float64(read) / float64(total)
	return Finding{
		NodeRef:  n,
		Severity: SeverityWarn,
		Title:    "Cache-miss heavy",
		Explanation: fmt.Sprintf(
			"read %d blocks from disk vs %d from cache (%.0f%% miss); the buffer cache is not holding this data",
			read, hit, missPct,
		),
		SuggestedFix: withRelation(
			"this scan is I/O-bound; consider increasing shared_buffers, warming the cache, or a narrower index on relation",
			n.RelationName,
		),
	}, true
}

// --- Rule 6: heap refetch on an Index Only Scan ------------------------------

const (
	// heapFetchThreshold: an Index Only Scan that drops to the heap for at least
	// this many tuples is defeating its own purpose — the visibility map is stale
	// and the "index only" path is doing heap I/O anyway. 1000 fetches is the
	// point where a VACUUM to refresh the visibility map clearly pays off; below
	// it the heap traffic is negligible on a freshly-loaded table.
	heapFetchThreshold = 1000
)

func ruleHeapRefetch(n *models.PlanNode, p *models.Plan) (Finding, bool) {
	if n.Op != "Index Only Scan" {
		return Finding{}, false
	}
	if !material(n, p) {
		return Finding{}, false
	}
	// A few heap fetches on a freshly-loaded table are normal; only flag when the
	// absolute volume crosses the threshold.
	if n.HeapFetches < heapFetchThreshold {
		return Finding{}, false
	}

	return Finding{
		NodeRef:  n,
		Severity: SeverityInfo,
		Title:    "Heap refetch",
		Explanation: fmt.Sprintf(
			"index-only scan made %d heap fetches; the visibility map is stale so it read the heap anyway",
			n.HeapFetches,
		),
		SuggestedFix: withRelation("run VACUUM to refresh the visibility map on relation", n.RelationName),
	}, true
}
