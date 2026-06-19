package orchestrator_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestTableInspectGate_DecrementsToZeroAndRevealsOnce models the ack-gate
// registerTableInspectOpen uses: N workers each `defer done()`, and done()
// fires the SetLoading(false) reveal EXACTLY once — when the counter reaches
// zero — regardless of how many workers error. Tested as a standalone
// mechanism (per the task: a direct unit test of the gate suffices when
// driving registerTableInspectOpen's internal counter is impractical).
func TestTableInspectGate_DecrementsToZeroAndRevealsOnce(t *testing.T) {
	const workers = 5

	var reveals atomic.Int32
	var ack atomic.Int32
	ack.Store(workers)
	done := func() {
		if ack.Add(-1) == 0 {
			reveals.Add(1)
		}
	}

	// One worker "errors" (still returns, still defers done()) — the gate must
	// not stall: an erroring worker decrements like any other.
	work := []func() error{
		func() error { return nil },
		func() error { return errors.New("boom") },
		func() error { return nil },
		func() error { return nil },
		func() error { return nil },
	}

	var wg sync.WaitGroup
	for _, w := range work {
		wg.Add(1)
		go func(fn func() error) {
			defer wg.Done()
			defer done()
			_ = fn()
		}(w)
	}
	wg.Wait()

	if got := ack.Load(); got != 0 {
		t.Fatalf("ack counter = %d, want 0 (popup would stay stuck in Loading…)", got)
	}
	if got := reveals.Load(); got != 1 {
		t.Fatalf("reveal fired %d times, want exactly 1", got)
	}
}

// TestTableInspectOpen_ClearsLoadingWhenAWorkerErrors drives the real open
// command with a conn whose constraints fetch errors. The gate must still
// reach zero and clear the Loading flag — a single failed leaf must never
// leave the popup stuck on "Loading…".
func TestTableInspectOpen_ClearsLoadingWhenAWorkerErrors(t *testing.T) {
	g, _ := connectAndSelectTableOpt(t, "public", "users", func(c *wireFakeConn) {
		c.constraintsErr = errors.New("boom")
	})

	fireTableInspectOpen(t, g)
	g.WaitForWorkersForTest()

	if g.Registry().TableInspect.IsLoading() {
		t.Fatal("TableInspect.IsLoading() = true after a worker errored; gate must still clear Loading")
	}
}
