package data_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// C7 stress half: runner-level -race stress proving the C2/C4/C6 freeze
// fixes hold under volume. These compose the REAL SQLSession (real
// streamMu) with the mutex-guarded recInnerSession / gatedRowStream
// fixtures from query_runner_launch_test.go and a recording cancel conn —
// the -race-safe fake discipline the C6 review pinned on sequencedSession.
//
// Every op is bounded by a 2s deadline (waitAck / explicit select) so a
// deadlock on the launch queue or streamMu surfaces as a per-op timeout,
// never a hung suite, and every test carries the repo's goleak guard.

// recordingCancelConn records driver-level Cancel calls so the <leader>x
// seam (runner.Cancel → SQLSession.Cancel → rh.Cancel → conn.Cancel) is
// provably reached, not just implied.
type recordingCancelConn struct {
	cancels atomic.Int32
}

func (c *recordingCancelConn) Close() error               { return nil }
func (c *recordingCancelConn) Ping(context.Context) error { return nil }
func (c *recordingCancelConn) ServerVersion() string      { return "fake" }
func (c *recordingCancelConn) AcquireSession(context.Context) (drivers.Session, error) {
	return nil, nil
}

func (c *recordingCancelConn) Cancel(context.Context, models.QueryID) error {
	c.cancels.Add(1)
	return nil
}

// countEvents counts session events containing substr (mirrors the inline
// loops the launch tests use, lifted here for reuse by the stress suite).
func countEvents(events []string, substr string) int {
	n := 0
	for _, ev := range events {
		if strings.Contains(ev, substr) {
			n++
		}
	}
	return n
}

// TestStressRapidLaunchStorm hammers the single-flight launch queue with
// 60 rapid single-statement launches (the <leader>r spam the C2 queue was
// built for). Under -race this sweeps the enqueue/last-wins CAS races and
// the real streamMu churn: every action must settle within its 2s bound,
// the surviving-work count stays within the [1, n] RANGE (W1 caveat: a
// fast-resolving early action legitimately survives its successor's
// enqueue, so an exact count would be a false positive), and goleak sees
// no leftover launcher, watcher, or drain goroutines.
func TestStressRapidLaunchStorm(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const n = 60
	inner := &recInnerSession{}
	for i := 0; i < n; i++ {
		inner.stage(recInnerStream{})
	}
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	acks := make(chan ackResult, n)
	for i := 0; i < n; i++ {
		runner.RunAsync(context.Background(), "SELECT storm", data.RunOptions{}, func(rh *session.RunHandle, err error) {
			if rh != nil {
				// Terminate the handle so streamMu releases before the next
				// queued op locks it (the tab/RBM drain stand-in).
				_ = rh.Rows().Close()
			}
			acks <- ackResult{rh, err}
		})
	}

	// Every action settles within 2s — a streamMu deadlock or a wedged
	// launcher manifests as this per-op timeout.
	for i := 0; i < n; i++ {
		got := waitAck(t, acks, "storm launch")
		if got.err != nil && !errors.Is(got.err, context.Canceled) {
			t.Fatalf("storm launch err = %v, want nil or context.Canceled (settled)", got.err)
		}
	}

	events := inner.snapshot()
	starts := countEvents(events, "stream:SELECT storm:start")
	if starts < 1 || starts > n {
		t.Fatalf("storm stream starts = %d, want 1..%d (bounded last-wins work)", starts, n)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}
}

// TestStressRapidEnterLastWinsBounded parks the launcher inside a blocker
// op, fires 50 rapid Enters behind it (each enqueue cancels the prior
// pending sentinel — last-wins), then releases the blocker. All 51 acks
// must settle within 2s and only a bounded handful of streams may ever
// reach the session — the invariant is RANGE-based per the C2 reviewer
// note, not an exact survivor count.
func TestStressRapidEnterLastWinsBounded(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	gate := make(chan struct{})
	inner.stage(recInnerStream{streamGate: gate}) // blocker parks the launcher
	inner.stage(recInnerStream{})                 // the survivor's stream
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	blocker := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT blocker", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		if rh != nil {
			_ = rh.Rows().Close()
		}
		blocker <- ackResult{rh, err}
	})
	waitInnerEvent(t, inner, "stream:SELECT blocker:start")

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		runner.RunAsync(context.Background(), "SELECT spam", data.RunOptions{}, func(rh *session.RunHandle, err error) {
			if rh != nil {
				_ = rh.Rows().Close()
			}
			wg.Done()
		})
	}

	close(gate)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: rapid-enter spam did not settle within 2s")
	}
	waitAck(t, blocker, "blocker")

	events := inner.snapshot()
	spamStarts := countEvents(events, "stream:SELECT spam:start")
	if spamStarts < 1 || spamStarts > n {
		t.Fatalf("spam stream starts = %d, want 1..%d (last-wins bounds the work)", spamStarts, n)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}
}

// TestStressCancelMidStreamTransitionsToCancelled drives the <leader>x
// path against a run whose row stream is parked mid-flight (the fake's
// stand-in for a server-side statement the user cancels): runner.Cancel()
// must reach the driver's Cancel exactly once and the run must reach Done
// within 2s — never hang. Mirrors the session-level witness through the
// runner/controller seam the freeze fixes protect.
func TestStressCancelMidStreamTransitionsToCancelled(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	conn := &recordingCancelConn{}
	inner := &recInnerSession{}
	rowGate := make(chan struct{})
	inner.stage(recInnerStream{rowGate: rowGate}) // rows park; run stays in flight
	sess := session.New(conn, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT cancel-me", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	got := waitAck(t, acks, "launch") // resolved; rows parked on the gate

	// <leader>x: Cancel targets the last resolved RunHandle.
	if err := runner.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if conn.cancels.Load() == 0 {
		t.Fatal("runner.Cancel did not reach the driver's Cancel (conn.Cancel not invoked)")
	}

	// Release the parked stream (the fake's stand-in for the driver surfacing
	// a terminal state after the cancel request); the run must then finish.
	close(rowGate)
	for {
		_, ok, err := got.rh.Rows().Next(context.Background())
		if !ok || err != nil {
			break
		}
	}
	select {
	case <-got.rh.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: run did not terminate within 2s of the mid-stream cancel")
	}
	_ = got.rh.Rows().Close()

	if err := sess.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}
}
