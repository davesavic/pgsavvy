//go:build integration

// Integration tests for pkg/session.SQLSession against the docker/postgres
// fixture. Skipped (not failed) when DBSAVVY_TEST_PG is unset or the probe
// fails — mirrors the pattern in pkg/drivers/pg/execute_test.go.

package session_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

const envDSN = "DBSAVVY_TEST_PG"

// requirePGSQLSession opens a real pg.Connection + Session and wraps it in
// a session.SQLSession. All resources are registered with t.Cleanup.
func requirePGSQLSession(t *testing.T) (*session.SQLSession, drivers.Connection) {
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
		Name:   "sql-session-test",
		Driver: "postgres",
		DSN:    dsn,
	}, nil)
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	inner, err := conn.AcquireSession(ctx)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("acquire session: %v", err)
	}
	s := session.New(conn, inner, session.Options{})
	t.Cleanup(func() {
		_ = s.Close()
		_ = conn.Close()
	})
	return s, conn
}

func TestSQLSessionStream_EndToEndDoneClosesAfterEOF(t *testing.T) {
	s, _ := requirePGSQLSession(t)

	rh, err := s.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 50)",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	ctx := context.Background()
	count := 0
	for {
		_, ok, err := rh.Rows().Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		count++
	}
	if count != 50 {
		t.Errorf("row count = %d, want 50", count)
	}

	select {
	case <-rh.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done did not close after EOF")
	}
	if err := rh.Rows().Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestSQLSessionNoticeFanIn_DeliversRaiseNoticeToActiveRun(t *testing.T) {
	s, _ := requirePGSQLSession(t)

	// Stream runs via the extended query protocol, which rejects multiple
	// commands in one string. RAISE NOTICE needs PL/pgSQL, and the notice
	// only fans into the RunHandle while its rows are being read — so define
	// a session-temp function that raises the notice AND returns a row, then
	// stream a single SELECT over it (one command, with rows to drain).
	if _, err := s.Execute(context.Background(), models.Query{
		SQL: `CREATE FUNCTION pg_temp.sqlsession_notice() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE 'hello from sqlsession'; RETURN 1; END $$`,
	}); err != nil {
		t.Fatalf("create temp notice function: %v", err)
	}

	rh, err := s.Stream(context.Background(), models.Query{
		SQL: `SELECT pg_temp.sqlsession_notice()`,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Drain the stream so finish() fires and the notice channel is
	// closed; range completes once finish closes it.
	ctx := context.Background()
	for {
		_, ok, err := rh.Rows().Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
	}
	// finish() runs the notice flush barrier before closing Done, so every
	// notice the query emitted is already routed into rh.Notices() by the
	// time Done closes — no async wait needed.
	<-rh.Done()

	var got []string
	for n := range rh.Notices() {
		got = append(got, n.Message)
	}
	found := false
	for _, m := range got {
		if m == "hello from sqlsession" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected notice 'hello from sqlsession' not delivered; captured: %v", got)
	}

	_ = rh.Rows().Close()
}

func TestSQLSessionCancelMidStream_DoneClosesAndReturns(t *testing.T) {
	s, _ := requirePGSQLSession(t)

	rh, err := s.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 100000000)",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Pull one row to confirm the stream is live, then Cancel.
	if _, ok, err := rh.Rows().Next(context.Background()); !ok || err != nil {
		t.Fatalf("warmup Next: ok=%v err=%v", ok, err)
	}
	if err := s.Cancel(rh.QueryID()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// Drain until Next reports terminal state; this drives finish() and
	// closes Done.
	for {
		_, ok, err := rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	select {
	case <-rh.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done did not close after Cancel + drain")
	}
	_ = rh.Rows().Close()
}
