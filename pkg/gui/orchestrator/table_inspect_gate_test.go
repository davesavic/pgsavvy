package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
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

func TestReview_inspector_current_failure_settles(t *testing.T) {
	for _, err := range []error{errors.New("synthetic metadata failure"), context.Canceled} {
		t.Run(err.Error(), func(t *testing.T) {
			g, d, _ := reviewInspectorGui(t, err, nil)
			reviewInspectorSelect(g, &models.Table{Schema: "public", Name: "a"})
			fireTableInspectOpen(t, g)
			g.WaitForWorkersForTest()
			var reveals int
			for _, fn := range reviewInspectorCallbacks(d) {
				loading := g.Registry().TableInspect.IsLoading()
				reviewInspectorRun(t, []func() error{fn})
				if loading && !g.Registry().TableInspect.IsLoading() {
					reveals++
				}
			}
			if reveals != 1 || g.Registry().TableInspect.IsLoading() {
				t.Fatalf("failed opening reveal transitions = %d, loading = %t", reveals, g.Registry().TableInspect.IsLoading())
			}
			if !strings.Contains(g.Registry().ForeignKeys.BodyText(), "could not load outbound foreign keys") || !strings.Contains(g.Registry().ForeignKeys.BodyText(), "could not load inbound foreign keys") || !strings.Contains(g.Registry().Constraints.BodyText(), "could not load constraints") {
				t.Fatal("current opening lost failure visibility")
			}
		})
	}
}

func TestReview_inspector_dismissal_cancels_workers(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "pop"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			g, d, sess := reviewInspectorGui(t, nil, nil)
			sess.stage.Store(1)
			tbl := &models.Table{Schema: "public", Name: "b"}
			reviewInspectorSelect(g, tbl)
			fireTableInspectOpen(t, g)
			select {
			case <-sess.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("load never entered")
			}
			finished := make(chan struct{})
			if shutdown {
				go func() { _ = g.Close(); close(finished) }()
			} else {
				if err := g.ContextTree().Pop(); err != nil {
					t.Fatal(err)
				}
				go func() { g.WaitForWorkersForTest(); close(finished) }()
			}
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				close(sess.release)
				<-finished
				t.Fatal("dismissed opening workers did not settle on cancellation")
			}
			d.drainPending()
			if len(g.Registry().Columns.Items()) != 0 || tbl.EstimatedRows.Load() != 0 {
				t.Fatal("dismissed opening published data")
			}
		})
	}
}
