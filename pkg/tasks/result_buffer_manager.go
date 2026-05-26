package tasks

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ResultBufferManager is the SQL-row analogue of lazygit's
// ViewBufferManager (pkg/tasks/tasks.go:31-149 in the vendored fork).
// It owns the lifecycle of a single in-flight streaming query: starts
// the stream on the worker pool, does an initial-fill drain that
// pre-paints the first page of rows synchronously, then switches to a
// chan-driven pull loop that delivers more rows on demand
// (ReadRows / ReadToEnd). All deliveries to the row-sink callback
// (appendRows) are routed back through onUIThread so the grid view
// only mutates on the gocui main loop.
//
// Preemption: a second NewQueryTask call with a *different* taskKey
// stops the running task (closes its RowStream and waits for its
// onDone) before starting the new one. A second call with the *same*
// taskKey is treated as a duplicate request and is a no-op — this
// matches the user-visible behavior of lazygit's "switch back to the
// same selection" path.
//
// All exported methods are safe for concurrent use from any goroutine.
//
// DESIGN.md §12.1; epic dbsavvy-66p §Shared Artifacts Registry.
type ResultBufferManager struct {
	// onWorker spawns a background goroutine via the orchestrator
	// (pkg/gui/orchestrator/threading.go:OnWorker). It increments the
	// busy counter and tracks the goroutine on shutdownWG so Close()
	// + goleak see a clean exit.
	onWorker func(func(gocui.Task) error)

	// onUIThread schedules fn for execution on the gocui MainLoop.
	// Signature matches orchestrator.Gui.OnUIThread (returns nothing;
	// the driver enqueues onto userEvents and returns immediately).
	// All appendRows invocations go through this seam.
	onUIThread func(func() error)

	// mu protects taskKey, stopCurrentTask, rowsToRead. Held briefly
	// during NewQueryTask hand-off and during Stop. The worker
	// goroutine reads rowsToRead via the channel value captured at
	// startup, so it never contends with mu after launch.
	mu sync.Mutex

	// taskKey identifies the currently running task. Empty string
	// means "idle, no task running". Tests and the future
	// GridView consult this to suppress duplicate launches.
	taskKey string

	// stopCurrentTask is set by NewQueryTask while a task is in
	// flight. Calling it closes the task's stop chan and blocks
	// until the worker has finished its cleanup (RowStream.Close +
	// onDone). Nil when no task is running. Wrapped in sync.Once so
	// a double-Stop is safe.
	stopCurrentTask func()

	// rowsToRead is the per-task pull-request channel. It is
	// re-created on every NewQueryTask so a stale request from a
	// preempted task cannot leak into the new task. Nil when no
	// task is running.
	rowsToRead chan RowsToRead

	// estimatedRows is the optimiser's row-count estimate for the
	// in-flight query. Populated at stream open by callers that have
	// a planner-side estimate handy (e.g. EXPLAIN FORMAT JSON's top-
	// level "Plan Rows"). A value of 0 means "unknown"; callers that
	// need to gate on a row-count threshold treat unknown as
	// conservative (e.g. show a warning prompt).
	//
	// dbsavvy-uv0.3: seeded externally via Store / left at zero. The
	// real EXPLAIN-side seed is deferred — for now this field is
	// always 0 in production; consumers must handle the unknown case.
	// TODO(dbsavvy-uv0.4+): wire a real EXPLAIN seed.
	estimatedRows atomic.Int64

	// log is the optional structured logger used by dbsavvy-8s2.7 for
	// cat=state RBM lifecycle events. Nil-tolerant via logs.Event.
	log *slog.Logger
}

// SetLogger wires the per-session structured logger consumed by the
// dbsavvy-8s2.7 cat=state instrumentation (rbm_task_launch /
// rbm_task_cleanup / rbm_estimated_rows). Safe to call post-construction
// before the first NewQueryTask.
func (m *ResultBufferManager) SetLogger(l *slog.Logger) { m.log = l }

// New constructs a ResultBufferManager wired to the given orchestrator
// threading helpers. Pass orchestrator.Gui.OnWorker and
// orchestrator.Gui.OnUIThreadContentOnly directly (the content-only
// variant is the documented choice for high-frequency row deliveries
// per DESIGN.md §6, but plain OnUIThread also satisfies the contract).
//
// New does not start any goroutines. The first goroutine is spawned by
// NewQueryTask.
func New(
	onWorker func(func(gocui.Task) error),
	onUIThread func(func() error),
) *ResultBufferManager {
	return &ResultBufferManager{
		onWorker:   onWorker,
		onUIThread: onUIThread,
	}
}

