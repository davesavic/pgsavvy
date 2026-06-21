//go:build integration

// Integration tests for (*Session).LiveTxStatus against the docker/postgres
// fixture. Exercises raw-SQL BEGIN/COMMIT/ROLLBACK over BOTH the Stream path
// (the query-editor path) and the Execute path, asserting the sampled pgconn
// status byte drives LiveTxStatus. requirePGSession (execute_test.go) owns the
// skip-or-open dance.

package pg_test

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// drainStream runs q via Stream and consumes it to EOF, then closes it. The
// EOF / Close release path samples the live tx status. Fatals on any error.
func drainStream(t *testing.T, sess drivers.Session, sql string) {
	t.Helper()
	ctx := context.Background()
	stream, err := sess.Stream(ctx, models.Query{SQL: sql})
	if err != nil {
		t.Fatalf("Stream(%q): %v", sql, err)
	}
	for {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil {
			_ = stream.Close()
			t.Fatalf("Next(%q): %v", sql, nerr)
		}
		if !ok {
			break
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close(%q): %v", sql, err)
	}
}

// streamExpectErr runs q via Stream and consumes it expecting a terminal error
// (e.g. the deliberate divide-by-zero that aborts the transaction). It still
// closes the stream so the inFlight guard and sampler fire.
func streamExpectErr(t *testing.T, sess drivers.Session, sql string) {
	t.Helper()
	ctx := context.Background()
	stream, err := sess.Stream(ctx, models.Query{SQL: sql})
	if err != nil {
		// Some errors surface at Stream() rather than Next(); that still
		// transitions the backend into the failed-tx state.
		return
	}
	for {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil {
			break
		}
		if !ok {
			break
		}
	}
	_ = stream.Close()
}

// TestPgLiveTxStatusRawSQLViaStream walks the four raw-SQL transitions over the
// Stream path: BEGIN → active, failing stmt → aborted, ROLLBACK → none, and a
// fresh BEGIN → COMMIT → none.
func TestPgLiveTxStatusRawSQLViaStream(t *testing.T) {
	sess := requirePGSession(t)

	// Outside any tx.
	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("pre-BEGIN LiveTxStatus = %q, want \"\"", status)
	}

	// BEGIN then a SELECT inside the tx → active.
	drainStream(t, sess, "BEGIN")
	drainStream(t, sess, "SELECT 1")
	if status, sps := sess.LiveTxStatus(); status != models.TxActive {
		t.Fatalf("in-tx LiveTxStatus = %q (savepoints=%v), want %q", status, sps, models.TxActive)
	} else if sps != nil {
		t.Fatalf("raw-SQL tx savepoints = %v, want nil (D4)", sps)
	}

	// A failing statement inside the tx → aborted_in_tx.
	streamExpectErr(t, sess, "SELECT 1/0")
	if status, _ := sess.LiveTxStatus(); status != models.TxAbortedInTx {
		t.Fatalf("post-error LiveTxStatus = %q, want %q", status, models.TxAbortedInTx)
	}

	// ROLLBACK clears it.
	drainStream(t, sess, "ROLLBACK")
	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("post-ROLLBACK LiveTxStatus = %q, want \"\"", status)
	}

	// Fresh BEGIN → COMMIT clears it.
	drainStream(t, sess, "BEGIN")
	if status, _ := sess.LiveTxStatus(); status != models.TxActive {
		t.Fatalf("second BEGIN LiveTxStatus = %q, want %q", status, models.TxActive)
	}
	drainStream(t, sess, "COMMIT")
	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("post-COMMIT LiveTxStatus = %q, want \"\"", status)
	}
}

// TestPgLiveTxStatusStreamQueryTimeError is a regression test for the case
// where a statement fails at Query() time (a parse/describe error such as
// undefined_table 42P01) rather than at Next(). Such errors return early from
// Session.Stream BEFORE a RowStream exists, so release()'s sampler never runs;
// the early-return path must sample explicitly or the [TX*] badge goes stale at
// the prior 'T'. Found via live tmux verification: a failing SELECT in the
// editor left the badge showing [TX] instead of [TX*].
func TestPgLiveTxStatusStreamQueryTimeError(t *testing.T) {
	sess := requirePGSession(t)
	ctx := context.Background()

	drainStream(t, sess, "BEGIN")
	if status, _ := sess.LiveTxStatus(); status != models.TxActive {
		t.Fatalf("in-tx LiveTxStatus = %q, want %q", status, models.TxActive)
	}

	// Undefined table → pgx returns the error from Stream() itself (parse-time),
	// taking the early-return path that must still sample the aborted status.
	stream, err := sess.Stream(ctx, models.Query{SQL: "SELECT * FROM definitely_no_such_table_zzz"})
	if err == nil {
		_ = stream.Close()
		t.Fatal("Stream(undefined table): expected a Query()-time error, got nil")
	}
	if status, _ := sess.LiveTxStatus(); status != models.TxAbortedInTx {
		t.Fatalf("post-query-time-error LiveTxStatus = %q, want %q (the bug: stale [TX])", status, models.TxAbortedInTx)
	}

	drainStream(t, sess, "ROLLBACK")
	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("post-ROLLBACK LiveTxStatus = %q, want \"\"", status)
	}
}

// TestPgLiveTxStatusRawSQLViaExecute mirrors the transitions over the Execute
// path (materialized result rather than a streamed one). Execute samples on its
// deferred return path.
func TestPgLiveTxStatusRawSQLViaExecute(t *testing.T) {
	sess := requirePGSession(t)
	ctx := context.Background()

	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("pre-BEGIN LiveTxStatus = %q, want \"\"", status)
	}

	if _, err := sess.Execute(ctx, models.Query{SQL: "BEGIN"}); err != nil {
		t.Fatalf("Execute BEGIN: %v", err)
	}
	if status, _ := sess.LiveTxStatus(); status != models.TxActive {
		t.Fatalf("in-tx LiveTxStatus = %q, want %q", status, models.TxActive)
	}

	// Failing statement → aborted (Execute returns an error; the backend is
	// now in the failed-tx state and Execute's deferred sample captures 'E').
	if _, err := sess.Execute(ctx, models.Query{SQL: "SELECT 1/0"}); err == nil {
		t.Fatal("Execute SELECT 1/0: expected error, got nil")
	}
	if status, _ := sess.LiveTxStatus(); status != models.TxAbortedInTx {
		t.Fatalf("post-error LiveTxStatus = %q, want %q", status, models.TxAbortedInTx)
	}

	if _, err := sess.Execute(ctx, models.Query{SQL: "ROLLBACK"}); err != nil {
		t.Fatalf("Execute ROLLBACK: %v", err)
	}
	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("post-ROLLBACK LiveTxStatus = %q, want \"\"", status)
	}

	if _, err := sess.Execute(ctx, models.Query{SQL: "BEGIN"}); err != nil {
		t.Fatalf("Execute second BEGIN: %v", err)
	}
	if _, err := sess.Execute(ctx, models.Query{SQL: "COMMIT"}); err != nil {
		t.Fatalf("Execute COMMIT: %v", err)
	}
	if status, _ := sess.LiveTxStatus(); status != "" {
		t.Fatalf("post-COMMIT LiveTxStatus = %q, want \"\"", status)
	}
}
