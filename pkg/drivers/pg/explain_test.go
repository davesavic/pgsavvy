//go:build integration

// Integration tests for (*Session).Explain against the docker/postgres fixture.
// Skipped (not failed) when PGSAVVY_TEST_PG is unset; see requirePGSession in
// execute_test.go for the shared bootstrap.

package pg_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/stretchr/testify/require"
)

func TestReview_single_analyze_live(t *testing.T) {
	t.Run("option fallback preserves caller transaction", func(t *testing.T) {
		sess := requirePGSession(t)
		ctx := context.Background()
		version, err := sess.Execute(ctx, models.Query{SQL: "SHOW server_version_num"})
		require.NoError(t, err)
		versionNum, err := strconv.Atoi(version.Rows[0].Values[0].(string))
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: "CREATE TEMP TABLE single_analyze_marker (id int)"})
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: "CREATE TEMP SEQUENCE single_analyze_fallback_seq"})
		require.NoError(t, err)
		tx, err := sess.Begin(ctx, models.TxOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		_, err = sess.Execute(ctx, models.Query{SQL: "INSERT INTO single_analyze_marker VALUES (7)"})
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: "SAVEPOINT pgsavvy_explain_options"})
		require.NoError(t, err)
		for _, analyze := range []bool{false, true} {
			plan, err := sess.Explain(ctx, models.Query{SQL: "SELECT nextval('single_analyze_fallback_seq'), $1::int", Args: []any{42}}, analyze)
			require.NoError(t, err)
			require.Equal(t, analyze, plan.Analyzed)
			if versionNum < 120000 {
				require.Equal(t, "EXPLAIN options unsupported by server; showing basic plan", plan.Notice)
			} else {
				require.Empty(t, plan.Notice)
			}
		}
		_, err = sess.Execute(ctx, models.Query{SQL: "RELEASE SAVEPOINT pgsavvy_explain_options"})
		require.NoError(t, err, "the caller's same-named savepoint must survive")
		res, err := sess.Execute(ctx, models.Query{SQL: "SELECT id, (SELECT last_value FROM single_analyze_fallback_seq) FROM single_analyze_marker"})
		require.NoError(t, err)
		require.Equal(t, int32(7), res.Rows[0].Values[0])
		require.Equal(t, int64(1), res.Rows[0].Values[1])
		require.NoError(t, tx.Rollback(ctx))
		res, err = sess.Execute(ctx, models.Query{SQL: "SELECT count(*) FROM single_analyze_marker"})
		require.NoError(t, err)
		require.Equal(t, int64(0), res.Rows[0].Values[0])
	})
	t.Run("one execution with bound args and rollback", func(t *testing.T) {
		sess := requirePGSession(t)
		ctx := context.Background()
		_, err := sess.Execute(ctx, models.Query{SQL: "CREATE TEMP SEQUENCE single_analyze_seq"})
		require.NoError(t, err)
		tx, err := sess.Begin(ctx, models.TxOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		plan, err := sess.Explain(ctx, models.Query{SQL: "SELECT nextval('single_analyze_seq'), $1::int WHERE $1::int=42", Args: []any{42}}, true)
		require.NoError(t, err)
		require.True(t, plan.Analyzed)
		require.Equal(t, int64(1), plan.Node.ActualRows)
		require.NoError(t, tx.Rollback(ctx))
		res, err := sess.Execute(ctx, models.Query{SQL: "SELECT last_value FROM single_analyze_seq"})
		require.NoError(t, err)
		require.Equal(t, int64(1), res.Rows[0].Values[0], "rollback cannot undo a second nextval")
		require.Contains(t, plan.RawText, "Generated from captured EXPLAIN JSON")
		require.Contains(t, plan.RawText, "actual Rows: 1")
		if plan.Notice == "" {
			require.Contains(t, plan.RawText, "42", "enriched capture retains bound Output")
		}
	})
	t.Run("runtime syntax error is never retried", func(t *testing.T) {
		sess := requirePGSession(t)
		ctx := context.Background()
		_, err := sess.Execute(ctx, models.Query{SQL: "CREATE TEMP SEQUENCE single_analyze_error_seq"})
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: `CREATE FUNCTION pg_temp.single_analyze_error() RETURNS int LANGUAGE plpgsql AS $$
BEGIN
  PERFORM nextval('single_analyze_error_seq');
  RAISE EXCEPTION 'unrecognized EXPLAIN option "settings"' USING ERRCODE = '42601';
END $$`})
		require.NoError(t, err)
		plan, err := sess.Explain(ctx, models.Query{SQL: "SELECT pg_temp.single_analyze_error()"}, true)
		require.Error(t, err)
		require.Nil(t, plan.Node)
		res, err := sess.Execute(ctx, models.Query{SQL: "SELECT last_value FROM single_analyze_error_seq"})
		require.NoError(t, err)
		require.Equal(t, int64(1), res.Rows[0].Values[0])
	})
	t.Run("execution error is not recovered by option savepoint", func(t *testing.T) {
		sess := requirePGSession(t)
		ctx := context.Background()
		_, err := sess.Execute(ctx, models.Query{SQL: "CREATE TEMP SEQUENCE single_analyze_failed_tx_seq"})
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: "CREATE TEMP TABLE single_analyze_failed_tx_marker (id int)"})
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: `CREATE FUNCTION pg_temp.single_analyze_failed_tx() RETURNS int LANGUAGE plpgsql AS $$
BEGIN
  PERFORM nextval('single_analyze_failed_tx_seq');
  RAISE EXCEPTION 'unrecognized EXPLAIN option "settings"' USING ERRCODE = '42601';
END $$`})
		require.NoError(t, err)
		tx, err := sess.Begin(ctx, models.TxOptions{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		_, err = sess.Execute(ctx, models.Query{SQL: "SAVEPOINT pgsavvy_explain_options"})
		require.NoError(t, err)
		_, err = sess.Execute(ctx, models.Query{SQL: "INSERT INTO single_analyze_failed_tx_marker VALUES (7)"})
		require.NoError(t, err)
		plan, err := sess.Explain(ctx, models.Query{SQL: "SELECT pg_temp.single_analyze_failed_tx()"}, true)
		require.Error(t, err)
		require.Nil(t, plan.Node)
		status, _ := sess.LiveTxStatus()
		require.Equal(t, models.TxAbortedInTx, status, "only pre-execution option errors may be recovered")
		_, err = sess.Execute(ctx, models.Query{SQL: "ROLLBACK TO SAVEPOINT pgsavvy_explain_options"})
		require.NoError(t, err)
		res, err := sess.Execute(ctx, models.Query{SQL: "SELECT count(*) FROM single_analyze_failed_tx_marker"})
		require.NoError(t, err)
		require.Equal(t, int64(0), res.Rows[0].Values[0], "caller rollback must target its own savepoint, not the driver's later boundary")
		require.NoError(t, tx.Rollback(ctx))
		res, err = sess.Execute(ctx, models.Query{SQL: "SELECT last_value FROM single_analyze_failed_tx_seq"})
		require.NoError(t, err)
		require.Equal(t, int64(1), res.Rows[0].Values[0])
	})
	t.Run("estimate does not execute and retains native text", func(t *testing.T) {
		sess := requirePGSession(t)
		ctx := context.Background()
		_, err := sess.Execute(ctx, models.Query{SQL: "CREATE TEMP SEQUENCE single_analyze_estimate_seq"})
		require.NoError(t, err)
		plan, err := sess.Explain(ctx, models.Query{SQL: "SELECT nextval('single_analyze_estimate_seq')"}, false)
		require.NoError(t, err)
		require.False(t, plan.Analyzed)
		require.NotContains(t, plan.RawText, "Generated from captured")
		res, err := sess.Execute(ctx, models.Query{SQL: "SELECT is_called FROM single_analyze_estimate_seq"})
		require.NoError(t, err)
		require.Equal(t, false, res.Rows[0].Values[0])
	})
	t.Run("cancelled request never executes", func(t *testing.T) {
		sess := requirePGSession(t)
		ctx := context.Background()
		_, err := sess.Execute(ctx, models.Query{SQL: "CREATE TEMP SEQUENCE single_analyze_cancel_seq"})
		require.NoError(t, err)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		plan, err := sess.Explain(cancelled, models.Query{SQL: "SELECT nextval('single_analyze_cancel_seq')"}, true)
		require.Error(t, err)
		require.Nil(t, plan.Node)
		res, err := sess.Execute(ctx, models.Query{SQL: "SELECT is_called FROM single_analyze_cancel_seq"})
		require.NoError(t, err)
		require.Equal(t, false, res.Rows[0].Values[0])
	})
}

