package data_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/davesavic/pgsavvy/pkg/tasks"
)

// These are end-to-end regression tests: a parked >200-row
// result holding the per-session streamMu must NOT freeze the UI when a
// second UI-goroutine session op (explain / explain-analyze / fk-forward /
// fk-reverse — all funnelled through QueryRunner.RunQuery) runs after it.
//
// Unlike the recorder-fake unit tests in query_runner_test.go, these compose
// the REAL collaborators so the test actually exercises the lock:
//
//   - a REAL *session.SQLSession whose streamMu genuinely locks (Stream
//     locks at Stream() and only unlocks in RunHandle.finish());
//   - a REAL *tasks.ResultBufferManager whose worker goroutine drains the
//     200-row initial fill and then settles into its chan loop (never
//     reaching EOF), holding the lock;
//   - a REAL ui.ResultTabsHelper.PreemptInFlight wired as the QueryRunner
//     preempter via SetPreempter — exactly as production does in
//     orchestrator/gui.go:551.
//
// The only fake is a drivers.Session whose row stream blocks (never reaches
// EOF) after the initial fill — that is the deterministic, no-live-DB stand-in
// for a large server-side result set parked past the initial-fill window.
//
// If the chokepoint preempt (QueryRunner.preemptInFlight) is reverted, the
// second op blocks on streamMu forever and each test TIMES OUT (t.Fatal)
// within 2s rather than hanging the suite.

// blockingRowStream models a server-side result set larger than the
// initial-fill window: it yields rows without ever reaching EOF, so the RBM
// worker drains the initial fill and then sits in its chan loop while
// SQLSession.streamMu stays locked (RunHandle.finish never runs). This is the
// exact parked-stream state — the lock is held by an in-flight stream that
// will not release it on its own.
//
// parkedCh is closed once the stream has yielded signalAfter rows (== the
// initial-fill count), i.e. exactly when the worker has finished the initial
// drain and is about to settle into the chan loop holding the lock. The test
// waits on it to guarantee the lock is genuinely held before issuing the
// second op.
//
// Once the per-task context is cancelled (what RBM.Stop does), any in-flight
// or subsequent Next returns promptly with ctx.Err() — mirroring a real pgx
// RowStream.Next — so Stop can drain the worker and release the lock.
type blockingRowStream struct {
	qid         models.QueryID
	signalAfter int
	idx         int
	closed      bool

	parkOnce sync.Once
	parkedCh chan struct{}

	// closeGate, when non-nil, blocks Close() until it is closed — simulating
	// the incident's driver behavior where rows.Close() drains until the
	// server-side query finishes rather than aborting. It wedges the RBM
	// worker inside release() so PreemptInFlight's Stop-wait cannot drain
	// within its bound, exercising the abandon path. Nil = instant
	// Close (the default cancellable-worker behavior).
	closeGate chan struct{}
}

func newBlockingRowStream(signalAfter int) *blockingRowStream {
	return &blockingRowStream{
		qid:         models.QueryID{SessionID: 7, Nonce: 1},
		signalAfter: signalAfter,
		parkedCh:    make(chan struct{}),
	}
}

func (s *blockingRowStream) Columns() []models.ColumnMeta { return nil }
func (s *blockingRowStream) QueryID() models.QueryID      { return s.qid }
func (s *blockingRowStream) RowsAffected() int64          { return 0 }

func (s *blockingRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	// Promptly honour a cancelled task context (RBM.Stop / preempt) so the
	// worker can return, close the stream, and release streamMu.
	select {
	case <-ctx.Done():
		return models.Row{}, false, ctx.Err()
	default:
	}
	row := models.Row{Values: []any{s.idx}}
	s.idx++
	if s.idx >= s.signalAfter {
		// The initial-fill drain has pulled its full quota; the worker is
		// about to settle into the chan loop holding streamMu. Never reaches
		// EOF, so the lock is parked until Stop fires.
		s.parkOnce.Do(func() { close(s.parkedCh) })
	}
	return row, true, nil
}

func (s *blockingRowStream) Close() error {
	if s.closeGate != nil {
		<-s.closeGate
	}
	s.closed = true
	return nil
}

// blockingSession is a minimal drivers.Session that hands back a single
// pre-staged blockingRowStream from Stream and returns zero values for the
// methods these tests don't exercise. Execute (BEGIN / ROLLBACK) and Explain
// succeed trivially — the point under test is the streamMu lock contention,
// not their bodies. Explain/Execute go through the REAL SQLSession wrappers,
// which Lock/defer-Unlock streamMu, so they are the operations that would
// deadlock against the parked Stream absent a preempt.
type blockingSession struct {
	rs *blockingRowStream
}

