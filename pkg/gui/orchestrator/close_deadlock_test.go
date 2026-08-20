package orchestrator_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// eofRowStream reports EOF on the first Next, so a ResultBufferManager
// worker drains an empty initial fill and then parks in its post-EOF
// chan loop (result_buffer_manager.go:416) waiting for a ReadRows
// request or Stop. This is exactly the parked-worker state captured in
// the production shutdown hang (goroutine 212246 in the SIGQUIT dump).
type eofRowStream struct{ qid models.QueryID }

func (eofRowStream) Columns() []models.ColumnMeta                   { return nil }
func (eofRowStream) Next(context.Context) (models.Row, bool, error) { return models.Row{}, false, nil }
func (eofRowStream) Close() error                                   { return nil }
func (s eofRowStream) QueryID() models.QueryID                      { return s.qid }
func (s eofRowStream) RowsAffected() int64                          { return 0 }

// fakeStreamSession embeds drivers.Session so only the two methods the
// stream-start path actually invokes need bodies; any other call panics
// (and must not happen, by construction).
type fakeStreamSession struct{ drivers.Session }

func (fakeStreamSession) ID() models.SessionID { return models.SessionID(1) }
func (fakeStreamSession) Stream(context.Context, models.Query) (drivers.RowStream, error) {
	return eofRowStream{qid: models.QueryID{SessionID: 1, Nonce: 1}}, nil
}

// fakeConn embeds drivers.Connection; only Cancel is reachable — Gui.Close
// preempts in-flight tabs (PreemptInFlight) which cancels each RunHandle
// before Stop. The remaining methods stay nil stubs.
type fakeConn struct{ drivers.Connection }

func (fakeConn) Cancel(context.Context, models.QueryID) error { return nil }

// TestCloseDoesNotDeadlockWithParkedResultTabWorker reproduces the
// shutdown hang: a result tab whose stream reached EOF leaves its
// ResultBufferManager worker parked in the chan loop, holding a
// workersWG count. Gui.Close() must stop in-flight tab tasks BEFORE
// g.workersWG.Wait() or it blocks forever (the worker only exits when
// its per-task stopCh fires, which nothing does ahead of the Wait).
func TestCloseDoesNotDeadlockWithParkedResultTabWorker(t *testing.T) {
	g, _ := buildTestGui(t)

	sess := session.New(fakeConn{}, fakeStreamSession{}, session.Options{})
	rh, err := sess.Stream(context.Background(), models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if err := g.ResultTabsHelper().OpenResultTab("q", rh); err != nil {
		t.Fatalf("OpenResultTab: %v", err)
	}

	// Wait for the worker to spin up and park (busy counter reaches 1).
	deadline := time.Now().Add(2 * time.Second)
	for g.BusyCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker never started (BusyCount stayed 0)")
		}
		time.Sleep(time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- g.Close() }()

	select {
	case <-done:
		// Close returned — no deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("Gui.Close() deadlocked: workersWG.Wait() never returned with a parked result-tab worker")
	}
}

// TestCloseReturnsWithinBoundWithStuckWorker is the C6 bounded-shutdown
// contract: an OnWorker goroutine that never returns (the launcher parked
// inside a Stream call that can never resolve) cannot hang Close. The
// bounded worker wait fires and Close still returns — fail-loud (logged)
// — within the test's 5s ceiling.
func TestCloseReturnsWithinBoundWithStuckWorker(t *testing.T) {
	g, _ := buildTestGui(t)

	started := make(chan struct{})
	release := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(started)
		<-release // never returns until the test releases it
		return nil
	})
	<-started

	done := make(chan error, 1)
	go func() { done <- g.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung >5s on a stuck worker — the bounded wait did not fire")
	}

	// Release the worker so it finishes and nothing leaks.
	close(release)
	g.WaitWorkers()
}

// TestOnUIThreadNoOpAfterClose verifies the AD7 closed-gate: after Close,
// OnUIThread and OnUIThreadContentOnly are strict no-ops — no panic, no
// block, and crucially no send into the dead UI (gocui's userEvents
// buffer is never drained post-MainLoop, so a late send could block).
func TestOnUIThreadNoOpAfterClose(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	preFull := rec.UpdateCalls
	preContent := rec.ContentOnlyCalls

	// Must not run, must not panic, must not touch the driver.
	g.OnUIThread(func() error {
		t.Error("OnUIThread closure ran after Close")
		return nil
	})
	g.OnUIThreadContentOnly(func() error {
		t.Error("OnUIThreadContentOnly closure ran after Close")
		return nil
	})

	if rec.UpdateCalls != preFull {
		t.Fatalf("OnUIThread after Close reached the driver (%d→%d), want no-op", preFull, rec.UpdateCalls)
	}
	if rec.ContentOnlyCalls != preContent {
		t.Fatalf("OnUIThreadContentOnly after Close reached the driver (%d→%d), want no-op", preContent, rec.ContentOnlyCalls)
	}
}

// closeCountingDriver wraps the recorder driver to count Close calls so
// the double-close test can prove idempotence.
type closeCountingDriver struct {
	*testfake.RecorderGuiDriver
	closeCalls atomic.Int32
}

func (d *closeCountingDriver) Close() error {
	d.closeCalls.Add(1)
	return nil
}

// TestCloseDoubleIsNoOp verifies the atomic closed-gate: a concurrent
// second Close is a no-op and the driver is torn down exactly once.
func TestCloseDoubleIsNoOp(t *testing.T) {
	drv := &closeCountingDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
	g := buildTestGuiWithDriverAndClock(t, drv, newFakeClock())

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			errs[i] = g.Close()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Close[%d] err = %v, want nil", i, err)
		}
	}
	if got := drv.closeCalls.Load(); got != 1 {
		t.Fatalf("driver Close calls = %d, want 1 (the second Close must be a no-op)", got)
	}
}
