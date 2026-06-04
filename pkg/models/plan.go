package models

// Plan is the root of an EXPLAIN tree. RawText holds the engine's text-format
// EXPLAIN output (lines joined by "\n"); Node is the parsed structured tree
// derived from the engine's JSON-format EXPLAIN output. Either field may be
// empty if the corresponding format was unavailable or unparseable; both being
// populated is the expected happy-path. See epic dbsavvy-66p §66p.6.
//
// Analyzed is true when the parsed tree carries `EXPLAIN ANALYZE` actuals
// (Actual Total Cost / Actual Rows / Actual Loops) on at least one node.
// Drivers set this flag during parsing; UI rendering branches on it to add
// actual-cost / actual-rows / loops columns to the tree view. See
// dbsavvy-uv0.8.
// Notice, when non-empty, carries a user-facing degraded-mode message the UI
// surfaces as a toast — e.g. the server rejected the enriched EXPLAIN option
// set and the driver fell back to a bare EXPLAIN.
type Plan struct {
	Node     *PlanNode
	RawText  string
	Analyzed bool
	Notice   string
	// Settings holds the top-level "Settings" object PG emits under
	// `EXPLAIN (SETTINGS)` — non-default GUC values active for the plan (e.g.
	// work_mem). Nil/empty when the option was not requested or no settings
	// deviate from defaults.
	Settings map[string]string
}

// PlanNode is a single node in an EXPLAIN tree.
//
// ActualCost / ActualRows / Loops are zero unless the source was
// `EXPLAIN ANALYZE`; Plan.Analyzed flags whether the tree carries actuals
// anywhere. dbsavvy-uv0.8.
type PlanNode struct {
	Op         string
	Cost       float64
	EstRows    int64
	ActualCost float64
	ActualRows int64
	Loops      int64

	// Timing actuals (ms) from EXPLAIN ANALYZE.
	ActualTotalTime   float64
	ActualStartupTime float64

	// PlanWidth is the planner's estimated average row width in bytes.
	PlanWidth int

	// RowsRemovedByFilter is the ANALYZE count of rows discarded by a filter.
	RowsRemovedByFilter int64

	// Buffer accounting (EXPLAIN (BUFFERS)).
	SharedHitBlocks     int64
	SharedReadBlocks    int64
	SharedWrittenBlocks int64
	LocalHitBlocks      int64
	LocalReadBlocks     int64
	LocalWrittenBlocks  int64
	TempReadBlocks      int64
	TempWrittenBlocks   int64

	// Parallelism.
	WorkersLaunched int
	ParallelAware   bool

	// Sort node diagnostics.
	SortMethod    string
	SortSpaceUsed int64

	// HeapFetches is reported by Index Only Scan nodes.
	HeapFetches int64

	// Relation identity (needed so findings can name the relation).
	RelationName string
	Alias        string
	IndexName    string

	// OutputColumns captures the VERBOSE "Output" projection list. Reserved
	// for future plan-detail display; NOT consumed by any rule in this epic.
	OutputColumns []string

	Children []*PlanNode
	Detail   map[string]string

	// Derived (computed once post-parse by Plan.computeDerived; see below).
	// These store EXCLUSIVE ("self") magnitudes — node total minus the sum of
	// its children's totals — so the heat map and JumpHeaviest point at the
	// real bottleneck rather than the always-largest root.
	//
	// SelfCost / SelfTime may be NEGATIVE: under parallel workers, InitPlan/
	// SubPlan, or Append nodes the summed child totals can exceed the parent.
	// They are stored RAW (possibly negative) for honesty; callers that use
	// them as a coloring basis clamp to >=0 at the percentile-feed site.
	SelfCost float64 // node.Cost - sum(child.Cost); inclusive estimate basis.
	// SelfTime is per-loop-corrected exclusive actual time (ms):
	// node.TotalTime - sum(child.TotalTime).
	SelfTime float64
	// TotalTime is node.ActualTotalTime * max(Loops,1) — the inclusive wall
	// time this node accounts for (PG reports actual time as a per-loop avg).
	TotalTime float64
	// RowEstimateError = max(ActualRows*max(Loops,1),1) / max(EstRows,1); ~1.0
	// when estimate ~= actual; finite even when either side is zero.
	RowEstimateError float64
}

// ComputeDerived walks the tree and stores SelfCost, SelfTime, TotalTime, and
// RowEstimateError on every node. It is idempotent: the stored values are pure
// functions of the parsed fields, so re-running yields identical results. Call
// once at parse time (and from test helpers that hand-build plans).
func (p *Plan) ComputeDerived() {
	computeNodeDerived(p.Node)
}

func computeNodeDerived(n *PlanNode) {
	if n == nil {
		return
	}
	for _, c := range n.Children {
		computeNodeDerived(c)
	}

	n.TotalTime = n.ActualTotalTime * float64(maxInt64(n.Loops, 1))

	var childCost, childTime float64
	for _, c := range n.Children {
		childCost += c.Cost
		childTime += c.TotalTime
	}
	n.SelfCost = n.Cost - childCost
	n.SelfTime = n.TotalTime - childTime

	actualRows := n.ActualRows * maxInt64(n.Loops, 1)
	n.RowEstimateError = float64(maxInt64(actualRows, 1)) / float64(maxInt64(n.EstRows, 1))
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