func (b *blockingSession) Close() error         { return nil }
func (b *blockingSession) ID() models.SessionID { return 7 }

func (b *blockingSession) ListDatabases(context.Context) ([]models.Database, error) { return nil, nil }
func (b *blockingSession) ListSchemas(context.Context, string) ([]models.Schema, error) {
	return nil, nil
}

func (b *blockingSession) ListTables(context.Context, string) ([]*models.Table, error) {
	return nil, nil
}

func (b *blockingSession) ListColumns(context.Context, string, string) ([]models.Column, error) {
	return nil, nil
}

func (b *blockingSession) ListIndexes(context.Context, string, string) ([]models.Index, error) {
	return nil, nil
}

func (b *blockingSession) ListConstraints(context.Context, string, string) ([]models.Constraint, error) {
	return nil, nil
}

func (b *blockingSession) ListForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (b *blockingSession) ListInboundForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (b *blockingSession) ListFunctions(context.Context) ([]string, error) { return nil, nil }

func (b *blockingSession) DescribeFunction(context.Context, string, string) ([]models.FunctionDetail, error) {
	return nil, nil
}

func (b *blockingSession) Execute(context.Context, models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (b *blockingSession) Stream(context.Context, models.Query) (drivers.RowStream, error) {
	return b.rs, nil
}

func (b *blockingSession) Explain(context.Context, models.Query, bool) (models.Plan, error) {
	return models.Plan{RawText: "real-explain"}, nil
}

func (b *blockingSession) Begin(context.Context, models.TxOptions) (drivers.Transaction, error) {
	return &nopTransaction{}, nil
}

type nopTransaction struct{}

func (nopTransaction) Commit(context.Context) error                { return nil }
func (nopTransaction) Rollback(context.Context) error              { return nil }
func (nopTransaction) Savepoint(context.Context, string) error     { return nil }
func (nopTransaction) Release(context.Context, string) error       { return nil }
func (nopTransaction) RollbackTo(context.Context, string) error    { return nil }
func (nopTransaction) Savepoints() []string                        { return nil }
func (nopTransaction) Status() models.TxStatus                     { return models.TxActive }
func (nopTransaction) ObserveError(error)                          {}
func (nopTransaction) StatementCount() int                         { return 0 }
func (b *blockingSession) InTransaction() bool                     { return false }
func (b *blockingSession) CurrentTransaction() drivers.Transaction { return nil }
func (b *blockingSession) Encoder() drivers.Encoder                { return nopEncoder{} }

type nopEncoder struct{}

func (nopEncoder) EncodeLiteral(any, uint32) string { return "NULL" }

var _ drivers.Session = (*blockingSession)(nil)

// parkedStreamFixture wires the REAL SQLSession + REAL ResultBufferManager +
// REAL ResultTabsHelper + REAL QueryRunner, starts a >200-row stream that
// parks holding streamMu, and returns the runner with the preempter installed.
//
// The caller issues the second session op through runner and asserts it does
// NOT deadlock. cleanup() preempts any still-parked stream and joins every
// worker goroutine so goleak sees a clean exit.
type parkedStreamFixture struct {
	runner  *data.QueryRunner
	tabs    *ui.ResultTabsHelper
	sess    *session.SQLSession
	parked  chan struct{}
	workers *sync.WaitGroup
}

func newParkedStreamFixture(t *testing.T) *parkedStreamFixture {
	t.Helper()

	// The initial fill drains exactly 200 rows (resultTabInitialRows); the
	// stream never reaches EOF, so the worker settles into its chan loop
	// holding streamMu. Signalling after the 200th row marks that parked state.
	rs := newBlockingRowStream(200)
	inner := &blockingSession{rs: rs}
	sess := session.New(&parkedConn{}, inner, session.Options{})

	var workers sync.WaitGroup
	onWorker := func(fn func(gocui.Task) error) {
		workers.Go(func() {
			_ = fn(nil)
		})
	}
	// RBM marshals appendRows / completion flips through onUIThread; running
	// them synchronously is safe here (no gocui main loop) and keeps the test
	// deterministic.
	onUIThread := func(fn func() error) { _ = fn() }

	tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{
		StreamFactory: func() ui.StreamRunner {
			return tasks.New(onWorker, onUIThread)
		},
		OnUIThread: onUIThread,
	})

	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{})
	// This is the line under test: the production chokepoint wiring
	// (orchestrator/gui.go:551). Removing it reintroduces the freeze.
	runner.SetPreempter(tabs.PreemptInFlight)

	return &parkedStreamFixture{
		runner:  runner,
		tabs:    tabs,
		sess:    sess,
		parked:  rs.parkedCh,
		workers: &workers,
	}
}

