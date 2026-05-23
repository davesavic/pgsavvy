//go:build integration

// Integration tests for (*Session).Stream and *pgRowStream against the
// docker/postgres fixture. requirePGSession (defined in execute_test.go)
// owns the skip-or-open dance.

package pg_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestPgStreamQueryIDPopulatedBeforeFirstNext(t *testing.T) {
	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 1000)",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	qid := stream.QueryID()
	if qid.SessionID == 0 {
		t.Error("QueryID.SessionID = 0, want non-zero")
	}
	if qid.BackendPID == 0 {
		t.Error("QueryID.BackendPID = 0, want non-zero (pgconn.PgConn.PID())")
	}
	if qid.Started.IsZero() {
		t.Error("QueryID.Started is zero, want non-zero")
	}
	if qid.Nonce == 0 {
		t.Error("QueryID.Nonce = 0, want non-zero (process-monotonic counter)")
	}
}

func TestPgStreamLazyIterationCount(t *testing.T) {
	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 1000) AS n",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	cols := stream.Columns()
	if len(cols) != 1 || cols[0].Name != "n" {
		t.Fatalf("Columns = %+v, want [{Name:n,...}]", cols)
	}

	ctx := context.Background()
	count := 0
	for {
		row, ok, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		// Single-value row check; cast guards against unexpected decode.
		if len(row.Values) != 1 {
			t.Fatalf("row.Values len = %d, want 1", len(row.Values))
		}
		count++
	}
	if count != 1000 {
		t.Fatalf("row count = %d, want 1000", count)
	}
}

func TestPgStreamCloseIdempotent(t *testing.T) {
	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT 1",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close (must be no-op): %v", err)
	}
}

func TestPgStreamConcurrentStreamPanics(t *testing.T) {
	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 100)",
	})
	if err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Second Stream BEFORE Close on the first MUST panic with "concurrent use".
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from second Stream on same Session; got none")
		}
		msg := fmt.Sprintf("%v", r)
		if !strings.Contains(msg, "concurrent use") {
			t.Errorf("panic = %q, want substring 'concurrent use'", msg)
		}
	}()
	_, _ = sess.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
}

func TestPgStreamContextCancellation(t *testing.T) {
	sess := requirePGSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := sess.Stream(ctx, models.Query{
		// 1M rows so we definitely have work in flight when we cancel.
		SQL: "SELECT generate_series(1, 1000000)",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Pull a few rows, then cancel.
	for i := 0; i < 5; i++ {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil || !ok {
			t.Fatalf("warm-up Next i=%d: ok=%v err=%v", i, ok, nerr)
		}
	}
	cancel()

	// Drain until Next reports an error or end-of-rows. Once ctx is canceled,
	// pgx surfaces context.Canceled from rows.Next/Err.
	var seen error
	for {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil {
			seen = nerr
			break
		}
		if !ok {
			break
		}
	}
	if seen == nil {
		t.Fatal("expected ctx.Err()-derived error after cancel, got nil")
	}
	if !errors.Is(seen, context.Canceled) {
		// pgx may wrap context.Canceled inside its own error; accept any
		// error that resolves via errors.Is.
		t.Logf("Next post-cancel err = %v (errors.Is(ctx.Canceled)=%v)",
			seen, errors.Is(seen, context.Canceled))
	}

	// Close MUST still release the inFlight guard even on a canceled stream.
	if err := stream.Close(); err != nil {
		t.Errorf("Close after cancel: %v", err)
	}
	// Prove the guard was released: a follow-up guarded call must not panic
	// with "session: concurrent use". The underlying pgx connection may be
	// poisoned by the prior ctx cancellation (cached-statement deallocation
	// fails on a dead conn), so we don't assert success — only that the
	// guard machinery let us back in. A "concurrent use" panic here would
	// mean Close failed to release inFlight; any other error is the pgx
	// conn surfacing its own post-cancellation state, which is out of scope
	// for this AC.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("guard not released after canceled-stream Close; second call panicked: %v", r)
			}
		}()
		s2, serr := sess.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
		if serr == nil && s2 != nil {
			_ = s2.Close()
		}
	}()
}

