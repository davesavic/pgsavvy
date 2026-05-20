package pg

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParsePlanJSON_NestedPlans is a golden-fixture test exercising the
// recursive tree shape for a Hash Join over two Seq Scans. Verifies that
// recognized scalar keys lift to typed fields, unrecognized scalars land in
// Detail, and the "Plans" array recurses.
func TestParsePlanJSON_NestedPlans(t *testing.T) {
	raw := []byte(`[
	  {
	    "Plan": {
	      "Node Type": "Hash Join",
	      "Total Cost": 42.5,
	      "Plan Rows": 100,
	      "Join Type": "Inner",
	      "Plans": [
	        {"Node Type": "Seq Scan", "Total Cost": 10.0, "Plan Rows": 50, "Relation Name": "users"},
	        {"Node Type": "Hash", "Total Cost": 5.0, "Plan Rows": 50, "Plans": [
	          {"Node Type": "Seq Scan", "Total Cost": 3.0, "Plan Rows": 50, "Relation Name": "posts"}
	        ]}
	      ]
	    }
	  }
	]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.Equal(t, "Hash Join", plan.Node.Op)
	require.InDelta(t, 42.5, plan.Node.Cost, 0.0001)
	require.Equal(t, int64(100), plan.Node.EstRows)
	require.NotNil(t, plan.Node.Detail)
	require.Equal(t, "Inner", plan.Node.Detail["Join Type"])
	require.Len(t, plan.Node.Children, 2)
	require.Equal(t, "Seq Scan", plan.Node.Children[0].Op)
	require.Equal(t, "Hash", plan.Node.Children[1].Op)
	require.Len(t, plan.Node.Children[1].Children, 1)
	require.Equal(t, "Seq Scan", plan.Node.Children[1].Children[0].Op)
}

// TestParsePlanJSON_PlanRowsFractionalRounds verifies math.Round semantics on
// Plan Rows (PG reports floats; we store int64).
func TestParsePlanJSON_PlanRowsFractionalRounds(t *testing.T) {
	raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Total Cost": 1.0, "Plan Rows": 99.6}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.Equal(t, int64(100), plan.Node.EstRows)
}

// TestParsePlanJSON_ScalarDetailOnly verifies that mixed scalar values land in
// Detail (stringified via fmt.Sprint) while array/object values are skipped.
func TestParsePlanJSON_ScalarDetailOnly(t *testing.T) {
	raw := []byte(`[{
	  "Plan": {
	    "Node Type": "Seq Scan",
	    "Total Cost": 1.0,
	    "Plan Rows": 1,
	    "Parallel Aware": false,
	    "Startup Cost": 0.5,
	    "Relation Name": "users",
	    "Output": ["id", "name"],
	    "Some Object": {"k": "v"}
	  }
	}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.NotNil(t, plan.Node.Detail)
	require.Equal(t, "false", plan.Node.Detail["Parallel Aware"])
	require.Equal(t, "users", plan.Node.Detail["Relation Name"])
	// float64 stringifies to "0.5" via fmt.Sprint
	require.Contains(t, plan.Node.Detail, "Startup Cost")
	// Array and object values are NOT recorded in Detail.
	require.NotContains(t, plan.Node.Detail, "Output")
	require.NotContains(t, plan.Node.Detail, "Some Object")
}

// TestParsePlanJSON_EmptyEnvelope: a JSON array with no elements is valid and
// returns an empty Plan (Node nil) without error.
func TestParsePlanJSON_EmptyEnvelope(t *testing.T) {
	plan, err := parsePlanJSON([]byte(`[]`))
	require.NoError(t, err)
	require.Nil(t, plan.Node)
	require.Empty(t, plan.RawText)
}

// TestParsePlanJSON_EmptyBytes documents the defensive short-circuit: nil and
// zero-length input return empty Plan without error.
func TestParsePlanJSON_EmptyBytes(t *testing.T) {
	plan, err := parsePlanJSON(nil)
	require.NoError(t, err)
	require.Nil(t, plan.Node)

	plan, err = parsePlanJSON([]byte{})
	require.NoError(t, err)
	require.Nil(t, plan.Node)
}

// TestParsePlanJSON_Malformed enumerates the wrapped-error paths.
func TestParsePlanJSON_Malformed(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"truncated array", []byte(`[`)},
		{"not json", []byte(`not json`)},
		{"null element", []byte(`[null]`)},
		{"missing Plan key", []byte(`[{}]`)},
		{"Plan not object", []byte(`[{"Plan": "not an object"}]`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePlanJSON(tc.in)
			require.Error(t, err)
			require.ErrorContains(t, err, "EXPLAIN parse failed")
		})
	}
}

// TestParsePlanJSON_OmitsEmptyDetailMap: when only the four mapped keys are
// present, buildPlanNode resets Detail back to nil to keep the tree tidy.
func TestParsePlanJSON_OmitsEmptyDetailMap(t *testing.T) {
	raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Total Cost": 1.0, "Plan Rows": 1, "Plans": []}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.Empty(t, plan.Node.Detail, "Detail should be nil/empty when no unrecognized scalar keys exist")
}

// TestParsePlanJSON_ActualFieldsPopulated verifies that the post-uv0.8
// switch arms (Actual Total Cost / Actual Rows / Actual Loops) lift
// into typed fields and that Plan.Analyzed flips true.
func TestParsePlanJSON_ActualFieldsPopulated(t *testing.T) {
	raw := []byte(`[{"Plan": {
	  "Node Type": "Seq Scan",
	  "Total Cost": 12.5,
	  "Plan Rows": 100,
	  "Actual Total Cost": 9.75,
	  "Actual Rows": 87,
	  "Actual Loops": 3
	}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.InDelta(t, 9.75, plan.Node.ActualCost, 0.0001)
	require.Equal(t, int64(87), plan.Node.ActualRows)
	require.Equal(t, int64(3), plan.Node.Loops)
	require.True(t, plan.Analyzed, "Analyzed should flip true when any Actual* key is present")
}

// TestParsePlanJSON_AnalyzedFalseWithoutActuals confirms the Analyzed
// flag stays false on plain EXPLAIN output (no ANALYZE actuals).
func TestParsePlanJSON_AnalyzedFalseWithoutActuals(t *testing.T) {
	raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Total Cost": 1.0, "Plan Rows": 1}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.False(t, plan.Analyzed)
}

// TestParsePlanJSON_AnalyzedDescendantTriggersFlag exercises the
// recursive nodeHasActuals check: an inner Hash Join under a top-level
// Seq Scan with actuals should still flip plan.Analyzed.
func TestParsePlanJSON_AnalyzedDescendantTriggersFlag(t *testing.T) {
	raw := []byte(`[{"Plan": {
	  "Node Type": "Result",
	  "Total Cost": 1.0,
	  "Plan Rows": 1,
	  "Plans": [
	    {"Node Type": "Seq Scan", "Total Cost": 1.0, "Plan Rows": 1, "Actual Rows": 5}
	  ]
	}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.True(t, plan.Analyzed)
}
