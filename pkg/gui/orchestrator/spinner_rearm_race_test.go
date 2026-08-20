package orchestrator_test

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// waitBusyEq polls BusyCount until it settles on want or a deadline
// elapses. BusyCount is an atomic over worker transitions, so a brief 2->1
// (or 1->2) overshoot while two transitions interleave is expected; the
// stable end state is what the rearm invariants are asserted against.
func waitBusyEq(t *testing.T, g interface{ BusyCount() int64 }, want int64) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if got := g.BusyCount(); got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("BusyCount did not settle on %d (last=%d)", want, g.BusyCount())
		case <-time.After(time.Millisecond):
		}
	}
}

// TestSpinnerRearm_InterleaveKeepsOneTickerWhileBusy drives the exact
// 0->1->0->1 window the pre-fix race lived in: worker A's exit transition
// (busy ->0, stop decision) races worker C's entry transition (busy 0->1,
// arm decision). Under the fix both sides are single spinnerMu critical
// sections, so whichever lands first fully completes its transition before
// the other decides — the invariant "exactly one live ticker whenever
// busy>0" cannot break. Under the pre-fix code the losing interleave (A
// decides stop, C arms, A's stopSpinner kills C's ticker) leaves busy==1
// with zero live tickers — this test catches that.
//
// Run under -race -count=5 to sweep scheduler interleavings.
func TestSpinnerRearm_InterleaveKeepsOneTickerWhileBusy(t *testing.T) {
	clk := newFakeClock()
	g, rec := buildTestGuiWithClock(t, clk)
	defer func() { _ = g.Close() }()

	const rounds = 50
	for i := range rounds {
		// Worker A: arms the ticker (busy 0->1) then blocks.
		releaseA := make(chan struct{})
		startedA := make(chan struct{})
		g.OnWorker(func(_ gocui.Task) error {
			close(startedA)
			<-releaseA
			return nil
		})
		<-startedA
		if got := g.BusyCount(); got != 1 {
			t.Fatalf("round %d: BusyCount=%d with A in flight, want 1", i, got)
		}
		if live := clk.liveTickers(); live != 1 {
			t.Fatalf("round %d: liveTickers=%d while busy==1, want 1", i, live)
		}

		// The rearm window: release A, then spin until its decrement lands
		// (BusyCount==0) and IMMEDIATELY start C on this goroutine. On the
		// pre-fix code A has applied busyDelta(-1) but not yet run
		// stopSpinner, so C's entry (busyDelta(+1) → armSpinner) lands
		// inside that window with high probability; when C's arm wins the
		// spinnerMu race, A's stop then kills C's fresh ticker — busy==1
		// with zero live tickers, exactly the bug. On the fixed code both
		// sides are single critical sections: observing busy==0 means A's
		// section is still held or done, and C's entry strictly serializes
		// after it, arming fresh — the invariant cannot break.
		close(releaseA)
		for g.BusyCount() != 0 {
			runtime.Gosched()
		}
		releaseC := make(chan struct{})
		startedC := make(chan struct{})
		g.OnWorker(func(_ gocui.Task) error {
			close(startedC)
			<-releaseC
			return nil
		})
		<-startedC
		// Observe the invariant while busy==1 (both transitions decided),
		// but ASSERT only after the round's workers have drained — a
		// failure inside the loop must not leave a blocked worker behind
		// to hang the deferred Close.
		waitBusyEq(t, g, 1)
		live := clk.liveTickers()
		close(releaseC)
		waitBusyEq(t, g, 0)
		if live != 1 {
			t.Fatalf("round %d: liveTickers=%d after 0->1->0->1 interleave with busy==1, want 1 — rearm race reproduced (dead ticker under busy>0)", i, live)
		}
	}
	g.WaitWorkers()
	waitBusyEq(t, g, 0)
	if live := clk.liveTickers(); live != 0 {
		t.Fatalf("after full drain: liveTickers=%d, want 0", live)
	}

	// Zero repaint at quiescence: the ticker is stopped at busy==0, so
	// driving the (dead) clock fires no content-only repaint — no idle
	// 10Hz loop once work finishes.
	contentOnly := atomic.Int64{}
	contentOnly.Store(int64(rec.ContentOnlyCalls))
	for range 3 {
		clk.tickAll()
	}
	deadline := time.After(150 * time.Millisecond)
	for {
		if int64(rec.ContentOnlyCalls) > contentOnly.Load() {
			t.Fatalf("content-only repaint fired at quiescence: ContentOnlyCalls grew past %d", contentOnly.Load())
		}
		select {
		case <-deadline:
			contentOnly.Store(int64(rec.ContentOnlyCalls))
		default:
			time.Sleep(time.Millisecond)
			continue
		}
		break
	}
	if got := int64(rec.ContentOnlyCalls); got != contentOnly.Load() {
		t.Fatalf("ContentOnlyCalls=%d after settle, want unchanged %d", got, contentOnly.Load())
	}
}

// TestSpinnerRearm_ExitAndEntryBothUnderSpinnerMu is the structural pin
// for AC "busy transition + arm/stop atomic on BOTH entry and exit
// paths": a burst of overlapping short workers constantly crosses the
// 0->1 and ->0 edges from many goroutines at once. Any decision made
// outside the transition critical section (the pre-fix shape) shows up
// here under -race -count=5 as either a ticker leak (live>0 at
// quiescence) or a dead ticker under busy>0 mid-burst.
func TestSpinnerRearm_ExitAndEntryBothUnderSpinnerMu(t *testing.T) {
	clk := newFakeClock()
	g, _ := buildTestGuiWithClock(t, clk)
	defer func() { _ = g.Close() }()

	const bursts = 8
	const perBurst = 12
	for b := range bursts {
		// One long holder keeps busy>0 across the whole burst.
		releaseHolder := make(chan struct{})
		startedHolder := make(chan struct{})
		g.OnWorker(func(_ gocui.Task) error {
			close(startedHolder)
			<-releaseHolder
			return nil
		})
		<-startedHolder

		// Short workers churn the 1->2->1 interior edges while the holder
		// pins busy>0 — the ticker must stay exactly-one throughout.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range perBurst {
				inner := make(chan struct{})
				g.OnWorker(func(_ gocui.Task) error {
					close(inner)
					return nil
				})
				<-inner
			}
		}()
		<-done
		if live := clk.liveTickers(); live != 1 {
			t.Fatalf("burst %d: liveTickers=%d while holder in flight, want 1", b, live)
		}
		close(releaseHolder)
	}
	g.WaitWorkers()
	waitBusyEq(t, g, 0)
	if live := clk.liveTickers(); live != 0 {
		t.Fatalf("after bursts: liveTickers=%d, want 0", live)
	}
}