func TestPgExplain_AnalyzeFalse_ReturnsPlan(t *testing.T) {
	sess := requirePGSession(t)
	plan, err := sess.Explain(context.Background(), models.Query{SQL: "SELECT 1"}, false)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if plan.Node == nil {
		t.Fatal("plan.Node = nil, want non-nil")
	}
	if plan.Node.Op == "" {
		t.Errorf("plan.Node.Op = %q, want non-empty", plan.Node.Op)
	}
	if plan.RawText == "" {
		t.Errorf("plan.RawText is empty, want EXPLAIN text output")
	} else if !strings.Contains(plan.RawText, "Result") && !strings.Contains(plan.RawText, "Scan") {
		t.Errorf("plan.RawText = %q, want it to contain 'Result' or 'Scan'", plan.RawText)
	}
	if plan.Node.Cost < 0 {
		t.Errorf("plan.Node.Cost = %v, want >= 0", plan.Node.Cost)
	}
}

func TestPgExplain_AnalyzeTrue_RunsAnalyze(t *testing.T) {
	sess := requirePGSession(t)
	plan, err := sess.Explain(context.Background(), models.Query{SQL: "SELECT 1"}, true)
	if err != nil {
		t.Fatalf("Explain (analyze): %v", err)
	}
	if plan.Node == nil {
		t.Fatal("plan.Node = nil, want non-nil")
	}
	if plan.Node.Op == "" {
		t.Errorf("plan.Node.Op = %q, want non-empty", plan.Node.Op)
	}
	if plan.RawText == "" {
		t.Fatal("plan.RawText is empty, want EXPLAIN ANALYZE output")
	}
	if !strings.Contains(plan.RawText, "actual") {
		t.Errorf("plan.RawText = %q, want it to contain 'actual' (EXPLAIN ANALYZE marker)", plan.RawText)
	}
	if plan.Node.Cost < 0 {
		t.Errorf("plan.Node.Cost = %v, want >= 0", plan.Node.Cost)
	}
}

