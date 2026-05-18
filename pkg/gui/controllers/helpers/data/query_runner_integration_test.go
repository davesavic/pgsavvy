//go:build integration

// Integration test for QueryRunner.Explain auto-rollback against the
// docker/postgres fixture. Skipped (not failed) when DBSAVVY_TEST_PG
// is unset. Mirrors the bootstrap pattern from pkg/drivers/pg tests.
//
// Covers acceptance scenario in dbsavvy-66p.11:
//
//	Given a docker pg session and a table app.notes with 0 rows
//	When <leader>E runs on "INSERT INTO app.notes(body) VALUES('x') RETURNING id"
//	Then app.notes row count remains 0 (the implicit BEGIN/ROLLBACK rolled it back)

package data_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

const envDSN = "DBSAVVY_TEST_PG"

// requirePGRunner builds a live QueryRunner against the docker fixture.
// Resources (driver, connection, session, SQLSession) are registered
// with t.Cleanup. Returns a fresh runner per call.
func requirePGRunner(t *testing.T) (*data.QueryRunner, *session.SQLSession) {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s unset; integration test requires docker/postgres fixture", envDSN)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe, err := pgx.Connect(probeCtx, dsn)
	if err != nil {
		t.Skipf("probe connect failed: %v", err)
	}
	_ = probe.Close(probeCtx)

	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, models.Connection{
		Name:   "query-runner-integration",
		Driver: "postgres",
		DSN:    dsn,
	})
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	inner, err := conn.AcquireSession(ctx)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("acquire session: %v", err)
	}
	sqlSess := session.New(conn, inner, session.Options{})
	t.Cleanup(func() {
		_ = sqlSess.Close()
		_ = conn.Close()
	})
	caps := drv.Capabilities()
	return data.NewQueryRunnerForSession(sqlSess, caps), sqlSess
}

func TestQueryRunnerExplainAnalyzeAutoRollbackPreservesRows(t *testing.T) {
	runner, sess := requirePGRunner(t)
	ctx := context.Background()

	// Fresh, isolated table per test run. Use a temp schema-less name
	// so the test is idempotent against repeated runs of the suite.
	const tbl = "qrunner_explain_analyze_rollback"
	mustExec(t, ctx, sess, "DROP TABLE IF EXISTS "+tbl)
	mustExec(t, ctx, sess, "CREATE TABLE "+tbl+" (id serial primary key, body text)")
	t.Cleanup(func() { _, _ = sess.Execute(context.Background(), models.Query{SQL: "DROP TABLE IF EXISTS " + tbl}) })

	// Sanity: zero rows before.
	if got := rowCount(t, ctx, sess, tbl); got != 0 {
		t.Fatalf("pre-explain row count = %d, want 0", got)
	}

	plan, err := runner.Explain(ctx, "INSERT INTO "+tbl+" (body) VALUES ('x') RETURNING id", true)
	if err != nil {
		t.Fatalf("Explain(analyze=true) err = %v", err)
	}
	if plan.RawText == "" {
		t.Fatal("plan.RawText empty, want EXPLAIN ANALYZE output")
	}

	// The auto-rollback wrap must leave the table empty.
	if got := rowCount(t, ctx, sess, tbl); got != 0 {
		t.Fatalf("post-explain row count = %d, want 0 (auto-rollback failed)", got)
	}
}

func TestQueryRunnerExplainPlainPathDoesNotWrap(t *testing.T) {
	runner, sess := requirePGRunner(t)
	ctx := context.Background()

	// SELECT-only EXPLAIN with analyze=false should not need a wrap;
	// the call succeeds outside of a transaction and the session
	// remains tx-free.
	plan, err := runner.Explain(ctx, "SELECT 1", false)
	if err != nil {
		t.Fatalf("Explain plain err = %v", err)
	}
	if plan.RawText == "" {
		t.Fatal("plan.RawText empty, want EXPLAIN output")
	}
	if sess.InTransaction() {
		t.Fatal("session left in a transaction after a plain Explain")
	}
}

func mustExec(t *testing.T, ctx context.Context, sess *session.SQLSession, sql string) {
	t.Helper()
	if _, err := sess.Execute(ctx, models.Query{SQL: sql}); err != nil {
		t.Fatalf("Execute(%q): %v", sql, err)
	}
}

func rowCount(t *testing.T, ctx context.Context, sess *session.SQLSession, tbl string) int {
	t.Helper()
	rh, err := sess.Stream(ctx, models.Query{SQL: "SELECT count(*)::int FROM " + tbl})
	if err != nil {
		t.Fatalf("count Stream: %v", err)
	}
	defer rh.Rows().Close()
	row, ok, err := rh.Rows().Next(ctx)
	if err != nil || !ok {
		t.Fatalf("count Next: ok=%v err=%v", ok, err)
	}
	v, _ := row.Values[0].(int32)
	if v != 0 {
		// pgx returns int32 for ::int; cast might surface differently
		// depending on driver settings, so try int64 / int as a fallback.
		if v64, ok := row.Values[0].(int64); ok {
			return int(v64)
		}
		if vint, ok := row.Values[0].(int); ok {
			return vint
		}
	}
	return int(v)
}

var _ = drivers.Capabilities{} // keep import for clarity when caps used in adjacent helpers
