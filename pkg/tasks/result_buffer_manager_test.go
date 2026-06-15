package tasks_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/tasks"
)

// stubRowStream is a deterministic drivers.RowStream used by every
// test in this file. It yields `total` rows whose single column
// value is the 0-based row index (so test assertions can verify
// order without juggling fixtures). Optional `errAt` triggers a
// Next-returns-error on the configured row index.
type stubRowStream struct {
	total      int
	errAt      int // -1 = never; otherwise return error when next would yield this index
	idx        int
	closeCount atomic.Int32
	// blockAt: when >= 0, Next blocks on `release` once idx == blockAt
	// (used to suspend a stream mid-drain so a preempt has time to
	// fire while the worker is parked inside Next).
	blockAt int
	release chan struct{}
}

func newStubRowStream(total int) *stubRowStream {
	return &stubRowStream{total: total, errAt: -1, blockAt: -1}
}

func (s *stubRowStream) Columns() []models.ColumnMeta { return nil }

func (s *stubRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	if s.blockAt >= 0 && s.idx == s.blockAt {
		// Park here until released or ctx cancelled — lets a test
		// observe the "worker is currently inside the driver"
		// state when triggering preemption.
		select {
		case <-s.release:
		case <-ctx.Done():
			return models.Row{}, false, ctx.Err()
		}
	}
	if s.errAt >= 0 && s.idx == s.errAt {
		return models.Row{}, false, errors.New("stub: synthetic stream error")
	}
	if s.idx >= s.total {
		return models.Row{}, false, nil
	}
	row := models.Row{Values: []any{s.idx}}
	s.idx++
	return row, true, nil
}

func (s *stubRowStream) Close() error {
	s.closeCount.Add(1)
	return nil
}

func (s *stubRowStream) QueryID() models.QueryID { return models.QueryID{} }
func (s *stubRowStream) RowsAffected() int64     { return 0 }

var _ drivers.RowStream = (*stubRowStream)(nil)

// testHarness wires a ResultBufferManager to in-process stubs for
// onWorker and onUIThread. onWorker spawns a real goroutine tracked
// on a WaitGroup so tests can join cleanly. onUIThread invokes the
// closure on a *dedicated single goroutine* — this is required by
// the "always on UI thread" assertion (the appendRows invocation
// must be observably on a different goroutine than the worker, and
// the same goroutine across all dispatches so FIFO is preserved).
type testHarness struct {
	mgr *tasks.ResultBufferManager

	workersWG sync.WaitGroup

	// uiCh feeds the dedicated UI-thread goroutine.
	uiCh   chan func() error
	uiDone chan struct{}

	// uiGoroutineID is the runtime-id of the UI goroutine, captured
	// on first dispatch. appendRows callers compare against it.
	uiGoroutineID atomic.Uint64

	// appendCallsFromWrongGoroutine is incremented if appendRows is
	// ever invoked from a goroutine other than the UI goroutine.
	appendCallsFromWrongGoroutine atomic.Int32
}

func newTestHarness() *testHarness {
	h := &testHarness{
		uiCh:   make(chan func() error, 1024),
		uiDone: make(chan struct{}),
	}

	// Dedicated UI-thread goroutine. Mimics gocui's MainLoop —
	// closures run serially in arrival order.
	go func() {
		defer close(h.uiDone)
		// Capture this goroutine's id once. Subsequent dispatches
		// MUST run on this same goroutine, so this single capture
		// is the reference value for the wrong-goroutine check.
		h.uiGoroutineID.Store(currentGoroutineID())
		for fn := range h.uiCh {
			_ = fn()
		}
	}()

	onWorker := func(fn func(gocui.Task) error) {
		h.workersWG.Go(func() {
			_ = fn(nil)
		})
	}

	onUIThread := func(fn func() error) {
		h.uiCh <- fn
	}

	h.mgr = tasks.New(onWorker, onUIThread)
	return h
}

// stopUI shuts down the UI goroutine. Call after Stop()-ing the
// manager and joining workers so no further dispatches happen.
func (h *testHarness) stopUI() {
	close(h.uiCh)
	<-h.uiDone
}

// drainUI waits until every appendRows dispatched so far has run.
// Implemented by enqueuing a barrier closure and waiting for it to
// fire — relies on FIFO inside uiCh.
func (h *testHarness) drainUI() {
	done := make(chan struct{})
	h.uiCh <- func() error {
		close(done)
		return nil
	}
	<-done
}