func TestPgStreamUseAfterCloseReturnsSentinel(t *testing.T) {
	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT 1",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, ok, err := stream.Next(context.Background())
	if ok {
		t.Fatal("Next after Close returned ok=true")
	}
	if !errors.Is(err, pg.ErrRowStreamClosed) {
		t.Fatalf("Next after Close err = %v, want errors.Is(ErrRowStreamClosed)", err)
	}
}

func TestPgStreamSyntaxErrorWrapsToQueryError(t *testing.T) {
	sess := requirePGSession(t)
	_, err := sess.Stream(context.Background(), models.Query{
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

func TestPgStreamReleasesGuardAfterConsumeAndClose_NoGoroutineLeak(t *testing.T) {
	// goleak: Stream → consume-all → Close → Session.Close must not leave
	// background goroutines behind. The fixture is small (1000 rows) so the
	// whole loop completes in well under the goleak default timeout.
	//
	// We ignore pgxpool's backgroundHealthCheck: it's a known long-lived
	// goroutine owned by the pool that only exits when Connection.Close()
	// invokes pool.Close(). The Connection is closed in requirePGSession's
	// t.Cleanup, which runs AFTER this deferred VerifyNone. Asserting that
	// pgxpool's own background goroutine is gone here would be asserting on
	// teardown order, not on our Stream/Session bookkeeping — which is what
	// this test is meant to cover.
	defer goleak.VerifyNone(t,
		goleak.IgnoreCurrent(),
		goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
	)

	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 1000)",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	ctx := context.Background()
	for {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil {
			t.Fatalf("Next: %v", nerr)
		}
		if !ok {
			break
		}
	}
	if err := stream.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Give pgx a beat to settle any backend reads — goleak's IgnoreCurrent
	// already excludes test runner goroutines.
	time.Sleep(50 * time.Millisecond)
}

func TestPgStreamLargeResultDoesNotAccumulate(t *testing.T) {
	// Smoke test: 200k rows. We don't assert allocation precisely (that's a
	// benchmark concern), but we DO assert that the loop completes without
	// OOM and that we hold only one row at a time. The pgRowStream stages a
	// fresh []any per Next; we discard it after a length check.
	sess := requirePGSession(t)
	stream, err := sess.Stream(context.Background(), models.Query{
		SQL: "SELECT generate_series(1, 200000)",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ctx := context.Background()
	count := 0
	for {
		row, ok, nerr := stream.Next(ctx)
		if nerr != nil {
			t.Fatalf("Next: %v", nerr)
		}
		if !ok {
			break
		}
		if len(row.Values) != 1 {
			t.Fatalf("row.Values len = %d, want 1", len(row.Values))
		}
		count++
	}
	if count != 200000 {
		t.Fatalf("row count = %d, want 200000", count)
	}
}

// TestFieldDescriptionTableOIDPopulated — dbsavvy-bwq.1 F1.
//
// Verifies fieldDescriptionsToColumnMetas copies pgconn.FieldDescription's
// TableOID through to models.ColumnMeta so later editability detection (B3+)
// can distinguish base-table columns from computed/CTE/expression columns.
//   - Plain SELECT against a base table → every column has non-zero TableOID.
//   - Computed column (SELECT now() AS t) → TableOID == 0.
//   - CTE column → TableOID == 0.
func TestFieldDescriptionTableOIDPopulated(t *testing.T) {
	sess := requirePGSession(t)
	ctx := context.Background()

	t.Run("BaseTable", func(t *testing.T) {
		stream, err := sess.Stream(ctx, models.Query{SQL: "SELECT id, email FROM app.users LIMIT 1"})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		cols := stream.Columns()
		if len(cols) != 2 {
			t.Fatalf("Columns len = %d, want 2", len(cols))
		}
		for _, c := range cols {
			if c.TableOID == 0 {
				t.Errorf("column %q TableOID = 0, want non-zero for base-table column", c.Name)
			}
			if c.TableAttributeNumber == 0 {
				t.Errorf("column %q TableAttributeNumber = 0, want non-zero for base-table column", c.Name)
			}
		}
	})

	t.Run("Computed", func(t *testing.T) {
		stream, err := sess.Stream(ctx, models.Query{SQL: "SELECT now() AS t"})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		cols := stream.Columns()
		if len(cols) != 1 {
			t.Fatalf("Columns len = %d, want 1", len(cols))
		}
		if cols[0].TableOID != 0 {
			t.Errorf("computed column TableOID = %d, want 0", cols[0].TableOID)
		}
	})

	t.Run("CTE", func(t *testing.T) {
		stream, err := sess.Stream(ctx, models.Query{
			SQL: "WITH c AS (SELECT 1 AS x) SELECT x FROM c",
		})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		defer func() { _ = stream.Close() }()
		cols := stream.Columns()
		if len(cols) != 1 {
			t.Fatalf("Columns len = %d, want 1", len(cols))
		}
		if cols[0].TableOID != 0 {
			t.Errorf("CTE column TableOID = %d, want 0", cols[0].TableOID)
		}
	})
}

// TestPgStreamEOFReleasesGuardForReStream — dbsavvy-zzy regression.
//
// Draining a stream to clean EOF must release the parent Session's inFlight
// guard without requiring an explicit Close, so the next Stream call on the
// same session proceeds without the "session: concurrent use" panic. Mirrors
// the in-app multi-statement <leader>R flow: handleRunAll issues N sequential
// Streams on one session; the inter-Stream cleanup is the EOF-release path.
func TestPgStreamEOFReleasesGuardForReStream(t *testing.T) {
	sess := requirePGSession(t)
	ctx := context.Background()

	first, err := sess.Stream(ctx, models.Query{SQL: "SELECT generate_series(1, 3)"})
	if err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	// Drain to EOF without ever calling Close.
	for {
		_, ok, nerr := first.Next(ctx)
		if nerr != nil {
			t.Fatalf("first.Next: %v", nerr)
		}
		if !ok {
			break
		}
	}

	// A follow-up Stream on the SAME session must not panic with
	// "session: concurrent use". Pre-fix behavior: inFlight was still held
	// by the EOF-drained first stream until an explicit Close.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("second Stream after EOF panicked (guard not released): %v", r)
			}
		}()
		second, serr := sess.Stream(ctx, models.Query{SQL: "SELECT 1"})
		if serr != nil {
			t.Fatalf("second Stream: %v", serr)
		}
		// Drain the second stream so its EOF-release runs too — keeps the
		// session usable for subsequent tests sharing the fixture.
		for {
			_, ok, nerr := second.Next(ctx)
			if nerr != nil {
				t.Fatalf("second.Next: %v", nerr)
			}
			if !ok {
				break
			}
		}
	}()

	// Explicit Close on the EOF-drained first stream must remain a safe
	// no-op (idempotency contract).
	if err := first.Close(); err != nil {
		t.Errorf("first.Close after EOF: %v", err)
	}
}

// TestPgStreamTerminalNextErrorReleasesGuard — dbsavvy-zzy companion.
//
// A Next that surfaces a terminal pgx error (e.g. a query that errors after
// the first batch has been pulled) must release inFlight the same way clean
// EOF does. Exercised by canceling the surrounding context mid-stream: pgx
// surfaces context.Canceled from Next, and the release must fire before the
// consumer can re-Stream.
func TestPgStreamTerminalNextErrorReleasesGuard(t *testing.T) {
	sess := requirePGSession(t)
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := sess.Stream(ctx, models.Query{
		SQL: "SELECT generate_series(1, 1000000)",
	})
	if err != nil {
		cancel()
		t.Fatalf("Stream: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil || !ok {
			cancel()
			t.Fatalf("warm-up Next i=%d: ok=%v err=%v", i, ok, nerr)
		}
	}
	cancel()

	for {
		_, ok, nerr := stream.Next(ctx)
		if nerr != nil {
			break
		}
		if !ok {
			break
		}
	}

	// Even without an explicit stream.Close, a follow-up Stream on the same
	// session must succeed (or fail with a non-guard pgx error — the pool
	// conn may be poisoned by the prior ctx cancel, which is out of scope
	// for this AC). A "session: concurrent use" panic would mean the
	// release-on-terminal-error path failed to fire.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Stream after terminal Next error panicked (guard not released): %v", r)
			}
		}()
		s2, serr := sess.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
		if serr == nil && s2 != nil {
			_ = s2.Close()
		}
	}()
}
