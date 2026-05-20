package models

import "testing"

// TestPlanNode_ActualFields_ZeroByDefault verifies that the new
// Actual* / Loops fields default to zero on a freshly-constructed
// PlanNode. The shape matters: drivers parse selectively and we want
// any unset field to read as 0 rather than NaN / -1.
func TestPlanNode_ActualFields_ZeroByDefault(t *testing.T) {
	n := PlanNode{}
	if n.ActualCost != 0 {
		t.Errorf("ActualCost = %v, want 0", n.ActualCost)
	}
	if n.ActualRows != 0 {
		t.Errorf("ActualRows = %v, want 0", n.ActualRows)
	}
	if n.Loops != 0 {
		t.Errorf("Loops = %v, want 0", n.Loops)
	}
}

// TestPlan_Analyzed_ZeroByDefault pins Plan.Analyzed at false on a zero
// Plan. EXPLAIN (no ANALYZE) callers leave the field alone; the field
// flips only when a driver detects ANALYZE-flavoured actuals.
func TestPlan_Analyzed_ZeroByDefault(t *testing.T) {
	p := Plan{}
	if p.Analyzed {
		t.Errorf("Plan{}.Analyzed = true, want false")
	}
}