// makeAppendRows returns an appendRows callback that records every
// batch into the supplied buffer (guarded by mu) and asserts each
// invocation is on the UI goroutine.
func (h *testHarness) makeAppendRows(mu *sync.Mutex, dst *[]models.Row) func([]models.Row) {
	return func(batch []models.Row) {
		if id := currentGoroutineID(); id != h.uiGoroutineID.Load() {
			h.appendCallsFromWrongGoroutine.Add(1)
		}
		mu.Lock()
		*dst = append(*dst, batch...)
		mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestNewQueryTaskInitialFillPrePaintsRows — AC scenario "Initial
// fill pre-paints rows". 500-row stub, initialRows=200; within 100ms
// appendRows holds the first 200; remaining 300 are NOT delivered
// until ReadRows fires.
func TestNewQueryTaskInitialFillPrePaintsRows(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(500)
	doneCh := make(chan struct{})

	var (
		mu      sync.Mutex
		got     []models.Row
		appendF = h.makeAppendRows(&mu, &got)
	)

	err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		200,
		func(error) { close(doneCh) },
	)
	if err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	// Wait up to 100ms for the initial fill to land on the UI
	// goroutine.
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("initial fill did not deliver 200 rows in 100ms; got %d", n)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Allow time for any (incorrect) further deliveries to happen.
	time.Sleep(30 * time.Millisecond)
	h.drainUI()

	mu.Lock()
	if len(got) != 200 {
		t.Fatalf("after initial fill: len(got)=%d, want 200 (remaining must wait for ReadRows)", len(got))
	}
	// Verify in-order delivery.
	for i, r := range got {
		if v := r.Values[0].(int); v != i {
			t.Fatalf("row %d: value=%d, want %d (out of order)", i, v, i)
		}
	}
	mu.Unlock()

	// Cleanup.
	h.mgr.Stop()
	h.workersWG.Wait()
	<-doneCh
	h.stopUI()

	if h.appendCallsFromWrongGoroutine.Load() != 0 {
		t.Fatalf("appendRows invoked off-UI %d times", h.appendCallsFromWrongGoroutine.Load())
	}
}

// TestNewQueryTaskReadRowsDeliversInOrder — after the initial fill
// of 200, ReadRows(50) yields the next 50 in-order.
func TestNewQueryTaskReadRowsDeliversInOrder(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(500)
	doneCh := make(chan struct{})

	var (
		mu      sync.Mutex
		got     []models.Row
		appendF = h.makeAppendRows(&mu, &got)
	)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		200,
		func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	// Wait for initial fill.
	waitForCount(t, &mu, &got, 200, 200*time.Millisecond)

	h.mgr.ReadRows(50)
	waitForCount(t, &mu, &got, 250, 200*time.Millisecond)

	mu.Lock()
	for i, r := range got {
		if v := r.Values[0].(int); v != i {
			t.Fatalf("row %d: value=%d, want %d", i, v, i)
		}
	}
	mu.Unlock()

	h.mgr.Stop()
	h.workersWG.Wait()
	<-doneCh
	h.stopUI()
}

// TestNewQueryTaskReadToEndCallsThenOnce — ReadToEnd drains the
// stream and fires `then` exactly once.
func TestNewQueryTaskReadToEndCallsThenOnce(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(300)
	doneCh := make(chan struct{})

	var (
		mu      sync.Mutex
		got     []models.Row
		appendF = h.makeAppendRows(&mu, &got)
	)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		50,
		func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}
	waitForCount(t, &mu, &got, 50, 200*time.Millisecond)

	var thenCalls atomic.Int32
	thenFired := make(chan struct{})
	h.mgr.ReadToEnd(func() {
		thenCalls.Add(1)
		close(thenFired)
	})

	select {
	case <-thenFired:
	case <-time.After(time.Second):
		t.Fatal("ReadToEnd `then` did not fire within 1s")
	}

	waitForCount(t, &mu, &got, 300, 200*time.Millisecond)

	if n := thenCalls.Load(); n != 1 {
		t.Fatalf("then fired %d times, want 1", n)
	}

	h.mgr.Stop()
	h.workersWG.Wait()
	<-doneCh
	h.stopUI()
}

