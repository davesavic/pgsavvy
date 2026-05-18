package models

// Plan is the root of an EXPLAIN tree. RawText holds the engine's text-format
// EXPLAIN output (lines joined by "\n"); Node is the parsed structured tree
// derived from the engine's JSON-format EXPLAIN output. Either field may be
// empty if the corresponding format was unavailable or unparseable; both being
// populated is the expected happy-path. See epic dbsavvy-66p §66p.6.
type Plan struct {
	Node    *PlanNode
	RawText string
}

// PlanNode is a single node in an EXPLAIN tree.
type PlanNode struct {
	Op       string
	Cost     float64
	EstRows  int64
	Children []*PlanNode
	Detail   map[string]string
}
