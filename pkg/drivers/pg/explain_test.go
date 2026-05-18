//go:build integration

// Integration tests for (*Session).Explain against the docker/postgres fixture.
// Skipped (not failed) when DBSAVVY_TEST_PG is unset; see requirePGSession in
// execute_test.go for the shared bootstrap.

package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

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
