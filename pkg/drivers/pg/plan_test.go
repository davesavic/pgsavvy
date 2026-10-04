package pg

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query/plandoctor"
)

func TestReview_single_analyze_generated_text(t *testing.T) {
	const raw = `[{"Execution Time":0.25,"Future Summary":{"counter":9007199254740993},"Plan":{"Node Type":"Result","Plan Rows":1,"Actual Rows":1,"Actual Loops":1,"Output":["42"],"Future Detail":{"nested":[true,null]},"Plans":[{"Node Type":"Seq Scan","Relation Name":"items","Actual Rows":0,"Actual Loops":0}]}}]`
	const want = `Generated from captured EXPLAIN JSON (not native PostgreSQL text)
Result
  actual Loops: 1
  actual Rows: 1
  Future Detail: {"nested":[true,null]}
  Output: ["42"]
  Plan Rows: 1
  -> Seq Scan
    actual Loops: 0
    actual Rows: 0
    Relation Name: "items"
Execution Time: 0.25
Future Summary: {"counter":9007199254740993}`
	for range 20 {
		plan, err := parsePlanJSON([]byte(raw))
		require.NoError(t, err)
		require.Equal(t, want, plan.RawText)
	}
	for _, raw := range []string{"", "[]", `[{"Plan":{}}]`, `[{"Plan":{"Plans":[null,42,{"Node Type":"Custom"}],"Mystery":[1,2]}}]`} {
		t.Run(raw, func(t *testing.T) {
			plan, err := parsePlanJSON([]byte(raw))
			require.NoError(t, err)
			if plan.Node != nil {
				require.True(t, strings.HasPrefix(plan.RawText, "Generated from captured EXPLAIN JSON"))
			}
		})
	}
	plan, err := parsePlanJSON([]byte(`[{"Plan":{"Plans":[null,42,{"Node Type":"Custom"}],"Mystery":[1,2]}}]`))
	require.NoError(t, err)
	require.Contains(t, plan.RawText, "(unknown node)")
	require.Contains(t, plan.RawText, "Mystery: [1,2]")
	require.Contains(t, plan.RawText, "Unrecognized plan entry: null")
	require.Contains(t, plan.RawText, "Unrecognized plan entry: 42")
	require.Contains(t, plan.RawText, "-> Custom")
	for _, raw := range []string{`[{"Plan":{"Plans":{"unexpected":true},"Node Type":42}}]`, `[{"Plan":{"Plans":"unexpected"}}]`} {
		plan, err := parsePlanJSON([]byte(raw))
		require.NoError(t, err)
		require.Contains(t, plan.RawText, "Plans:")
	}
}

