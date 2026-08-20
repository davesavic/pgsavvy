package data_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// This file covers the launcher busy-bridge (pgsavvy-446q): SetBusyHold
// installs an acquire/release pair that wraps each queued launch so the
// UI busy spinner animates for the whole statement duration — op stream
// AND RBM drain — even though the launcher runs session ops directly
// (not via OnWorker).

// TestBusyHoldCoversOpStreamAndDrain proves the pairing contract at the
// launcher seam: the acquire fires when the launcher starts a launch and
// the release fires ONLY after the resulting RunHandle terminates. The
// hold is therefore still up while the stream drains after the op has
// resolved, and is released exactly once when the drain completes.
func TestBusyHoldCoversOpStreamAndDrain(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	inner.stage(recInnerStream{}) // Stream() returns a live (open) RunHandle
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	var held, released atomic.Int32
	runner.SetBusyHold(func() bool {
		held.Add(1)
		return true
	}, func() {
		released.Add(1)
	})

	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT held", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	got := waitAck(t, acks, "held launch")
	if got.err != nil || got.rh == nil {
		t.Fatalf("ack = (%v, %v), want clean rh", got.rh, got.err)
	}

	// The op has resolved (rh in hand) but the RunHandle is still open
	// (its drain has not completed). The busy hold must still be held and
	// must NOT yet be released — the spinner stays up through the drain.
	if n := held.Load(); n != 1 {
		t.Fatalf("acquire fired %d times after op resolution, want 1", n)
	}
	if n := released.Load(); n != 0 {
		t.Fatalf("release fired %d times while the RunHandle still drains, want 0", n)
	}

	// Terminate the run (the RBM drain's completion): closing the rows
	// fires finish(), which closes Done — the launcher watcher then
	// releases the hold. Channel-driven: no sleeping.
	_ = got.rh.Rows().Close()

	deadline := time.After(2 * time.Second)
	for released.Load() != 1 {
		select {
		case <-deadline:
			t.Fatalf("release never fired after RunHandle termination (held=%d, released=%d)", held.Load(), released.Load())
		case <-time.After(time.Millisecond):
		}
	}
	if n := held.Load(); n != 1 {
		t.Fatalf("acquire fired %d times total, want exactly 1", n)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
}

// TestBusyHoldNilSafe proves SetBusyHold(nil, nil) clears the bridge and
// a launch with no hooks wired runs clean — the acquire/release sites in
// execLaunch must no-op when nothing is installed.
func TestBusyHoldNilSafe(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	inner.stage(recInnerStream{})
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{})

	// Explicitly clear (both nil) after an initial wire, then run.
	runner.SetBusyHold(func() bool { t.Fatal("cleared acquire fired"); return false }, func() { t.Fatal("cleared release fired") })
	runner.SetBusyHold(nil, nil)

	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT unheld", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	got := waitAck(t, acks, "unheld launch")
	if got.err != nil || got.rh == nil {
		t.Fatalf("ack = (%v, %v), want clean rh", got.rh, got.err)
	}
	_ = got.rh.Rows().Close() // terminate so the watcher + Close settle

	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
}