// TestNewQueryTaskStopClosesStreamAndFiresOnDoneOnce — Stop on a
// running task closes the RowStream and fires onDone exactly once.
func TestNewQueryTaskStopClosesStreamAndFiresOnDoneOnce(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(10000) // big enough we won't naturally exit
	var onDoneCalls atomic.Int32
	doneCh := make(chan struct{})

	appendF := func(_ []models.Row) {}

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		100,
		func(error) {
			onDoneCalls.Add(1)
			close(doneCh)
		},
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	// Let the worker start the chan loop.
	time.Sleep(20 * time.Millisecond)

	h.mgr.Stop()
	h.workersWG.Wait()

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("onDone did not fire within 1s of Stop")
	}

	if n := onDoneCalls.Load(); n != 1 {
		t.Fatalf("onDone fired %d times, want 1", n)
	}
	if c := stream.closeCount.Load(); c != 1 {
		t.Fatalf("RowStream.Close called %d times, want 1", c)
	}

	h.stopUI()
}

// TestNewQueryTaskStopBeforeStreamFnStillClosesStream is the
// deadlock regression guard at the RBM layer. In dbsavvy the pgx stream is
// opened — and the per-session streamMu locked — by SQLSession.Stream
// BEFORE NewQueryTask; the worker's streamFn merely hands back that
// already-open stream. If a Stop arrives before the worker reaches
// streamFn and the worker bails WITHOUT closing the stream, the stream's
// Close()/finish() never runs and streamMu leaks — freezing the next run
// (run_all statement-2, leader+R/leader+E with 2+ statements).
//
// This test forces "Stop fires before the worker runs streamFn" by gating
// the worker goroutine until after Stop() has closed stopCh, then asserts
// the pre-opened stream is still Closed exactly once and onDone fires once.
func TestNewQueryTaskStopBeforeStreamFnStillClosesStream(t *testing.T) {
	gate := make(chan struct{})
	var workersWG sync.WaitGroup
	onWorker := func(fn func(gocui.Task) error) {
		workersWG.Go(func() {
			<-gate // do not start until the test has Stopped the task
			_ = fn(nil)
		})
	}
	onUIThread := func(fn func() error) { _ = fn() }
	mgr := tasks.New(onWorker, onUIThread)

	stream := newStubRowStream(500)
	var streamFnCalls, onDoneCalls atomic.Int32
	streamFn := func(_ context.Context) (drivers.RowStream, error) {
		streamFnCalls.Add(1)
		return stream, nil
	}

	if err := mgr.NewQueryTask("k", streamFn, func([]models.Row) {}, 200, func(error) {
		onDoneCalls.Add(1)
	}); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	// Stop while the worker is still gated (before it can reach streamFn).
	// Stop closes stopCh then blocks on the worker's doneCh, so run it on a
	// goroutine; the 50ms gap lets Stop close stopCh before we release the
	// worker, so the worker observes an already-closed stopCh.
	stopped := make(chan struct{})
	go func() { mgr.Stop(); close(stopped) }()
	time.Sleep(50 * time.Millisecond)
	close(gate)

	<-stopped
	workersWG.Wait()

	if got := streamFnCalls.Load(); got != 1 {
		t.Errorf("streamFn calls = %d, want 1 (a Stopped task must still acquire its stream so it can close it)", got)
	}
	if got := stream.closeCount.Load(); got != 1 {
		t.Errorf("stream Close count = %d, want 1 (a Stopped task must close its pre-opened stream to release streamMu)", got)
	}
	if got := onDoneCalls.Load(); got != 1 {
		t.Errorf("onDone calls = %d, want 1", got)
	}
}

