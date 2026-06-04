package models

import (
	"math"
	"testing"
)

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

const eps = 1e-9

// TestComputeDerived_SelfCostAndTime verifies the exclusive-magnitude math:
// SelfCost = Cost - Σchild.Cost; TotalTime = ActualTotalTime*max(Loops,1);
// SelfTime = TotalTime - Σchild.TotalTime (per-loop corrected).
func TestComputeDerived_SelfCostAndTime(t *testing.T) {
	// gc: time per loop 2ms, looped 3x → TotalTime=6, leaf SelfTime=6.
	gc := &PlanNode{Op: "gc", Cost: 15, EstRows: 10, ActualTotalTime: 2, ActualRows: 4, Loops: 3}
	b := &PlanNode{Op: "b", Cost: 20, EstRows: 5, ActualTotalTime: 10, Loops: 1, Children: []*PlanNode{gc}}
	a := &PlanNode{Op: "a", Cost: 4, EstRows: 1, ActualTotalTime: 1, Loops: 1}
	root := &PlanNode{Op: "root", Cost: 40, EstRows: 1, ActualTotalTime: 30, Loops: 1, Children: []*PlanNode{a, b}}
	p := Plan{Node: root}
	p.ComputeDerived()

	// TotalTime.
	checkF(t, "gc.TotalTime", gc.TotalTime, 6) // 2*3
	checkF(t, "b.TotalTime", b.TotalTime, 10)  // 10*1
	checkF(t, "root.TotalTime", root.TotalTime, 30)

	// SelfCost.
	checkF(t, "gc.SelfCost", gc.SelfCost, 15)     // leaf
	checkF(t, "b.SelfCost", b.SelfCost, 5)        // 20-15
	checkF(t, "root.SelfCost", root.SelfCost, 16) // 40-(4+20)

	// SelfTime.
	checkF(t, "b.SelfTime", b.SelfTime, 4)        // 10-6
	checkF(t, "root.SelfTime", root.SelfTime, 19) // 30-(a:1 + b:10)
}

// TestComputeDerived_NegativeSelf documents that Self* may go negative when
// child inclusive totals exceed the parent (e.g. parallel/Append). The model
// stores the raw negative value; clamping is the coloring layer's job.
func TestComputeDerived_NegativeSelf(t *testing.T) {
	c1 := &PlanNode{Op: "c1", Cost: 30, ActualTotalTime: 30, Loops: 1}
	c2 := &PlanNode{Op: "c2", Cost: 30, ActualTotalTime: 30, Loops: 1}
	root := &PlanNode{Op: "root", Cost: 40, ActualTotalTime: 20, Loops: 1, Children: []*PlanNode{c1, c2}}
	p := Plan{Node: root}
	p.ComputeDerived()
	if root.SelfCost >= 0 {
		t.Errorf("root.SelfCost = %v, want negative (raw, unclamped)", root.SelfCost)
	}
	if root.SelfTime >= 0 {
		t.Errorf("root.SelfTime = %v, want negative (raw, unclamped)", root.SelfTime)
	}
}

// TestComputeDerived_RowEstimateError covers the zero-guards: never divide by
// zero, finite result, ~1.0 when both sides ~0.
func TestComputeDerived_RowEstimateError(t *testing.T) {
	tests := []struct {
		name      string
		actual    int64
		loops     int64
		est       int64
		wantRatio float64
	}{
		{"actual gt est", 100, 1, 10, 10},
		{"per-loop multiply", 50, 4, 10, 20}, // 50*4 / 10
		{"est zero -> finite", 5, 1, 0, 5},   // max(5,1)/max(0,1)=5
		{"both zero -> neutral 1", 0, 0, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := &PlanNode{ActualRows: tc.actual, Loops: tc.loops, EstRows: tc.est}
			p := Plan{Node: n}
			p.ComputeDerived()
			if math.IsInf(n.RowEstimateError, 0) || math.IsNaN(n.RowEstimateError) {
				t.Fatalf("RowEstimateError = %v, want finite", n.RowEstimateError)
			}
			checkF(t, "RowEstimateError", n.RowEstimateError, tc.wantRatio)
		})
	}
}

// TestComputeDerived_Idempotent verifies re-running yields identical values.
func TestComputeDerived_Idempotent(t *testing.T) {
	gc := &PlanNode{Op: "gc", Cost: 15, ActualTotalTime: 2, ActualRows: 4, Loops: 3, EstRows: 2}
	b := &PlanNode{Op: "b", Cost: 20, ActualTotalTime: 10, Loops: 1, Children: []*PlanNode{gc}}
	root := &PlanNode{Op: "root", Cost: 40, ActualTotalTime: 30, Loops: 1, Children: []*PlanNode{b}}
	p := Plan{Node: root}
	p.ComputeDerived()
	first := []float64{root.SelfCost, root.SelfTime, root.TotalTime, b.SelfTime, gc.SelfCost, gc.RowEstimateError}
	p.ComputeDerived()
	second := []float64{root.SelfCost, root.SelfTime, root.TotalTime, b.SelfTime, gc.SelfCost, gc.RowEstimateError}
	for i := range first {
		if math.Abs(first[i]-second[i]) > eps {
			t.Errorf("idx %d: first=%v second=%v (not idempotent)", i, first[i], second[i])
		}
	}
}

func checkF(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > eps {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
