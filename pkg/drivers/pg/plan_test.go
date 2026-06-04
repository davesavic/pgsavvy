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
	// Relation Name and Parallel Aware are now promoted to typed fields and
	// must no longer appear in Detail.
	require.Equal(t, "users", plan.Node.RelationName)
	require.NotContains(t, plan.Node.Detail, "Relation Name")
	require.False(t, plan.Node.ParallelAware)
	require.NotContains(t, plan.Node.Detail, "Parallel Aware")
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

// TestParsePlanJSON_RealAnalyzeKeysFlipAnalyzed is the regression for
// AMENDMENT 3: PG never emits "Actual Total Cost" — the keys it actually
// emits under ANALYZE are "Actual Total Time" / "Actual Startup Time" /
// "Actual Loops". This verifies they lift into typed fields and that the
// timing-based nodeHasActuals arm flips Analyzed true.
func TestParsePlanJSON_RealAnalyzeKeysFlipAnalyzed(t *testing.T) {
	raw := []byte(`[{"Plan": {
	  "Node Type": "Seq Scan",
	  "Total Cost": 12.5,
	  "Plan Rows": 100,
	  "Actual Startup Time": 0.05,
	  "Actual Total Time": 4.2,
	  "Actual Rows": 87,
	  "Actual Loops": 1
	}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.InDelta(t, 0.05, plan.Node.ActualStartupTime, 0.0001)
	require.InDelta(t, 4.2, plan.Node.ActualTotalTime, 0.0001)
	require.Equal(t, int64(87), plan.Node.ActualRows)
	require.Equal(t, int64(1), plan.Node.Loops)
	require.True(t, plan.Analyzed, "Analyzed should flip true via the ActualTotalTime arm")
}

// TestParsePlanJSON_AnalyzeBuffersVerboseGolden is the end-to-end golden
// fixture round-tripping the full set of T2-promoted fields from an
// ANALYZE + BUFFERS + VERBOSE + SETTINGS document, including a parallel
// child node with worker/buffer accounting and a sort node.
func TestParsePlanJSON_AnalyzeBuffersVerboseGolden(t *testing.T) {
	raw := []byte(`[{
	  "Plan": {
	    "Node Type": "Sort",
	    "Parallel Aware": false,
	    "Total Cost": 200.5,
	    "Plan Rows": 1000,
	    "Plan Width": 64,
	    "Actual Startup Time": 1.1,
	    "Actual Total Time": 12.3,
	    "Actual Rows": 950,
	    "Actual Loops": 1,
	    "Sort Method": "quicksort",
	    "Sort Space Used": 128,
	    "Output": ["users.id", "users.name"],
	    "Shared Hit Blocks": 40,
	    "Shared Read Blocks": 8,
	    "Plans": [
	      {
	        "Node Type": "Index Only Scan",
	        "Parallel Aware": true,
	        "Relation Name": "users",
	        "Alias": "u",
	        "Index Name": "users_pkey",
	        "Total Cost": 100.0,
	        "Plan Rows": 1000,
	        "Plan Width": 64,
	        "Actual Total Time": 6.5,
	        "Actual Rows": 1000,
	        "Actual Loops": 1,
	        "Workers Launched": 2,
	        "Heap Fetches": 3,
	        "Rows Removed by Filter": 17,
	        "Shared Hit Blocks": 100,
	        "Shared Read Blocks": 20,
	        "Shared Written Blocks": 1,
	        "Local Hit Blocks": 2,
	        "Local Read Blocks": 3,
	        "Local Written Blocks": 4,
	        "Temp Read Blocks": 5,
	        "Temp Written Blocks": 6
	      }
	    ]
	  },
	  "Settings": {"work_mem": "4MB", "random_page_cost": "1.1"}
	}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.True(t, plan.Analyzed)

	root := plan.Node
	require.NotNil(t, root)
	require.Equal(t, "Sort", root.Op)
	require.Equal(t, 64, root.PlanWidth)
	require.InDelta(t, 1.1, root.ActualStartupTime, 0.0001)
	require.InDelta(t, 12.3, root.ActualTotalTime, 0.0001)
	require.Equal(t, "quicksort", root.SortMethod)
	require.Equal(t, int64(128), root.SortSpaceUsed)
	require.False(t, root.ParallelAware)
	require.Equal(t, []string{"users.id", "users.name"}, root.OutputColumns)
	require.Equal(t, int64(40), root.SharedHitBlocks)
	require.Equal(t, int64(8), root.SharedReadBlocks)

	require.Len(t, root.Children, 1)
	child := root.Children[0]
	require.Equal(t, "Index Only Scan", child.Op)
	require.True(t, child.ParallelAware)
	require.Equal(t, "users", child.RelationName)
	require.Equal(t, "u", child.Alias)
	require.Equal(t, "users_pkey", child.IndexName)
	require.Equal(t, 2, child.WorkersLaunched)
	require.Equal(t, int64(3), child.HeapFetches)
	require.Equal(t, int64(17), child.RowsRemovedByFilter)
	require.Equal(t, int64(100), child.SharedHitBlocks)
	require.Equal(t, int64(20), child.SharedReadBlocks)
	require.Equal(t, int64(1), child.SharedWrittenBlocks)
	require.Equal(t, int64(2), child.LocalHitBlocks)
	require.Equal(t, int64(3), child.LocalReadBlocks)
	require.Equal(t, int64(4), child.LocalWrittenBlocks)
	require.Equal(t, int64(5), child.TempReadBlocks)
	require.Equal(t, int64(6), child.TempWrittenBlocks)

	// Promoted keys must not leak into Detail.
	require.NotContains(t, child.Detail, "Relation Name")
	require.NotContains(t, child.Detail, "Shared Hit Blocks")
	require.NotContains(t, child.Detail, "Heap Fetches")

	// Settings round-trips from the top-level envelope.
	require.Equal(t, "4MB", plan.Settings["work_mem"])
	require.Equal(t, "1.1", plan.Settings["random_page_cost"])
}

// TestParsePlanJSON_EstimateOnlyLeavesNewFieldsZero is the estimate-only
// regression: a plain EXPLAIN (no ANALYZE/BUFFERS/VERBOSE/SETTINGS) leaves
// every T2-promoted field at its zero value and Plan.Settings nil.
func TestParsePlanJSON_EstimateOnlyLeavesNewFieldsZero(t *testing.T) {
	raw := []byte(`[{"Plan": {"Node Type": "Seq Scan", "Total Cost": 1.0, "Plan Rows": 1}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.False(t, plan.Analyzed)
	require.Nil(t, plan.Settings)

	n := plan.Node
	require.Zero(t, n.ActualTotalTime)
	require.Zero(t, n.ActualStartupTime)
	require.Zero(t, n.PlanWidth)
	require.Zero(t, n.RowsRemovedByFilter)
	require.Zero(t, n.SharedHitBlocks)
	require.Zero(t, n.WorkersLaunched)
	require.False(t, n.ParallelAware)
	require.Empty(t, n.SortMethod)
	require.Zero(t, n.SortSpaceUsed)
	require.Zero(t, n.HeapFetches)
	require.Empty(t, n.RelationName)
	require.Empty(t, n.OutputColumns)
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

// TestParsePlanJSON_WrongTypeCoercesToZero is the T2 edge-case regression:
// when a numeric key carries a STRING value and a bool key carries a wrong
// type, the jsonNumberTo* helpers coerce to zero and the bool stays false
// without panicking. The test completing is proof that parsing did not panic.
func TestParsePlanJSON_WrongTypeCoercesToZero(t *testing.T) {
	raw := []byte(`[{"Plan": {
	  "Node Type": "Seq Scan",
	  "Total Cost": "abc",
	  "Plan Rows": 1,
	  "Plan Width": "nope",
	  "Shared Hit Blocks": "x",
	  "Parallel Aware": "yes"
	}}]`)
	plan, err := parsePlanJSON(raw)
	require.NoError(t, err)
	require.NotNil(t, plan.Node)
	require.Zero(t, plan.Node.Cost)
	require.Zero(t, plan.Node.PlanWidth)
	require.Zero(t, plan.Node.SharedHitBlocks)
	require.False(t, plan.Node.ParallelAware)
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
