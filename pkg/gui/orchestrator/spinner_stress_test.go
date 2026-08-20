package orchestrator_test

import (
	"sync"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"go.uber.org/goleak"
)

// C7 stress half: spinner arm/stop atomicity across rapid busy transitions
// under the fake clock (the C3 fix). The existing rearm tests prove the
// interleave window structurally; this suite hammers the SAME 0->1->0->1
// window at volume and adds the two invariants volume alone can expose: no
// orphaned drain goroutine (goleak) and a busy counter that returns to a
// consistent quiescent zero after every transition sequence.

// TestStressSpinnerTransitions runs 50 rapid sequential 0->1->0 cycles,
// asserting at each edge that the exactly-one-ticker invariant holds (1
// live ticker while busy>0, 0 at quiescence), then a burst of 20
// overlapping workers churning the 1->2->1 interior edges while a holder
// pins busy>0, and finally a full drain. goleak verifies no dead ticker
// goroutine or orphaned drain survives.
func TestStressSpinnerTransitions(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clk := newFakeClock()
	g, _ := buildTestGuiWithClock(t, clk)
	defer func() { _ = g.Close() }()

	// Phase 1: rapid sequential 0->1->0 transitions. Each cycle arms one
	// worker, observes the arm invariant, releases it, and observes the
	// stop invariant before the next cycle begins — the exact window the C3
	// spinnerMu critical sections made atomic.
	const cycles = 50
	for i := 0; i < cycles; i++ {
		started := make(chan struct{})
		release := make(chan struct{})
		g.OnWorker(func(_ gocui.Task) error {
			close(started)
			<-release
			return nil
		})
		<-started

		if got := g.BusyCount(); got != 1 {
			t.Fatalf("cycle %d: BusyCount=%d after arm, want 1", i, got)
		}
		if live := clk.liveTickers(); live != 1 {
			t.Fatalf("cycle %d: liveTickers=%d while busy, want 1 (arm did not hold)", i, live)
		}

		close(release)
		waitBusyEq(t, g, 0)
		// The worker's exit critical section (busyDelta(-1) → stopSpinner)
		// runs in the goroutine's deferred funcs; busy hits 0 BEFORE
		// stopSpinner executes, so wait for the goroutine to fully exit
		// before asserting the ticker is gone (deterministic quiescence).
		g.WaitWorkers()
		if live := clk.liveTickers(); live != 0 {
			t.Fatalf("cycle %d: liveTickers=%d at quiescence, want 0 (dead ticker)", i, live)
		}
	}

	// Phase 2: overlapping burst. One long holder pins busy>0 while 20
	// short workers churn the 1->2->1 interior edges — the ticker must stay
	// exactly-one throughout, then fully drain.
	startedHolder := make(chan struct{})
	releaseHolder := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(startedHolder)
		<-releaseHolder
		return nil
	})
	<-startedHolder

	const burst = 20
	var wg sync.WaitGroup
	wg.Add(burst)
	for range burst {
		inner := make(chan struct{})
		g.OnWorker(func(_ gocui.Task) error {
			defer wg.Done()
			close(inner)
			return nil
		})
		<-inner
	}
	if live := clk.liveTickers(); live != 1 {
		t.Fatalf("during burst: liveTickers=%d, want 1", live)
	}
	wg.Wait()
	close(releaseHolder)
	g.WaitWorkers()

	// Full drain: busy settles to a consistent 0 and the ticker is gone.
	waitBusyEq(t, g, 0)
	if live := clk.liveTickers(); live != 0 {
		t.Fatalf("after %d rapid transitions + burst: liveTickers=%d, want 0 (dead ticker)", cycles, live)
	}
}