// TestNewQueryTaskPreemption — task "A" is running; NewQueryTask("B")
// fires. A's onDone must fire before B's streamFn is called.
func TestNewQueryTaskPreemption(t *testing.T) {
	h := newTestHarness()

	streamA := newStubRowStream(10000)
	streamB := newStubRowStream(10)

	doneA := make(chan struct{})
	streamBOpened := make(chan struct{})

	if err := h.mgr.NewQueryTask(
		"A",
		func(_ context.Context) (drivers.RowStream, error) { return streamA, nil },
		func(_ []models.Row) {},
		50,
		func(error) { close(doneA) },
	); err != nil {
		t.Fatalf("NewQueryTask(A): %v", err)
	}

	// Give A's worker time to actually start and enter the chan
	// loop. Without this the preempt path may not exercise the
	// in-flight stop branch.
	time.Sleep(30 * time.Millisecond)

	// Concurrently fire B. NewQueryTask is allowed to block until A
	// has been preempted (it calls priorStop synchronously), so
	// kick it off on a goroutine so we can observe the ordering
	// from the test goroutine.
	bStarted := make(chan struct{})
	go func() {
		_ = h.mgr.NewQueryTask(
			"B",
			func(_ context.Context) (drivers.RowStream, error) {
				close(streamBOpened)
				return streamB, nil
			},
			func(_ []models.Row) {},
			5,
			func(error) {},
		)
		close(bStarted)
	}()

	// A's onDone must fire before B's streamFn opens.
	select {
	case <-doneA:
		// good
	case <-streamBOpened:
		t.Fatal("B's streamFn opened before A's onDone fired (preemption ordering violated)")
	case <-time.After(time.Second):
		t.Fatal("A's onDone did not fire within 1s of preempting NewQueryTask(B)")
	}

	// Now sanity-check streamBOpened fires too.
	select {
	case <-streamBOpened:
	case <-time.After(time.Second):
		t.Fatal("B's streamFn never opened after preempt completed")
	}

	<-bStarted
	h.mgr.Stop()
	h.workersWG.Wait()
	h.stopUI()
}

// TestNewQueryTaskAppendRowsAlwaysOnUIThread — instrumented via
// makeAppendRows + the UI-goroutine-id seam in the harness.
func TestNewQueryTaskAppendRowsAlwaysOnUIThread(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(500)
	doneCh := make(chan struct{})

	var (
		mu      sync.Mutex
		got     []models.Row
		appendF = h.makeAppendRows(&mu, &got)
	)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		100,
		func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	waitForCount(t, &mu, &got, 100, 200*time.Millisecond)
	h.mgr.ReadRows(100)
	waitForCount(t, &mu, &got, 200, 200*time.Millisecond)
	h.mgr.ReadRows(150)
	waitForCount(t, &mu, &got, 350, 200*time.Millisecond)

	h.mgr.Stop()
	h.workersWG.Wait()
	<-doneCh
	h.stopUI()

	if n := h.appendCallsFromWrongGoroutine.Load(); n != 0 {
		t.Fatalf("appendRows invoked off UI thread %d times", n)
	}
}

// TestNewQueryTaskStreamFnErrorFiresOnDoneCleanly — streamFn returns
// an error: onDone still fires; no panic; manager returns to idle.
func TestNewQueryTaskStreamFnErrorFiresOnDoneCleanly(t *testing.T) {
	h := newTestHarness()
	doneCh := make(chan struct{})

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) {
			return nil, errors.New("synthetic open failure")
		},
		func(_ []models.Row) {},
		100,
		func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("onDone did not fire within 1s of streamFn error")
	}

	h.workersWG.Wait()
	if k := h.mgr.TaskKey(); k != "" {
		t.Fatalf("after streamFn error: TaskKey=%q, want \"\" (idle)", k)
	}
	h.stopUI()
}

// TestNewQueryTaskNextErrorMidStream — Next returns an error after
// some rows: rows up to the error are retained; onDone fires
// automatically with the propagated error. Prior to
// the fix the Next error was swallowed and onDone only fired on a manual
// Stop, leaving the result indistinguishable from a clean completion.
func TestNewQueryTaskNextErrorMidStream(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(100)
	stream.errAt = 30 // Next yields rows 0..29 then errors at idx==30
	doneCh := make(chan struct{})
	var doneErr atomic.Value // stores the error handed to onDone

	var (
		mu      sync.Mutex
		got     []models.Row
		appendF = h.makeAppendRows(&mu, &got)
	)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		100, // initial fill larger than errAt — drains stop at error
		func(err error) {
			if err != nil {
				doneErr.Store(err)
			}
			close(doneCh)
		},
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	// onDone must fire on its own — no Stop required — because the
	// mid-stream error now propagates through the done path.
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("onDone did not fire within 1s of mid-stream error")
	}

	// The propagated error must be non-nil so the UI can render an
	// error state instead of a misleading "complete".
	if doneErr.Load() == nil {
		t.Fatal("onDone fired with nil error; want the mid-stream stream.Next error")
	}

	// Rows up to the error are retained: exactly 30 delivered.
	waitForCount(t, &mu, &got, 30, 200*time.Millisecond)
	h.drainUI()
	mu.Lock()
	if len(got) != 30 {
		t.Fatalf("after mid-stream error: len(got)=%d, want 30", len(got))
	}
	mu.Unlock()

	h.workersWG.Wait()
	if k := h.mgr.TaskKey(); k != "" {
		t.Fatalf("after mid-stream error: TaskKey=%q, want \"\" (idle)", k)
	}
	h.stopUI()
}

