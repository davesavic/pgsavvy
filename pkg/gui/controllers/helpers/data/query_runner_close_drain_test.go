package data_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// This file covers the C6 shutdown/quit hardening on the launch queue:
// Unbind cancels the pending sentinel, the close-time drain cancels
// queued sentinels and waits for the launcher to go idle. The
// quit-during-ANALYZE safety lives in query_runner_quit_analyze_test.go
// (same package).

// TestUnbindCancelsPendingSentinel is C6 gap #1: Unbind must cancel the
// pending launch sentinel BEFORE dropping the binding, so a queued launch
// aborts via its ctx (context.Canceled) instead of running to
// ErrNoSession against the dead binding. The session records no call for
// the queued statement.
func TestUnbindCancelsPendingSentinel(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gate := make(chan struct{})
	gs.stage(gateStagedStream{gate: gate}) // A parks the launcher
	gs.stage(gateStagedStream{})           // B is queued behind A
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	a := make(chan ackResult, 1)
	b := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT A", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		a <- ackResult{rh, err}
	})
	waitForEvent(t, gs, "stream:SELECT A:start")
	r.RunAsync(context.Background(), "SELECT B", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		b <- ackResult{rh, err}
	})

	// Unbind cancels B's pending sentinel (and abandons A's in-flight one)
	// before the binding is dropped.
	r.Unbind()

	close(gate)
	if got := waitAck(t, b, "B (queued at Unbind)"); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("B ack err = %v, want context.Canceled (sentinel cancelled by Unbind)", got.err)
	}
	// The queued statement never touched the session.
	for _, ev := range gs.snapshot() {
		if strings.HasPrefix(ev, "stream:SELECT B") {
			t.Fatalf("queued launch B still reached the session: %v", gs.snapshot())
		}
	}
	_ = waitAck(t, a, "A")
}

// TestCancelQueuedAndWaitIdleCancelsQueuedAndWaitsIdle is C6 gap #3: the
// close-time drain cancels every pending/queued sentinel and waits for the
// launcher to go idle within the bound. A queued launch never executes
// against a session that is about to die, and no ack is pending into the
// dead UI afterwards.
func TestCancelQueuedAndWaitIdleCancelsQueuedAndWaitsIdle(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gate := make(chan struct{})
	gs.stage(gateStagedStream{gate: gate}) // A parks the launcher
	gs.stage(gateStagedStream{})           // B queued behind A
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	a := make(chan ackResult, 1)
	b := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT A", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		a <- ackResult{rh, err}
	})
	waitForEvent(t, gs, "stream:SELECT A:start")
	r.RunAsync(context.Background(), "SELECT B", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		b <- ackResult{rh, err}
	})

	// The drain cancels both sentinels and waits for the launcher to go
	// idle. A's op is parked on the gate, so the wait must still be
	// blocked at this point.
	drained := make(chan error, 1)
	go func() { drained <- r.CancelQueuedAndWaitIdle(2 * time.Second) }()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("drain returned early err = %v, want nil after idle", err)
		}
		t.Fatal("drain returned before the launcher went idle")
	case <-time.After(250 * time.Millisecond):
		// still waiting — launcher parked on A. Good.
	}

	close(gate) // A's op resolves; the launcher then aborts B and exits.
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("drain err = %v, want nil (launcher idle)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after the launcher went idle")
	}

	// B never touched the session.
	for _, ev := range gs.snapshot() {
		if strings.HasPrefix(ev, "stream:SELECT B") {
			t.Fatalf("queued launch B still reached the session: %v", gs.snapshot())
		}
	}
	// Both launches were cancelled.
	if got := waitAck(t, a, "A"); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("A ack err = %v, want context.Canceled", got.err)
	}
	if got := waitAck(t, b, "B"); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("B ack err = %v, want context.Canceled", got.err)
	}
}

// TestQuitDuringLaunchGapCommitsOnlyAfterDrain is the C2 contract under
// the quit flow: a launch parked in the pending gap must fully drain
// (sentinel Done spans resolution + RunHandle termination) before the
// commit lands. CancelAndWaitActiveRun blocks through the gap; only after
// the row stream terminates does the commitAndQuit sequence proceed.
func TestQuitDuringLaunchGapCommitsOnlyAfterDrain(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	streamGate := make(chan struct{})
	rowGate := make(chan struct{})
	inner.stage(recInnerStream{streamGate: streamGate, rowGate: rowGate})
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	// The quit dialog premise: a user transaction is open.
	utx, err := runner.Begin(context.Background(), models.TxOptions{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// The launch parks inside the pending gap (Stream call blocked on
	// streamGate).
	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT parked", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	waitInnerEvent(t, inner, "stream:SELECT parked:start")

	// commitAndQuit sequence: CancelAndWaitActiveRun then CurrentTransaction
	// then Commit. Runs on a goroutine; we assert Commit is withheld until
	// the run has fully drained.
	committed := make(chan struct{})
	go func() {
		defer close(committed)
		runner.CancelAndWaitActiveRun()
		tx := runner.CurrentTransaction()
		if tx == nil {
			return
		}
		_ = tx.Commit(context.Background())
	}()

	// While the row stream is still live, the commit must be withheld.
	select {
	case <-committed:
		t.Fatal("Commit landed before the run fully drained (row stream still live)")
	case <-time.After(250 * time.Millisecond):
	}

	// Release the stream op; the row stream then reaches EOF on its own
	// (gatedRowStream returns EOF after the gate), finishing the run.
	close(streamGate)
	close(rowGate)
	got := waitAck(t, acks, "parked launch")
	if got.err != nil || got.rh == nil {
		t.Fatalf("ack = (%v, %v), want clean rh", got.rh, got.err)
	}
	_ = got.rh.Rows().Close() // terminate the run like the abandoned path does

	select {
	case <-committed:
	case <-time.After(2 * time.Second):
		t.Fatal("Commit did not land after the run drained")
	}

	// The user tx is the one that was committed — never the runner's wrap.
	rtx, ok := utx.(*recTx)
	if !ok {
		t.Fatalf("Begin returned %T, want *recTx", utx)
	}
	if !rtx.committed.Load() {
		t.Fatal("user tx was not committed after the run drained")
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
}
