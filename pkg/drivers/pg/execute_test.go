//go:build integration

// Integration tests for (*Session).Execute against the docker/postgres fixture.
// Skipped (not failed) when PGSAVVY_TEST_PG is unset or the fixture probe
// fails; see test/integration for the shared probe pattern.

package pg_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

const envDSN = "PGSAVVY_TEST_PG"

// requirePGSession is the per-file gate: skip when the fixture DSN is unset,
// otherwise build a Driver, open a Connection, and acquire a Session. All
// resources are registered with t.Cleanup.
func requirePGSession(t *testing.T) drivers.Session {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s unset; integration test requires docker/postgres fixture", envDSN)
	}
	// Quick probe so we get a clean Skip rather than an Open failure.
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
		Name:   "execute-test",
		Driver: "postgres",
		DSN:    dsn,
	}, nil)
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("acquire session: %v", err)
	}
	t.Cleanup(func() {
		_ = sess.Close()
		_ = conn.Close()
	})
	return sess
}

func TestPgExecuteSelectReturnsRowsAndDuration(t *testing.T) {
	sess := requirePGSession(t)
	res, err := sess.Execute(context.Background(), models.Query{
		SQL: "SELECT id, email FROM app.users ORDER BY id LIMIT 3",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := len(res.Columns); got != 2 {
		t.Fatalf("Result.Columns len = %d, want 2", got)
	}
	if res.Columns[0].Name != "id" || res.Columns[1].Name != "email" {
		t.Errorf("Result.Columns names = [%s, %s], want [id, email]",
			res.Columns[0].Name, res.Columns[1].Name)
	}
	if got := len(res.Rows); got != 3 {
		t.Fatalf("Result.Rows len = %d, want 3", got)
	}
	if res.Duration <= 0 {
		t.Errorf("Result.Duration = %v, want > 0", res.Duration)
	}
	// Spot-check the decoded values — pgx returns id as int64 and email as string.
	if v, ok := res.Rows[0].Values[1].(string); !ok || v != "alice@example.com" {
		t.Errorf("first row email = %v (%T), want \"alice@example.com\"",
			res.Rows[0].Values[1], res.Rows[0].Values[1])
	}
}

func TestPgExecuteEmptySelect(t *testing.T) {
	sess := requirePGSession(t)
	res, err := sess.Execute(context.Background(), models.Query{
		SQL: "SELECT 1 WHERE FALSE",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("Result.Rows len = %d, want 0", len(res.Rows))
	}
	if len(res.Columns) != 1 {
		t.Errorf("Result.Columns len = %d, want 1 (FieldDescriptions returned even on empty result)",
			len(res.Columns))
	}
}

func TestPgExecuteSyntaxErrorWrapsToQueryError(t *testing.T) {
	sess := requirePGSession(t)
	_, err := sess.Execute(context.Background(), models.Query{
		SQL: "SELECT * FROM no_such_table_xyz",
	})
	if err == nil {
		t.Fatal("expected error from undefined-table query, got nil")
	}
	var qe *drivers.QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v (%T), want *drivers.QueryError", err, err)
	}
	if qe.Code != "42P01" {
		t.Errorf("QueryError.Code = %q, want 42P01 (undefined_table)", qe.Code)
	}
}

func TestPgExecuteReleasesGuardOnSuccess(t *testing.T) {
	// After Execute returns, the inFlight guard MUST be released so the
	// next Session method runs without "concurrent use".
	sess := requirePGSession(t)
	ctx := context.Background()
	if _, err := sess.Execute(ctx, models.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := sess.Execute(ctx, models.Query{SQL: "SELECT 2"}); err != nil {
		t.Fatalf("second Execute (guard not released?): %v", err)
	}
}

func TestPgExecuteReleasesGuardOnError(t *testing.T) {
	sess := requirePGSession(t)
	ctx := context.Background()
	_, err := sess.Execute(ctx, models.Query{SQL: "SELECT * FROM no_such_table_xyz"})
	if err == nil {
		t.Fatal("expected error; guard release on error path can't be verified")
	}
	// If guard wasn't released on error, this call would panic with
	// "concurrent use" — recover would catch it.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("guard not released on error path; second call panicked: %v", r)
		}
	}()
	if _, err := sess.Execute(ctx, models.Query{SQL: "SELECT 1"}); err != nil {
		t.Fatalf("second Execute after error: %v", err)
	}
}