// TestNewQueryTaskReadToEndNextErrorMidStream — a mid-stream Next error
// on the ReadToEnd (G / drain-to-end) path must NOT fire the ReadToEnd
// `then` callback (clean completion) and must propagate the error through
// onDone. Regression guard: req.Then() previously fired
// before the error check, so fireReadToEnd → markCompleteOnUI(nil) set
// StateComplete first, and the later onDone(err) → markCompleteOnUI(err)
// was skipped by its StateRunning/StateSorting guard — leaving the tab
// "complete" despite the failure. The fix exits on err BEFORE Then(), so
// the error flows solely through onDone → StateErrored.
func TestNewQueryTaskReadToEndNextErrorMidStream(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(100)
	stream.errAt = 30 // rows 0..29 then error at idx==30
	doneCh := make(chan struct{})
	var doneErr atomic.Value // error handed to onDone

	var (
		mu      sync.Mutex
		got     []models.Row
		appendF = h.makeAppendRows(&mu, &got)
	)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		10, // small initial fill (< errAt) so the error lands in the chan loop
		func(err error) {
			if err != nil {
				doneErr.Store(err)
			}
			close(doneCh)
		},
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	// Initial fill delivers the first 10 rows cleanly, then the worker
	// parks in the chan loop awaiting a pull request.
	waitForCount(t, &mu, &got, 10, 200*time.Millisecond)

	// ReadToEnd drains the rest; the stream errors at idx 30 mid-drain.
	var thenCalls atomic.Int32
	h.mgr.ReadToEnd(func() { thenCalls.Add(1) })

	// onDone must fire on its own with the propagated error.
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("onDone did not fire within 1s of mid-stream error on ReadToEnd path")
	}
	if doneErr.Load() == nil {
		t.Fatal("onDone fired with nil error; want the mid-stream stream.Next error")
	}

	// The clean-completion callback must NOT have fired: a mid-stream
	// error is not a clean drain. This is the core of the regression —
	// firing Then() here would mislabel the tab StateComplete.
	if n := thenCalls.Load(); n != 0 {
		t.Fatalf("ReadToEnd `then` fired %d times on mid-stream error, want 0", n)
	}

	// Rows up to the error are retained: 10 (initial) + 20 (idx 10..29).
	waitForCount(t, &mu, &got, 30, 200*time.Millisecond)
	h.drainUI()
	mu.Lock()
	if len(got) != 30 {
		t.Fatalf("after mid-stream error on ReadToEnd: len(got)=%d, want 30", len(got))
	}
	mu.Unlock()

	h.workersWG.Wait()
	if k := h.mgr.TaskKey(); k != "" {
		t.Fatalf("after mid-stream error: TaskKey=%q, want \"\" (idle)", k)
	}
	h.stopUI()
}

