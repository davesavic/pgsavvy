//go:build integration

// Integration tests for the per-Session NoticeRouter against the
// docker/postgres fixture. Skipped (not failed) when DBSAVVY_TEST_PG is unset.
//
// See epic dbsavvy-66p.5 — NOTICE / WARNING / INFO routing via
// pgconn.Config.OnNotice → NoticeRouter → (*Session).AttachNotice channel.

package pg_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// openPGConn opens a fresh *pg.Connection bound to t.Cleanup. Sibling of
// requirePGConnAndSession (cancel_test.go) but does NOT pre-acquire a Session;
// notice tests want full control over Session lifetime to exercise
// AttachNotice / Close / unsubscribe behavior.
func openPGConn(t *testing.T) drivers.Connection {
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
		Name:   "notice-test",
		Driver: "postgres",
		DSN:    dsn,
	}, nil)
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// acquirePGSession is a Session-only helper that wraps Connection.AcquireSession
// and registers Close on t.Cleanup. The returned *pg.Session exposes the notice
// surface (AttachNotice, DroppedNotices) that drivers.Session does not.
func acquirePGSession(t *testing.T, conn drivers.Connection) *pg.Session {
	t.Helper()
	sess, err := conn.AcquireSession(context.Background())
	if err != nil {
		t.Fatalf("AcquireSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	pgs, ok := sess.(*pg.Session)
	if !ok {
		t.Fatalf("expected *pg.Session, got %T", sess)
	}
	return pgs
}

// raiseNotice executes a DO block on s that raises a single notice with the
// given severity and message. Severity must be one of NOTICE / WARNING / INFO.
func raiseNotice(t *testing.T, s *pg.Session, severity, msg string) {
	t.Helper()
	sql := "DO $$ BEGIN RAISE " + severity + " '" + msg + "'; END $$"
	if _, err := s.Execute(context.Background(), models.Query{SQL: sql}); err != nil {
		t.Fatalf("RAISE %s %q: %v", severity, msg, err)
	}
}

func TestPgNoticeRouterDeliversNoticeToSubscriber(t *testing.T) {
	conn := openPGConn(t)
	sess := acquirePGSession(t, conn)

	ch := make(chan pgconn.Notice, 8)
	sess.AttachNotice(ch)

	raiseNotice(t, sess, "NOTICE", "foo")

	select {
	case n := <-ch:
		if !strings.Contains(n.Message, "foo") {
			t.Fatalf("notice.Message = %q, want substring %q", n.Message, "foo")
		}
		if n.Severity != "NOTICE" {
			t.Fatalf("notice.Severity = %q, want %q", n.Severity, "NOTICE")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no notice delivered within 200ms")
	}
}

func TestPgNoticeRouterPerSessionIsolation(t *testing.T) {
	conn := openPGConn(t)
	a := acquirePGSession(t, conn)
	b := acquirePGSession(t, conn)

	chA := make(chan pgconn.Notice, 8)
	chB := make(chan pgconn.Notice, 8)
	a.AttachNotice(chA)
	b.AttachNotice(chB)

	raiseNotice(t, a, "NOTICE", "only A")

	select {
	case n := <-chA:
		if !strings.Contains(n.Message, "only A") {
			t.Fatalf("A got notice %q, want %q substring", n.Message, "only A")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("A: no notice delivered within 500ms")
	}

	select {
	case n := <-chB:
		t.Fatalf("B unexpectedly received a notice: %+v", n)
	case <-time.After(200 * time.Millisecond):
		// Expected: B's channel stays empty.
	}
}

func TestPgNoticeRouterRoutesWarningAndInfo(t *testing.T) {
	conn := openPGConn(t)
	sess := acquirePGSession(t, conn)

	ch := make(chan pgconn.Notice, 8)
	sess.AttachNotice(ch)

	// INFO is a notice-class severity in PostgreSQL; the OnNotice handler
	// fires for any NoticeResponse / WarningResponse message regardless of
	// severity. We exercise NOTICE / WARNING / INFO in one batch and verify
	// each is delivered with its severity intact.
	cases := []struct{ severity, msg string }{
		{"NOTICE", "n-msg"},
		{"WARNING", "w-msg"},
		{"INFO", "i-msg"},
	}
	for _, c := range cases {
		raiseNotice(t, sess, c.severity, c.msg)
	}

	seen := map[string]string{}
	deadline := time.After(500 * time.Millisecond)
	for len(seen) < len(cases) {
		select {
		case n := <-ch:
			seen[n.Severity] = n.Message
		case <-deadline:
			t.Fatalf("only received %d/%d notices: %v", len(seen), len(cases), seen)
		}
	}
	for _, c := range cases {
		got, ok := seen[c.severity]
		if !ok {
			t.Errorf("no %s notice delivered", c.severity)
			continue
		}
		if !strings.Contains(got, c.msg) {
			t.Errorf("%s message = %q, want substring %q", c.severity, got, c.msg)
		}
	}
}

func TestPgNoticeRouterFullChannelDropsAndCounts(t *testing.T) {
	conn := openPGConn(t)
	sess := acquirePGSession(t, conn)

	// Buffer cap=1: the second RAISE NOTICE in a row must be dropped.
	ch := make(chan pgconn.Notice, 1)
	sess.AttachNotice(ch)

	raiseNotice(t, sess, "NOTICE", "first")
	raiseNotice(t, sess, "NOTICE", "second")

	// Drain whatever the channel holds; we want to assert the COUNTER
	// regardless of which of the two notices made it through.
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no notice delivered within 200ms")
	}

	if got := sess.DroppedNotices(); got < 1 {
		t.Fatalf("DroppedNotices = %d, want >= 1 after a full-channel drop", got)
	}
}

func TestPgNoticeRouterCloseUnsubscribes(t *testing.T) {
	conn := openPGConn(t)

	// Acquire a Session WITHOUT t.Cleanup auto-close — we want to control
	// Close timing precisely here.
	rawSess, err := conn.AcquireSession(context.Background())
	if err != nil {
		t.Fatalf("AcquireSession: %v", err)
	}
	sess := rawSess.(*pg.Session)

	ch := make(chan pgconn.Notice, 8)
	sess.AttachNotice(ch)

	// Fire one notice, drain, then Close. A subsequent notice (raised via a
	// FRESHLY ACQUIRED Session that happens to receive the same pgconn from
	// the pool) must NOT arrive on ch and must NOT panic.
	raiseNotice(t, sess, "NOTICE", "before-close")
	select {
	case <-ch:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("pre-close notice not delivered")
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Drain any residual deliveries (none expected, but flush so the
	// post-close check is unambiguous).
	drained := false
	for !drained {
		select {
		case <-ch:
		case <-time.After(50 * time.Millisecond):
			drained = true
		}
	}

	// Acquire a NEW Session — pgxpool will (with overwhelming probability,
	// MinConns=2) hand us a different pgconn, but even if the pgconn is
	// recycled into our hands, the router's pgconn→sid mapping for the OLD
	// session is gone, so notices route to the NEW session's subscriber (or
	// to nothing if no subscriber). Either way the old ch must remain idle.
	sess2 := acquirePGSession(t, conn)
	raiseNotice(t, sess2, "NOTICE", "after-close")

	select {
	case n := <-ch:
		t.Fatalf("closed-session channel received notice %+v after Close", n)
	case <-time.After(200 * time.Millisecond):
		// Expected.
	}
}

func TestPgNoticeRouterConcurrentSessionsRaceClean(t *testing.T) {
	// Exercises the AC: "No race detector violation when 100 notices fire
	// across N sessions concurrently." The plan AC names 10 sessions, but
	// session.BuildPgxConfig caps MaxConns at 8 (profile.go), so we use
	// sessions=8, noticesPerSession=13 (=104 notices total). Spirit of the
	// AC preserved — the routing fan-in is unchanged. This test is
	// meaningful only under -race; without -race it still verifies
	// functional correctness (every notice routed to its own session's
	// channel).
	conn := openPGConn(t)

	const sessions = 8
	const noticesPerSession = 13

	type bundle struct {
		s  *pg.Session
		ch chan pgconn.Notice
	}
	bundles := make([]bundle, sessions)
	for i := range bundles {
		s := acquirePGSession(t, conn)
		ch := make(chan pgconn.Notice, noticesPerSession+2) // buffer to avoid spurious drops
		s.AttachNotice(ch)
		bundles[i] = bundle{s, ch}
	}

	var wg sync.WaitGroup
	wg.Add(sessions)
	for i := range bundles {
		go func(b bundle, idx int) {
			defer wg.Done()
			for j := 0; j < noticesPerSession; j++ {
				raiseNotice(t, b.s, "NOTICE", "race")
			}
		}(bundles[i], i)
	}
	wg.Wait()

	// Each session must have received exactly noticesPerSession notices on
	// its OWN channel. (Cross-session leakage would manifest as a session
	// receiving fewer than the expected count.)
	for i, b := range bundles {
		got := 0
		drainDeadline := time.After(2 * time.Second)
	drain:
		for got < noticesPerSession {
			select {
			case <-b.ch:
				got++
			case <-drainDeadline:
				break drain
			}
		}
		if got != noticesPerSession {
			t.Errorf("session %d: received %d notices, want %d", i, got, noticesPerSession)
		}
	}
}
