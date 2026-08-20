package orchestrator_test

import (
	"testing"
	"time"
)

// This file covers the HoldBusy / ReleaseBusy launcher-bridge
// (pgsavvy-446q): the two methods let the QueryRunner launcher arm the
// busy spinner for the whole duration of a slow query (op stream + RBM
// drain) without spawning an OnWorker goroutine. They mirror OnWorker's
// entry/exit critical sections, so the rearm invariants the spinner
// stress tests pin for workers must hold here too.

// TestHoldBusy_ArmsSpinnerAndReleaseStops proves the pairing contract:
// HoldBusy raises BusyCount 0->1 and arms the spinner ticker (exactly
// one live ticker, frame advancing over simulated time), and the paired
// ReleaseBusy returns the counter to 0 and stops the spinner — all
// synchronously on the calling goroutine (no OnWorker goroutine is
// involved, so no WaitWorkers is needed for the counter/arm assertions).
func TestHoldBusy_ArmsSpinnerAndReleaseStops(t *testing.T) {
	clk := newFakeClock()
	g, _ := buildTestGuiWithClock(t, clk)
	defer func() { _ = g.Close() }()

	if got := g.BusyCount(); got != 0 {
		t.Fatalf("pre-condition: BusyCount=%d, want 0", got)
	}
	if live := clk.liveTickers(); live != 0 {
		t.Fatalf("pre-condition: liveTickers=%d, want 0", live)
	}

	if held := g.HoldBusy(); !held {
		t.Fatal("HoldBusy returned false while the Gui is open")
	}
	if got := g.BusyCount(); got != 1 {
		t.Fatalf("after HoldBusy: BusyCount=%d, want 1", got)
	}
	if live := clk.liveTickers(); live != 1 {
		t.Fatalf("after HoldBusy: liveTickers=%d, want 1 (spinner armed)", live)
	}

	// The spinner frame advances over simulated time while the hold is up.
	clk.Advance(350 * time.Millisecond)
	if frame := g.SpinnerFrame(); frame < 3 {
		t.Fatalf("SpinnerFrame=%d after 350ms while held, want >= 3", frame)
	}

	g.ReleaseBusy()
	if got := g.BusyCount(); got != 0 {
		t.Fatalf("after ReleaseBusy: BusyCount=%d, want 0", got)
	}
	if live := clk.liveTickers(); live != 0 {
		t.Fatalf("after ReleaseBusy: liveTickers=%d, want 0 (spinner stopped)", live)
	}
	// Wait for the ticker's drain goroutine (spawned by the arm) to exit
	// so a goleak sweep sees a quiescent pool.
	g.WaitWorkers()
}

// TestHoldBusy_AfterCloseReturnsFalse verifies the closed-gate half of
// the pairing contract: after Close, HoldBusy returns false WITHOUT
// touching the busy counter (the caller skips the paired ReleaseBusy, so
// a closed Gui must never be left holding a phantom +1).
func TestHoldBusy_AfterCloseReturnsFalse(t *testing.T) {
	g, _ := buildTestGui(t)
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := g.BusyCount(); got != 0 {
		t.Fatalf("pre-condition: BusyCount=%d, want 0", got)
	}
	if held := g.HoldBusy(); held {
		t.Fatal("HoldBusy returned true after Close")
	}
	if got := g.BusyCount(); got != 0 {
		t.Fatalf("HoldBusy after Close mutated BusyCount to %d, want 0", got)
	}
}
