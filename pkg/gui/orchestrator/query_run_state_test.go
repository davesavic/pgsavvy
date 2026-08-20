package orchestrator_test

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
)

// This file covers the generation-tagged query-run slot
// (pgsavvy-vky3.2): SetQueryRunStarted / ClearQueryRun /
// QueryRunStarted on Gui, including the injected-clock stamping and
// the stale-generation clear discipline. Pure state — no repaint
// behavior is asserted here (rendering lands in a later task).

// newQueryRunGui builds a fully wired Gui on the recorder driver with
// a fake clock, so startedAt stamps come from the injected clock
// deterministically.
func newQueryRunGui(t *testing.T) (*orchestrator.Gui, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	g := buildTestGuiWithDriverAndClock(t, testfake.NewRecorderGuiDriver(), clk)
	return g, clk
}

// TestQueryRunSetThenStartedReportsRunIDAndTime covers the basic
// contract: after SetQueryRunStarted("run-a"), QueryRunStarted returns
// the recorded runID, a startedAt stamped from the injected clock, and
// ok=true. Before any Set, ok=false.
func TestQueryRunSetThenStartedReportsRunIDAndTime(t *testing.T) {
	g, clk := newQueryRunGui(t)

	if _, _, ok := g.QueryRunStarted(); ok {
		t.Fatal("fresh Gui reports a run in flight, want ok=false")
	}

	clk.Advance(3 * time.Second)
	want := clk.Now()
	g.SetQueryRunStarted("run-a")

	startedAt, runID, ok := g.QueryRunStarted()
	if !ok {
		t.Fatal("ok=false after SetQueryRunStarted, want true")
	}
	if runID != "run-a" {
		t.Fatalf("runID = %q, want %q", runID, "run-a")
	}
	if !startedAt.Equal(want) {
		t.Fatalf("startedAt = %v, want the injected clock's now %v", startedAt, want)
	}
}

// TestQueryRunStartedAtStampsSetTimeNotReadTime proves startedAt is
// captured when Set runs, not when QueryRunStarted reads it: advancing
// the clock after the Set must not move a reported startedAt.
func TestQueryRunStartedAtStampsSetTimeNotReadTime(t *testing.T) {
	g, clk := newQueryRunGui(t)

	clk.Advance(3 * time.Second)
	g.SetQueryRunStarted("run-a")
	want := clk.Now()

	clk.Advance(7 * time.Second)
	startedAt, _, ok := g.QueryRunStarted()
	if !ok {
		t.Fatal("ok=false after SetQueryRunStarted, want true")
	}
	if !startedAt.Equal(want) {
		t.Fatalf("startedAt = %v, want the Set-time clock now %v (clock has since advanced to %v)", startedAt, want, clk.Now())
	}
}

// TestQueryRunClearMatchingRunIDClearsSlot covers the finish path:
// ClearQueryRun with the current generation's runID empties the slot.
func TestQueryRunClearMatchingRunIDClearsSlot(t *testing.T) {
	g, _ := newQueryRunGui(t)

	g.SetQueryRunStarted("run-a")
	g.ClearQueryRun("run-a")
	if _, _, ok := g.QueryRunStarted(); ok {
		t.Fatal("ok=true after ClearQueryRun with matching runID, want false")
	}
}

// TestQueryRunClearStaleGenerationIsNoop covers the generation-tag
// contract: a stale run finishing after a newer run started must not
// wipe the newer run's state. Set(A), Set(B), Clear(A) → still B;
// Clear(B) → empty.
func TestQueryRunClearStaleGenerationIsNoop(t *testing.T) {
	g, _ := newQueryRunGui(t)

	g.SetQueryRunStarted("run-a")
	g.SetQueryRunStarted("run-b")
	g.ClearQueryRun("run-a")

	_, runID, ok := g.QueryRunStarted()
	if !ok || runID != "run-b" {
		t.Fatalf("after stale Clear(run-a): (runID=%q, ok=%v), want (run-b, true)", runID, ok)
	}

	g.ClearQueryRun("run-b")
	if _, _, ok := g.QueryRunStarted(); ok {
		t.Fatal("ok=true after ClearQueryRun(run-b), want false")
	}
}

// TestQueryRunLastWinsOverwrite covers the supersession contract: a
// later SetQueryRunStarted overwrites the earlier run's slot.
func TestQueryRunLastWinsOverwrite(t *testing.T) {
	g, clk := newQueryRunGui(t)

	clk.Advance(time.Second)
	g.SetQueryRunStarted("run-a")
	clk.Advance(time.Second)
	want := clk.Now()
	g.SetQueryRunStarted("run-b")

	startedAt, runID, ok := g.QueryRunStarted()
	if !ok || runID != "run-b" {
		t.Fatalf("(runID=%q, ok=%v) after overwrite, want (run-b, true)", runID, ok)
	}
	if !startedAt.Equal(want) {
		t.Fatalf("startedAt = %v, want run-b's stamp %v", startedAt, want)
	}
}

// TestQueryRunClearOnIdleIsNoop proves clearing an empty slot neither
// panics nor flips ok.
func TestQueryRunClearOnIdleIsNoop(t *testing.T) {
	g, _ := newQueryRunGui(t)

	g.ClearQueryRun("run-ghost")
	if _, _, ok := g.QueryRunStarted(); ok {
		t.Fatal("ok=true after ClearQueryRun on idle slot, want false")
	}
}

// TestQueryRunConcurrentHammer exercises the slot under -race: writers
// hammer Set with fresh runIDs and Clear with random ones while
// readers spin on QueryRunStarted. Must terminate without race or
// panic; the slot's only invariant under contention is that a read
// observes SOME consistent (runID, startedAt, ok) snapshot.
func TestQueryRunConcurrentHammer(t *testing.T) {
	g, clk := newQueryRunGui(t)

	const writers = 8
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(writers + 2)

	rng := rand.New(rand.NewSource(1))
	var rngMu sync.Mutex
	randomRunID := func() string {
		rngMu.Lock()
		defer rngMu.Unlock()
		return fmt.Sprintf("run-%d", rng.Intn(4))
	}

	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				runID := fmt.Sprintf("run-%d-%d", w, i)
				if i%3 == 0 {
					clk.Advance(time.Millisecond)
				}
				g.SetQueryRunStarted(runID)
				g.ClearQueryRun(randomRunID())
			}
		}(w)
	}
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			g.ClearQueryRun(fmt.Sprintf("run-reader-%d", i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			startedAt, runID, ok := g.QueryRunStarted()
			if ok && (runID == "" || startedAt.IsZero()) {
				t.Errorf("active slot with empty fields: (runID=%q, startedAt=%v)", runID, startedAt)
				return
			}
		}
	}()

	wg.Wait()

	// Quiesced state must still be coherent: drain whatever the last
	// writer left, then observe ok=false.
	_, runID, ok := g.QueryRunStarted()
	for ok {
		g.ClearQueryRun(runID)
		_, runID, ok = g.QueryRunStarted()
	}
}