// TaskKey returns the key of the currently running task, or "" when
// the manager is idle. Safe to call from any goroutine.
func (m *ResultBufferManager) TaskKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.taskKey
}

// EstimatedRows returns the optimiser's row-count estimate seeded into
// the manager, or 0 when unknown. dbsavvy-uv0.3.
func (m *ResultBufferManager) EstimatedRows() int64 {
	return m.estimatedRows.Load()
}

// SetEstimatedRows stores the planner-side row-count estimate. Intended
// to be called once at stream open by code that has just parsed an
// EXPLAIN result. dbsavvy-uv0.3.
func (m *ResultBufferManager) SetEstimatedRows(n int64) {
	m.estimatedRows.Store(n)
	logs.Event(m.log, "state", "rbm_estimated_rows", slog.Int64("n", n))
}

// ReadRows requests the worker pull up to n more rows from the
// current RowStream and dispatch them via appendRows. No-op when the
// manager is idle. Non-blocking: the request is enqueued on the
// rowsToRead chan (which has buffer 1024 like lazygit's; see
// NewQueryTask), and the worker picks it up on its next iteration.
//
// Order is preserved: requests are pulled FIFO by the single worker
// goroutine, and each request's rows are delivered in arrival order
// from the underlying RowStream.
func (m *ResultBufferManager) ReadRows(n int) {
	m.mu.Lock()
	ch := m.rowsToRead
	m.mu.Unlock()
	if ch == nil {
		return
	}
	// Non-blocking send into a 1024-buffer chan: in practice this
	// never blocks; if a misbehaving caller fires thousands of
	// requests faster than the worker drains, the send blocks and
	// applies natural back-pressure rather than dropping requests.
	ch <- RowsToRead{Total: n, InitialRefreshAfter: -1}
}

// ReadToEnd requests the worker drain the rest of the RowStream and
// then invoke `then` (if non-nil) exactly once. When the manager is
// idle, `then` is invoked synchronously so callers can rely on it
// firing in both cases.
func (m *ResultBufferManager) ReadToEnd(then func()) {
	m.mu.Lock()
	ch := m.rowsToRead
	m.mu.Unlock()
	if ch == nil {
		if then != nil {
			then()
		}
		return
	}
	ch <- RowsToRead{Total: -1, InitialRefreshAfter: -1, Then: then}
}

// Stop preempts the currently running task. Closes the RowStream,
// drains the worker goroutine, and invokes the task's onDone callback
// exactly once. No-op when the manager is idle. Safe to call twice
// (second call observes a nil stopCurrentTask and returns).
func (m *ResultBufferManager) Stop() {
	m.mu.Lock()
	stop := m.stopCurrentTask
	m.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
}