func TestPgExplain_BindsArgs(t *testing.T) {
	sess := requirePGSession(t)
	plan, err := sess.Explain(context.Background(), models.Query{
		SQL:  "SELECT $1::int",
		Args: []any{42},
	}, false)
	if err != nil {
		t.Fatalf("Explain with args: %v", err)
	}
	if plan.Node == nil {
		t.Fatal("plan.Node = nil, want non-nil")
	}
}

func TestPgExplain_NestedPlanFromJoin(t *testing.T) {
	sess := requirePGSession(t)
	plan, err := sess.Explain(context.Background(), models.Query{
		SQL: "SELECT * FROM pg_class JOIN pg_namespace ON pg_class.relnamespace = pg_namespace.oid LIMIT 1",
	}, false)
	if err != nil {
		t.Fatalf("Explain (join): %v", err)
	}
	if plan.Node == nil {
		t.Fatal("plan.Node = nil, want non-nil")
	}
	rootIsJoin := strings.Contains(plan.Node.Op, "Join")
	hasScanDescendant := false
	if len(plan.Node.Children) >= 1 {
		hasScanDescendant = anyDescendantOpContains(plan.Node, "Scan")
	}
	if !rootIsJoin && !hasScanDescendant {
		t.Errorf("expected a Join op or a Scan descendant; root Op=%q children=%d",
			plan.Node.Op, len(plan.Node.Children))
	}
}

func TestPgExplain_TimeoutPropagates(t *testing.T) {
	sess := requirePGSession(t)
	start := time.Now()
	_, err := sess.Explain(context.Background(), models.Query{
		SQL:     "SELECT 1",
		Timeout: 1 * time.Nanosecond,
	}, false)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("call took %v with 1ns timeout, want <50ms", elapsed)
	}
}

// anyDescendantOpContains walks the PlanNode tree and reports whether any node
// at or below n has an Op containing substr.
func anyDescendantOpContains(n *models.PlanNode, substr string) bool {
	if n == nil {
		return false
	}
	if strings.Contains(n.Op, substr) {
		return true
	}
	for _, c := range n.Children {
		if anyDescendantOpContains(c, substr) {
			return true
		}
	}
	return false
}
