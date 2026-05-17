package models

// Plan is the root of an EXPLAIN tree.
type Plan struct {
	Node *PlanNode
}

// PlanNode is a single node in an EXPLAIN tree.
type PlanNode struct {
	Op       string
	Cost     float64
	EstRows  int64
	Children []*PlanNode
	Detail   map[string]string
}
