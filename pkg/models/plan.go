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
type Plan struct {
	Node     *PlanNode
	RawText  string
	Analyzed bool
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
	Children   []*PlanNode
	Detail     map[string]string
}