// startParkedStream launches the big SELECT through the runner, opens a result
// tab for it (which starts the RBM worker), and blocks until the worker has
// drained the initial fill and settled into its chan loop — i.e. until streamMu
// is genuinely held by an in-flight stream that will never reach EOF on its own.
func (f *parkedStreamFixture) startParkedStream(t *testing.T) {
	t.Helper()
	rh, err := f.runner.Run(context.Background(), "SELECT * FROM big", data.RunOptions{})
	if err != nil {
		t.Fatalf("Run(parked stream) err = %v", err)
	}
	if err := f.tabs.OpenResultTab("big", rh); err != nil {
		t.Fatalf("OpenResultTab err = %v", err)
	}
	select {
	case <-f.parked:
		// Worker has drained 200 rows and is parked in its chan loop holding streamMu.
	case <-time.After(2 * time.Second):
		t.Fatal("stream never parked: worker did not reach the blocking Next within 2s")
	}
}

func (f *parkedStreamFixture) cleanup(t *testing.T) {
	t.Helper()
	// PreemptInFlight stops any still-running tab, freeing the worker and
	// streamMu. Then Close the session and join workers so goleak is clean.
	f.tabs.PreemptInFlight()
	if err := f.sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
	done := make(chan struct{})
	go func() { f.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker goroutines did not exit within 2s during cleanup")
	}
}

// parkedConn is the drivers.Connection the SQLSession wraps. Only Cancel is
// reachable from these tests (via RunHandle.cancelFn); the rest are stubs.
type parkedConn struct{}

func (*parkedConn) Close() error                                            { return nil }
func (*parkedConn) Ping(context.Context) error                              { return nil }
func (*parkedConn) ServerVersion() string                                   { return "fake" }
func (*parkedConn) AcquireSession(context.Context) (drivers.Session, error) { return nil, nil }
func (*parkedConn) Cancel(context.Context, models.QueryID) error            { return nil }

// runWithDeadline runs op on a goroutine and fails the test if it has not
// returned within 2s — a deadlock on streamMu manifests as this timeout, never
// a hung suite.
func runWithDeadline(t *testing.T, what string, op func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- op() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s returned err = %v", what, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("DEADLOCK: %s blocked on the parked-stream streamMu for >2s "+
			"(the QueryRunner preempt chokepoint did not fire)", what)
	}
}

// TestParkedStreamThenExplainDoesNotDeadlock proves the `explain` caller
// (analyze=false) preempts the parked stream before its session Explain locks
// streamMu. Carries the goleak check for the suite.
func TestParkedStreamThenExplainDoesNotDeadlock(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	f := newParkedStreamFixture(t)
	f.startParkedStream(t)

	runWithDeadline(t, "Explain(analyze=false)", func() error {
		_, err := f.runner.Explain(context.Background(), "SELECT 1", false, "")
		return err
	})

	f.cleanup(t)
}

// TestParkedStreamThenExplainAnalyzeDoesNotDeadlock proves the
// `explain_analyze` caller preempts before the BEGIN/Explain/ROLLBACK wrap —
// the first lock taken is the Execute(BEGIN), which would deadlock against the
// parked stream absent the preempt.
func TestParkedStreamThenExplainAnalyzeDoesNotDeadlock(t *testing.T) {
	f := newParkedStreamFixture(t)
	f.startParkedStream(t)

	runWithDeadline(t, "Explain(analyze=true)", func() error {
		_, err := f.runner.Explain(context.Background(), "INSERT INTO t VALUES (1)", true, "")
		return err
	})

	f.cleanup(t)
}

// TestParkedStreamThenFKForwardDoesNotDeadlock proves the `fk-forward` caller
// preempts before its session Stream locks streamMu. fk-forward funnels through
// QueryRunner.RunQuery (the shared chokepoint); we exercise RunQuery directly
// rather than driving FKForwardHelper.Jump (owned by a parallel task).
func TestParkedStreamThenFKForwardDoesNotDeadlock(t *testing.T) {
	f := newParkedStreamFixture(t)
	f.startParkedStream(t)

	runWithDeadline(t, "RunQuery(fk-forward)", func() error {
		rh, err := f.runner.RunQuery(context.Background(),
			models.Query{SQL: "SELECT * FROM parent WHERE id = $1", Args: []any{42}})
		if err == nil && rh != nil {
			// Close the freshly-acquired stream so its RunHandle.finish()
			// releases streamMu — otherwise cleanup's session Close would have
			// to wait out the parked second stream. The op under test (the
			// Stream that re-locks streamMu) has already returned by here.
			_ = rh.Rows().Close()
		}
		return err
	})

	f.cleanup(t)
}

