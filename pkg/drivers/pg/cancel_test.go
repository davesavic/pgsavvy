//go:build integration

// Integration tests for (*Connection).Cancel and (*Session).SecretKey against
// the docker/postgres fixture. requirePGSession (defined in execute_test.go)
// owns the skip-or-open dance for individual Sessions; for cancel coverage we
// also need access to the *pg.Connection, which is why some tests below
// duplicate the open dance and capture both halves.

package pg_test

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// requirePGConnAndSession is a sibling of requirePGSession that hands back BOTH
// the underlying *pg.Connection (needed to invoke Cancel) and a freshly-opened
// Session, with the same skip-on-missing-fixture semantics.
func requirePGConnAndSession(t *testing.T) (drivers.Connection, drivers.Session) {
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
		Name:   "cancel-test",
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
	return conn, sess
}

func TestPgCancelHasLiveCancelTrue(t *testing.T) {
	// Smoke check: the static capability flag must read true.
	// This does not need a live server — exercised in the unit suite too —
	// but kept here so a future regression that requires server-side state
	// (e.g. pg_cancel_backend authz) surfaces alongside the live tests.
	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !drv.Capabilities().HasLiveCancel {
		t.Fatal("Capabilities.HasLiveCancel = false, want true")
	}
}

func TestPgSessionSecretKeyNonZero(t *testing.T) {
	sess := requirePGSession(t)
	pgsess, ok := sess.(*pg.Session)
	if !ok {
		t.Fatalf("expected *pg.Session, got %T", sess)
	}
	if pgsess.SecretKey() == 0 {
		t.Fatal("Session.SecretKey() = 0, want non-zero (captured from pgconn.PgConn.SecretKey())")
	}
	if pgsess.BackendPID() == 0 {
		t.Fatal("Session.BackendPID() = 0, want non-zero")
	}
}

func TestPgCancelZeroPIDReturnsErrInvalidQueryID(t *testing.T) {
	conn, _ := requirePGConnAndSession(t)
	err := conn.Cancel(context.Background(), models.QueryID{BackendPID: 0})
	if !errors.Is(err, drivers.ErrInvalidQueryID) {
		t.Fatalf("Cancel(BackendPID=0) err = %v, want errors.Is(ErrInvalidQueryID)", err)
	}
}

func TestPgCancelCtxAlreadyCancelled(t *testing.T) {
	conn, _ := requirePGConnAndSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := conn.Cancel(ctx, models.QueryID{BackendPID: 1234})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancel(canceled ctx) err = %v, want errors.Is(context.Canceled)", err)
	}
}

func TestPgCancelInvalidPIDIgnored(t *testing.T) {
	// Cancel with a random, almost-certainly-not-live BackendPID must return
	// nil — pg silently drops cancel-requests it can't match to a backend.
	conn, _ := requirePGConnAndSession(t)
	// Random non-zero PID well outside what a fresh test container would
	// realistically allocate.
	pid := uint32(rand.Int31n(1_000_000_000) + 2_000_000_000) //nolint:gosec // non-cryptographic
	err := conn.Cancel(context.Background(), models.QueryID{BackendPID: pid})
	if err != nil {
		t.Fatalf("Cancel(unknown PID) err = %v, want nil", err)
	}
}

func TestPgCancelTerminatesLongRunningQuery(t *testing.T) {
	// Session A runs SELECT pg_sleep(60) on a dedicated goroutine. The query
	// is single-row so pgx blocks INSIDE Session.Stream (specifically
	// pgxpool.Conn.Query) until the row arrives 60s later — meaning we
	// cannot read stream.QueryID before issuing Cancel. We sidestep that by
	// constructing the QueryID from Session.BackendPID directly, which is
	// the same value Stream would have stamped.
	conn, sess := requirePGConnAndSession(t)
	pgsess, ok := sess.(*pg.Session)
	if !ok {
		t.Fatalf("expected *pg.Session, got %T", sess)
	}
	qid := models.QueryID{BackendPID: pgsess.BackendPID()}
	if qid.BackendPID == 0 {
		t.Fatal("Session.BackendPID = 0; cannot cancel")
	}

	type streamResult struct {
		err error
		dur time.Duration
	}
	done := make(chan streamResult, 1)
	go func() {
		start := time.Now()
		stream, sErr := sess.Stream(context.Background(), models.Query{SQL: "SELECT pg_sleep(60)"})
		// When cancel terminates pg_sleep DURING Stream's underlying
		// pgxpool.Conn.Query call, pgx may return a non-error rows handle
		// whose error surfaces only at Next/Err. Drain one row to coerce
		// pgx into reporting the deferred 57014.
		if sErr == nil && stream != nil {
			_, _, sErr = stream.Next(context.Background())
		}
		if stream != nil {
			_ = stream.Close()
		}
		done <- streamResult{err: sErr, dur: time.Since(start)}
	}()

	// Brief settle so pg_sleep is registered on the backend before we cancel.
	time.Sleep(250 * time.Millisecond)

	cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCancel()
	if err := conn.Cancel(cancelCtx, qid); err != nil {
		t.Fatalf("Connection.Cancel: %v", err)
	}

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatalf("Stream returned nil err after Cancel (dur=%v); want 57014", res.dur)
		}
		var qe *drivers.QueryError
		if !errors.As(res.err, &qe) {
			t.Fatalf("Stream err = %v (%T), want *drivers.QueryError carrying 57014", res.err, res.err)
		}
		if qe.Code != "57014" {
			t.Errorf("QueryError.Code = %q, want 57014 (query_canceled)", qe.Code)
		}
		if res.dur > time.Second {
			t.Errorf("Cancel took %v to surface; want < 1s", res.dur)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel did not terminate pg_sleep within 3s")
	}
}

func TestPgCancelIdempotent(t *testing.T) {
	// Two simultaneous Cancel calls for the SAME QueryID must each return
	// nil — pg silently accepts duplicates, and our Cancel must not serialize
	// or error on the second one. We exercise on an IDLE session (no query
	// actually in flight) so the test does not depend on race-window timing.
	conn, sess := requirePGConnAndSession(t)

	pgsess, ok := sess.(*pg.Session)
	if !ok {
		t.Fatalf("expected *pg.Session, got %T", sess)
	}
	qid := models.QueryID{BackendPID: pgsess.BackendPID()}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = conn.Cancel(context.Background(), qid)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Cancel #%d err = %v, want nil (idempotent)", i, err)
		}
	}
}

func TestPgCancelDuringSessionClose(t *testing.T) {
	// Close + concurrent Cancel must not error: the registry entry may have
	// been removed mid-flight by Session.Close, but Cancel treats an unknown
	// PID as best-effort (writes cancel-request with secretKey=0, which pg
	// silently drops) and returns nil. The session is closed BEFORE this
	// helper returns — the defer chain in t.Cleanup is a no-op for an
	// already-closed session.
	conn, sess := requirePGConnAndSession(t)
	pgsess, ok := sess.(*pg.Session)
	if !ok {
		t.Fatalf("expected *pg.Session, got %T", sess)
	}
	qid := models.QueryID{BackendPID: pgsess.BackendPID()}

	var wg sync.WaitGroup
	var closeErr, cancelErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		closeErr = sess.Close()
	}()
	go func() {
		defer wg.Done()
		cancelErr = conn.Cancel(context.Background(), qid)
	}()
	wg.Wait()

	if closeErr != nil {
		t.Errorf("Session.Close err = %v, want nil", closeErr)
	}
	if cancelErr != nil {
		t.Errorf("Cancel during Close err = %v, want nil", cancelErr)
	}
}