// TestNewQueryTaskStopTwiceNoop — Stop+Stop yields a single onDone
// invocation.
func TestNewQueryTaskStopTwiceNoop(t *testing.T) {
	h := newTestHarness()
	stream := newStubRowStream(1000)
	var onDoneCalls atomic.Int32

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		func(_ []models.Row) {},
		50,
		func(error) { onDoneCalls.Add(1) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	h.mgr.Stop()
	h.mgr.Stop() // second call must be a no-op

	h.workersWG.Wait()
	if n := onDoneCalls.Load(); n != 1 {
		t.Fatalf("onDone fired %d times, want 1", n)
	}
	h.stopUI()
}

// TestNewQueryTaskSameKeyDuplicateIsNoop — second NewQueryTask with
// the same taskKey is dropped; the original task remains in flight.
func TestNewQueryTaskSameKeyDuplicateIsNoop(t *testing.T) {
	h := newTestHarness()
	streamA := newStubRowStream(10000)
	var onDoneCallsA atomic.Int32
	var streamFnBCalls atomic.Int32

	if err := h.mgr.NewQueryTask(
		"shared",
		func(_ context.Context) (drivers.RowStream, error) { return streamA, nil },
		func(_ []models.Row) {},
		50,
		func(error) { onDoneCallsA.Add(1) },
	); err != nil {
		t.Fatalf("NewQueryTask(first): %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	if err := h.mgr.NewQueryTask(
		"shared", // same key
		func(_ context.Context) (drivers.RowStream, error) {
			streamFnBCalls.Add(1)
			return newStubRowStream(10), nil
		},
		func(_ []models.Row) {},
		5,
		func(error) {},
	); err != nil {
		t.Fatalf("NewQueryTask(dup): %v", err)
	}

	// Give the duplicate path a moment in case the implementation
	// erroneously schedules a worker.
	time.Sleep(50 * time.Millisecond)

	if c := streamFnBCalls.Load(); c != 0 {
		t.Fatalf("duplicate-key NewQueryTask invoked streamFn %d times, want 0", c)
	}
	if c := onDoneCallsA.Load(); c != 0 {
		t.Fatalf("original onDone fired %d times during duplicate-key call, want 0", c)
	}
	if k := h.mgr.TaskKey(); k != "shared" {
		t.Fatalf("after duplicate call: TaskKey=%q, want %q", k, "shared")
	}

	h.mgr.Stop()
	h.workersWG.Wait()
	if c := onDoneCallsA.Load(); c != 1 {
		t.Fatalf("after final Stop: original onDone fired %d times, want 1", c)
	}
	h.stopUI()
}

// TestResultBufferManagerGoleak — full run-and-stop cycle leaves no
// stray goroutines.
func TestResultBufferManagerGoleak(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := newTestHarness()
	stream := newStubRowStream(500)
	doneCh := make(chan struct{})

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		func(_ []models.Row) {},
		100,
		func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	h.mgr.ReadRows(100)
	h.mgr.Stop()
	h.workersWG.Wait()
	<-doneCh
	h.stopUI()
}

// TestNewQueryTaskCompletesOnNaturalEOF — a result smaller than the
// initial fill hits clean EOF during initial drain; onDone must fire
// WITHOUT any Stop()/preempt (the Gap-1 fix). Before the fix the worker
// parks on the chan loop and onDone never fires.
func TestNewQueryTaskCompletesOnNaturalEOF(t *testing.T) {
	h := newTestHarness()
	defer h.stopUI()

	stream := newStubRowStream(10) // < initialRows below
	doneCh := make(chan struct{})

	var (
		mu  sync.Mutex
		got []models.Row
	)
	appendF := h.makeAppendRows(&mu, &got)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		50, // initialRows > 10 → initial fill observes clean EOF
		func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("onDone did not fire on natural EOF within 1s (Gap 1)")
	}

	waitForCount(t, &mu, &got, 10, 200*time.Millisecond)
	if n := stream.closeCount.Load(); n != 1 {
		t.Fatalf("stream Close called %d times, want 1", n)
	}
	h.workersWG.Wait()
}

// TestNewQueryTaskCompletesOnEOFViaChanLoop — a result larger than the
// initial fill completes when a later ReadRows drain hits clean EOF.
func TestNewQueryTaskCompletesOnEOFViaChanLoop(t *testing.T) {
	h := newTestHarness()
	defer h.stopUI()

	stream := newStubRowStream(60)
	doneCh := make(chan struct{})

	var (
		mu  sync.Mutex
		got []models.Row
	)
	appendF := h.makeAppendRows(&mu, &got)

	if err := h.mgr.NewQueryTask(
		"q1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF, 50, func(error) { close(doneCh) },
	); err != nil {
		t.Fatalf("NewQueryTask: %v", err)
	}
	waitForCount(t, &mu, &got, 50, 200*time.Millisecond)

	h.mgr.ReadRows(50) // drains remaining 10 → clean EOF → exit

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("onDone did not fire after EOF via chan loop")
	}
	waitForCount(t, &mu, &got, 60, 200*time.Millisecond)
	h.workersWG.Wait()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func waitForCount(t *testing.T, mu *sync.Mutex, dst *[]models.Row, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		mu.Lock()
		n := len(*dst)
		mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("appendRows: got %d rows after %s, want >= %d", n, within, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