// TestParkedStreamThenFKReverseDoesNotDeadlock proves the `fk-reverse` caller
// preempts before its session Stream locks streamMu. Like fk-forward, reverse
// funnels through QueryRunner.RunQuery; we exercise that chokepoint directly.
func TestParkedStreamThenFKReverseDoesNotDeadlock(t *testing.T) {
	f := newParkedStreamFixture(t)
	f.startParkedStream(t)

	runWithDeadline(t, "RunQuery(fk-reverse)", func() error {
		rh, err := f.runner.RunQuery(context.Background(),
			models.Query{SQL: "SELECT * FROM child WHERE parent_id = $1", Args: []any{42}})
		if err == nil && rh != nil {
			_ = rh.Rows().Close()
		}
		return err
	})

	f.cleanup(t)
}

// newAbandonFixture mirrors newParkedStreamFixture but (a) wedges the parked
// stream's Close() so the RBM worker cannot finish when Stop fires, and (b)
// injects a fire-immediately preempt Stop-wait timer. Together these force the
// abandon path deterministically: the cancel+Stop loop never drains, so
// PreemptInFlight expires its bound and the QueryRunner fences the session.
func newAbandonFixture(t *testing.T) (*parkedStreamFixture, *blockingRowStream) {
	t.Helper()

	rs := newBlockingRowStream(200)
	rs.closeGate = make(chan struct{})
	inner := &blockingSession{rs: rs}
	sess := session.New(&parkedConn{}, inner, session.Options{})

	var workers sync.WaitGroup
	onWorker := func(fn func(gocui.Task) error) {
		workers.Go(func() {
			_ = fn(nil)
		})
	}
	onUIThread := func(fn func() error) { _ = fn() }

	// Fire the bound immediately. With Close() wedged the loop's done channel
	// never closes, so the timer branch wins deterministically (no flake).
	fireNow := func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}

	tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{
		StreamFactory: func() ui.StreamRunner {
			return tasks.New(onWorker, onUIThread)
		},
		OnUIThread: onUIThread,
		After:      fireNow,
	})

	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{})
	runner.SetPreempter(tabs.PreemptInFlight)

	return &parkedStreamFixture{
		runner:  runner,
		tabs:    tabs,
		sess:    sess,
		parked:  rs.parkedCh,
		workers: &workers,
	}, rs
}

// TestParkedStreamAbandonFencesSessionNoDeadlock is the AD4 end-to-end
// guard for an UNcancellable worker: a parked stream whose Close() wedges (the
// incident's drain-don't-abort behavior) cannot drain within the preempt
// Stop-wait bound. The bound expires, the QueryRunner fences the session, and
// the NEXT op returns ErrPreemptPending fast instead of deadlocking on the
// still-held streamMu. When the worker finally exits, the fence clears and the
// abandoned Stop goroutine unblocks (goleak verifies no leak).
func TestParkedStreamAbandonFencesSessionNoDeadlock(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	f, rs := newAbandonFixture(t)
	f.startParkedStream(t)

	// The dependent op preempts; the wedged Close makes the Stop-wait expire,
	// fencing the session. Explain must return ErrPreemptPending fast, never
	// block on the still-held streamMu.
	done := make(chan error, 1)
	go func() {
		_, err := f.runner.Explain(context.Background(), "SELECT 1", false, "")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, session.ErrPreemptPending) {
			t.Fatalf("Explain after preempt-abandon = %v, want ErrPreemptPending", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: Explain blocked on streamMu after the preempt Stop-wait was abandoned")
	}

	// A further op while the worker is still wedged is likewise fenced, and
	// starts no new stream.
	if _, err := f.runner.Run(context.Background(), "SELECT 2", data.RunOptions{}); !errors.Is(err, session.ErrPreemptPending) {
		t.Fatalf("Run during fence = %v, want ErrPreemptPending", err)
	}

	// Release the wedged Close: the worker finishes, onFinish clears the fence
	// and releases streamMu, and the abandoned Stop goroutine unblocks.
	close(rs.closeGate)
	f.cleanup(t)
}