func TestReview_diagnostics_parser(t *testing.T) {
	for _, tc := range []struct {
		name, fields        string
		analyzed, rowsKnown bool
		ratio               float64
		badEstimate         bool
	}{
		{"estimate only", ``, false, false, 1, false},
		{"all zero actuals", `,"Actual Rows":0,"Actual Loops":0,"Actual Total Time":0`, true, true, 1, false},
		{"measured zero", `,"Actual Rows":0,"Actual Loops":1,"Actual Total Time":1`, true, true, 0.01, true},
		{"accurate repeated", `,"Actual Rows":100,"Actual Loops":100,"Actual Total Time":1`, true, true, 1, false},
		{"unknown rows", `,"Actual Loops":1,"Actual Total Time":1`, true, false, 1, false},
		{"unexecuted", `,"Actual Rows":0,"Actual Loops":0,"Actual Total Time":1`, true, true, 1, false},
		{"null rows", `,"Actual Rows":null,"Actual Loops":1,"Actual Total Time":1`, true, false, 1, false},
		{"invalid rows", `,"Actual Rows":"invalid","Actual Loops":1,"Actual Total Time":1`, true, false, 1, false},
		{"invalid actuals only", `,"Actual Rows":null,"Actual Loops":"invalid","Actual Total Time":false`, false, false, 1, false},
		{"zero cost present", `,"Actual Total Cost":0`, true, false, 1, false},
		{"zero startup present", `,"Actual Startup Time":0`, true, false, 1, false},
		{"zero total time present", `,"Actual Total Time":0`, true, false, 1, false},
		{"zero loops present", `,"Actual Loops":0`, true, false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`[{"Plan":{"Node Type":"Result","Total Cost":1000,"Plan Rows":100%s}}]`, tc.fields))
			p, err := parsePlanJSON(raw)
			require.NoError(t, err)
			require.Equal(t, tc.analyzed, p.Analyzed)
			require.Equal(t, tc.analyzed, p.Node.HasActuals)
			require.Equal(t, tc.rowsKnown, p.Node.HasActualRows)
			require.InDelta(t, tc.ratio, p.Node.RowEstimateError, 1e-9)
			badEstimate := false
			for _, f := range plandoctor.Analyze(&p) {
				if f.Title == "Bad row estimate" {
					badEstimate = true
				}
			}
			require.Equal(t, tc.badEstimate, badEstimate)
		})
	}
	t.Run("mixed tree with measured zero descendant", func(t *testing.T) {
		p, err := parsePlanJSON([]byte(`[{"Plan":{"Node Type":"Result","Total Cost":1000,"Plan Rows":100,"Plans":[{"Node Type":"Result","Total Cost":500,"Plan Rows":100,"Actual Rows":0,"Actual Loops":1,"Actual Total Time":1}]}}]`))
		require.NoError(t, err)
		require.True(t, p.Analyzed)
		require.False(t, p.Node.HasActuals)
		require.False(t, p.Node.HasActualRows)
		require.Equal(t, float64(1), p.Node.RowEstimateError)
		child := p.Node.Children[0]
		require.True(t, child.HasActualRows)
		findings := plandoctor.Analyze(&p)
		require.Len(t, findings, 1)
		require.Equal(t, "Bad row estimate", findings[0].Title)
		require.Same(t, child, findings[0].NodeRef)
	})
}

func TestReview_diagnostics_live_repeated_scan(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires isolated PGSAVVY_TEST_PG fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		// A parse error can contain the fixture DSN, including credentials.
		t.Fatal("connection failed: check isolated fixture and PGSAVVY_TEST_PG")
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		require.NoError(t, conn.Close(closeCtx))
	})
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE diagnostic_rows AS SELECT i FROM generate_series(1,10000) AS i; ANALYZE diagnostic_rows; SET max_parallel_workers_per_gather=0`)
	require.NoError(t, err)
	var raw []byte
	err = conn.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON)
		SELECT sum(s.i) FROM generate_series(1,100) AS g(i)
		CROSS JOIN LATERAL (SELECT r.i FROM diagnostic_rows AS r WHERE r.i <= 100 + g.i * 0 OFFSET 0) AS s`).Scan(&raw)
	require.NoError(t, err)
	p, err := parsePlanJSON(raw)
	require.NoError(t, err)
	var scan *models.PlanNode
	var visit func(*models.PlanNode)
	visit = func(n *models.PlanNode) {
		if n.Op == "Seq Scan" && n.RelationName == "diagnostic_rows" {
			scan = n
		}
		for _, child := range n.Children {
			visit(child)
		}
	}
	visit(p.Node)
	require.NotNil(t, scan, "live plan must exercise the repeated sequential scan")
	require.Equal(t, int64(100), scan.Loops)
	require.Equal(t, int64(100), scan.ActualRows)
	require.Equal(t, int64(9900), scan.RowsRemovedByFilter)
	require.Positive(t, scan.EstRows)
	// Check all independent defects even if an earlier assertion fails.
	t.Run("presence", func(t *testing.T) {
		require.True(t, p.Analyzed)
		require.True(t, scan.HasActuals)
		require.True(t, scan.HasActualRows)
	})
	t.Run("per loop ratio", func(t *testing.T) {
		require.InDelta(t, float64(scan.ActualRows)/float64(scan.EstRows), scan.RowEstimateError, 1e-9)
	})
	t.Run("scan totals", func(t *testing.T) {
		var selective *plandoctor.Finding
		for _, f := range plandoctor.Analyze(&p) {
			if f.NodeRef == scan && f.Title == "Selective sequential scan" {
				selective = &f
			}
		}
		require.NotNil(t, selective, "repeated 99%% rejection must be diagnosed")
		require.Equal(t, "sequential scan read 1000000 rows and discarded 990000 via filter (99% rejected)", selective.Explanation)
	})
}

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

// TestParsePlanJSON_ActualFieldsPopulated verifies that the
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