// NewQueryTask starts (or replaces) the streaming task identified by
// taskKey. Returns nil immediately on the duplicate-key fast path; in
// all other cases blocks only long enough to (a) preempt any prior
// task and (b) schedule the new worker via onWorker. The actual
// initial-fill drain runs asynchronously inside the worker.
//
//   - taskKey   identifies the task for preemption / dedup. Two
//     consecutive calls with the same key are a no-op.
//   - streamFn  is invoked once inside the worker to open the
//     RowStream. ctx is the per-task context (cancelled on Stop /
//     preempt). If streamFn returns an error, onDone is invoked and
//     the manager returns to idle without ever touching the chan loop.
//   - appendRows is the row sink. It receives a fresh slice on every
//     call. The manager guarantees this is invoked on the UI thread
//     (via onUIThread) and never from the worker goroutine.
//   - initialRows is the size of the synchronous initial-fill drain.
//     A value of 0 skips the initial fill entirely (the worker starts
//     the chan loop immediately).
//   - onDone is invoked exactly once when the task completes (clean
//     EOF, stream error, or Stop / preempt).
func (m *ResultBufferManager) NewQueryTask(
	taskKey string,
	streamFn func(ctx context.Context) (drivers.RowStream, error),
	appendRows func([]models.Row),
	initialRows int,
	onDone func(),
) error {
	m.mu.Lock()

	// Duplicate-key fast path: the AC scenario "second NewQueryTask
	// with same taskKey is no-op" — return without disturbing the
	// running task. onDone for the *new* call is intentionally
	// dropped (no task ran for it); this matches the user-visible
	// semantics of re-selecting the same row in lazygit.
	if taskKey != "" && taskKey == m.taskKey && m.stopCurrentTask != nil {
		m.mu.Unlock()
		return nil
	}

	priorStop := m.stopCurrentTask
	m.mu.Unlock()

	// Capture preempted_prior BEFORE running priorStop (AC: the field
	// must reflect "there was a prior task" at the time NewQueryTask
	// was invoked, not what the manager state looks like afterwards).
	preemptedPrior := priorStop != nil
	logs.Event(m.log, "state", "rbm_task_launch",
		slog.String("taskKey", taskKey),
		slog.Bool("preempted_prior", preemptedPrior),
		slog.Int("rows_to_read", initialRows),
	)

	// Preempt the prior task synchronously. priorStop blocks until
	// the prior worker has closed its RowStream and fired its
	// onDone, so by the time we proceed the manager is guaranteed
	// idle from the prior task's perspective.
	if priorStop != nil {
		priorStop()
	}

	// Per-task state. rowsToRead is buffered to 1024 to match
	// lazygit's ViewBufferManager (line 187). The buffer absorbs
	// bursts of ReadRows calls from rapid scroll without forcing
	// the UI thread to block.
	rowsToRead := make(chan RowsToRead, 1024)
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	var stopOnce sync.Once
	stopFn := func() {
		stopOnce.Do(func() {
			close(stopCh)
		})
		// Block until the worker has finished cleanup. Safe to
		// call concurrently — the receive on a closed chan returns
		// immediately for every caller after the worker closes
		// doneCh.
		<-doneCh
	}

	m.mu.Lock()
	m.taskKey = taskKey
	m.stopCurrentTask = stopFn
	m.rowsToRead = rowsToRead
	m.mu.Unlock()

	// Capture so the worker's clearState closure runs against the
	// exact rowsToRead chan it owns (defensive against a follow-up
	// NewQueryTask racing with our cleanup — that NewQueryTask
	// would already have replaced m.rowsToRead with its own chan).
	myRowsToRead := rowsToRead
	myStopFn := stopFn

	m.onWorker(func(_ gocui.Task) error {
		m.runTask(
			taskKey,
			streamFn,
			appendRows,
			initialRows,
			onDone,
			rowsToRead,
			stopCh,
			doneCh,
			myRowsToRead,
			myStopFn,
		)
		return nil
	})

	return nil
}

// runTask is the worker-goroutine body. It owns the RowStream for
// the task's entire lifetime: opens via streamFn, drains the initial
// fill synchronously, services the chan loop, and on exit closes the
// stream + clears manager state + fires onDone (once).
//
// All paths through this function MUST go through the final
// `defer cleanup()` so onDone fires exactly once on every exit.
func (m *ResultBufferManager) runTask(
	taskKey string,
	streamFn func(ctx context.Context) (drivers.RowStream, error),
	appendRows func([]models.Row),
	initialRows int,
	onDone func(),
	rowsToRead chan RowsToRead,
	stopCh chan struct{},
	doneCh chan struct{},
	myRowsToRead chan RowsToRead,
	myStopFn func(),
) {
	var (
		stream     drivers.RowStream
		onDoneOnce sync.Once
		fireOnDone = func() {
			if onDone != nil {
				onDoneOnce.Do(onDone)
			}
		}
	)

	// cleanup is the single exit point. It clears manager state
	// (only if we are still the registered task — a preempting
	// NewQueryTask may have already overwritten it), closes the
	// stream if it was ever opened, and fires onDone. close(doneCh)
	// unblocks any in-flight Stop callers waiting on stopFn.
	cleanup := func() {
		m.mu.Lock()
		// Only clear if we are still the active task. A preempt
		// from NewQueryTask has already replaced these fields.
		cleared := m.rowsToRead == myRowsToRead
		if cleared {
			m.taskKey = ""
			m.stopCurrentTask = nil
			m.rowsToRead = nil
		}
		m.mu.Unlock()

		if stream != nil {
			_ = stream.Close()
		}
		fireOnDone()
		close(doneCh)
		_ = myStopFn // keep referenced; prevents Go vet noise about unused capture
		logs.Event(m.log, "state", "rbm_task_cleanup", slog.String("taskKey", taskKey), slog.Bool("cleared", cleared))
	}
	defer cleanup()

	// Per-task context cancelled when stopCh fires. Passed to
	// streamFn and to every RowStream.Next call so a long-running
	// driver call returns promptly on Stop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-doneCh:
			// task finished on its own; cancel is run by the
			// outer defer.
		}
	}()

	s, err := streamFn(ctx)
	if err != nil {
		// streamFn failure: onDone fires (via cleanup), no rows
		// delivered, manager returns to idle.
		return
	}
	stream = s

	// Early-stop check AFTER capturing the stream: if Stop fired before
	// the worker was scheduled, return now — but only once `stream` is set
	// so the deferred cleanup closes it. In dbsavvy the stream is opened
	// (and the per-session streamMu locked) by SQLSession.Stream before
	// this worker runs, and streamFn just hands it back, so bailing here
	// WITHOUT closing would orphan the open stream and leak streamMu —
	// deadlocking the next run (dbsavvy-dk6). streamFn is a zero-cost
	// `return rh.Rows(), nil`, so always calling it is free.
	select {
	case <-stopCh:
		return
	default:
	}

	// --- Initial fill ---
	//
	// Pull up to initialRows rows synchronously and dispatch them
	// in a single appendRows batch on the UI thread. Lazygit
	// pre-paints lines from the scanner the same way (tasks.go:262;
	// the InitialRefreshAfter knob) — the "first page is on screen
	// before the chan loop starts" property is what AC scenario
	// "Initial fill pre-paints rows" requires.
	if initialRows > 0 {
		initial, eof := m.drainRows(ctx, stream, initialRows, stopCh)
		if len(initial) > 0 {
			m.dispatchRows(initial, appendRows)
		}
		if eof {
			// Whole result fit within the initial fill: the stream
			// is exhausted, so exit now. The deferred cleanup fires
			// onDone → markCompleteOnUI (StateComplete) without
			// needing a Stop / preempt (Gap 1).
			return
		}
	}

	// --- Chan-driven pull loop ---
	for {
		select {
		case <-stopCh:
			return
		case req, ok := <-rowsToRead:
			if !ok {
				// chan closed: only happens if someone external
				// closed it (we never do). Treat as stop.
				return
			}
			eof := m.servicePullRequest(ctx, stream, req, appendRows, stopCh)
			if req.Then != nil {
				req.Then()
			}
			if eof {
				// Stream exhausted by a clean EOF: exit so the
				// deferred cleanup fires onDone → StateComplete
				// (Gap 1). A mid-stream error returns eof=false
				// and keeps looping until Stop / preempt.
				return
			}
		}
	}
}

// drainRows pulls up to want rows from the stream, respecting stop.
// Returns the slice of rows pulled (possibly empty, possibly < want
// on EOF / error / stop) and a bool reporting whether a *clean* EOF
// was observed. The clean-EOF flag is distinct from error: a mid-stream
// error returns eof=false so the worker keeps looping until Stop /
// preempt (preserving TestNewQueryTaskNextErrorMidStream); only an
// orderly end-of-stream (Next reports ok=false, err=nil) returns
// eof=true, which lets the caller exit and fire onDone. Stop also
// returns eof=false — stopCh is not EOF.
//
// want=-1 means "drain to end".
func (m *ResultBufferManager) drainRows(
	ctx context.Context,
	stream drivers.RowStream,
	want int,
	stopCh <-chan struct{},
) ([]models.Row, bool) {
	out := make([]models.Row, 0, max(want, 0))
	for i := 0; want == -1 || i < want; i++ {
		select {
		case <-stopCh:
			return out, false // stop is not EOF
		default:
		}
		row, ok, err := stream.Next(ctx)
		if err != nil {
			return out, false // error: NOT clean EOF (loop-until-Stop preserved)
		}
		if !ok {
			return out, true // clean EOF
		}
		out = append(out, row)
	}
	return out, false // filled `want` without observing the end
}

// servicePullRequest handles a single RowsToRead from the chan loop.
// Pulls up to req.Total rows (req.Total=-1 means drain), dispatches
// them in a single batch on the UI thread, and returns whether a clean
// EOF was observed. The batch is dispatched only if non-empty so the UI
// side never sees a spurious zero-row append.
func (m *ResultBufferManager) servicePullRequest(
	ctx context.Context,
	stream drivers.RowStream,
	req RowsToRead,
	appendRows func([]models.Row),
	stopCh <-chan struct{},
) bool {
	batch, eof := m.drainRows(ctx, stream, req.Total, stopCh)
	if len(batch) > 0 {
		m.dispatchRows(batch, appendRows)
	}
	return eof
}

// dispatchRows schedules a single appendRows call on the UI thread.
// This is the only call site that invokes appendRows; centralising it
// makes the "appendRows is always on the UI thread" invariant
// trivially auditable. When onUIThread is nil (defensive — should
// never happen in production) the batch is dropped.
func (m *ResultBufferManager) dispatchRows(batch []models.Row, appendRows func([]models.Row)) {
	if m.onUIThread == nil || appendRows == nil {
		return
	}
	m.onUIThread(func() error {
		appendRows(batch)
		return nil
	})
}
