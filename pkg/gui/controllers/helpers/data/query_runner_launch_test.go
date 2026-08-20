package data_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// This file covers the C2 single-flight launch queue: RunAsync /
// RunQueryAsync enqueue a launch and return immediately; ONE launcher
// goroutine consumes requests FIFO, running the preempt-first chokepoint
// and the session op off the UI thread. The sentinel stored in
// r.last before enqueue makes a tab-less launch last-wins cancellable.

// --- fake RunnerSession with gated, ctx-honoring Stream calls ---

// ackResult carries one launch ack.
type ackResult struct {
	rh  *session.RunHandle
	err error
}

// gateSess records every session call (mutex-guarded; calls arrive on the
// launcher goroutine) and hands back staged Stream results. A staged gate
// blocks the Stream call until released OR the launch ctx is cancelled —
// the deterministic stand-in for a slow server-side statement.
type gateSess struct {
	mu      sync.Mutex
	events  []string
	staged  []gateStagedStream
	fenced  bool
	lastTx  *gateTx
	inTx    bool
	cancels []models.QueryID
}

type gateStagedStream struct {
	gate chan struct{}
	err  error
	rh   *session.RunHandle
}

func (g *gateSess) record(ev string) {
	g.mu.Lock()
	g.events = append(g.events, ev)
	g.mu.Unlock()
}

func (g *gateSess) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.events))
	copy(out, g.events)
	return out
}

func (g *gateSess) stage(s gateStagedStream) {
	g.mu.Lock()
	g.staged = append(g.staged, s)
	g.mu.Unlock()
}

func (g *gateSess) Execute(_ context.Context, q models.Query) (models.Result, error) {
	g.record("exec:" + q.SQL)
	return models.Result{}, nil
}

func (g *gateSess) Stream(ctx context.Context, q models.Query) (*session.RunHandle, error) {
	g.mu.Lock()
	if g.fenced {
		g.mu.Unlock()
		g.record("stream:" + q.SQL + ":fenced")
		return nil, session.ErrPreemptPending
	}
	if len(g.staged) == 0 {
		g.mu.Unlock()
		g.record("stream:" + q.SQL + ":unstaged")
		return nil, errors.New("gateSess: no staged streams")
	}
	spec := g.staged[0]
	g.staged = g.staged[1:]
	g.mu.Unlock()

	g.record("stream:" + q.SQL + ":start")
	if spec.gate != nil {
		select {
		case <-spec.gate:
		case <-ctx.Done():
			g.record("stream:" + q.SQL + ":ctx-aborted")
			return nil, ctx.Err()
		}
	}
	g.record("stream:" + q.SQL + ":end")
	return spec.rh, spec.err
}

func (g *gateSess) Explain(_ context.Context, q models.Query, _ bool) (models.Plan, error) {
	g.record("explain:" + q.SQL)
	return models.Plan{}, nil
}

func (g *gateSess) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	g.record("begin")
	g.mu.Lock()
	g.inTx = true
	g.lastTx = &gateTx{onRollback: func() { g.record("rollback") }}
	tx := g.lastTx
	g.mu.Unlock()
	return tx, nil
}

func (g *gateSess) InTransaction() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inTx
}

func (g *gateSess) CurrentTransaction() drivers.Transaction {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastTx
}

func (g *gateSess) LiveTxStatus() (models.TxStatus, []string) { return "", nil }

func (g *gateSess) Cancel(qid models.QueryID) error {
	g.mu.Lock()
	g.cancels = append(g.cancels, qid)
	g.mu.Unlock()
	return nil
}

func (g *gateSess) SetDisconnected(_ bool) {}
func (g *gateSess) IsDisconnected() bool   { return false }

func (g *gateSess) MarkPreemptPending() {
	g.mu.Lock()
	g.fenced = true
	g.mu.Unlock()
	g.record("fence-marked")
}

// gateTx is a drivers.Transaction stub that records its rollback.
type gateTx struct {
	rolledBack atomic.Bool
	onRollback func()
}

func (t *gateTx) Commit(context.Context) error { return nil }
func (t *gateTx) Rollback(context.Context) error {
	t.rolledBack.Store(true)
	t.onRollback()
	return nil
}
func (t *gateTx) Savepoint(context.Context, string) error  { return nil }
func (t *gateTx) Release(context.Context, string) error    { return nil }
func (t *gateTx) RollbackTo(context.Context, string) error { return nil }
func (t *gateTx) Savepoints() []string                     { return nil }
func (t *gateTx) Status() models.TxStatus                  { return models.TxActive }
func (t *gateTx) ObserveError(error)                       {}
func (t *gateTx) StatementCount() int                      { return 0 }

// waitAck waits for one ack on ch or fails the test.
func waitAck(t *testing.T, ch <-chan ackResult, what string) ackResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: no ack within 2s", what)
		return ackResult{}
	}
}

// waitForEvent polls the session events until want appears (or fails).
func waitForEvent(t *testing.T, gs *gateSess, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, ev := range gs.snapshot() {
			if ev == want {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("event %q never fired within 2s; events = %v", want, gs.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestRunAsyncEnqueueReturnsBeforeStreamResolves is AC1 at the runner
// seam: RunAsync must return after ENQUEUE (well under the 50ms budget)
// while the session Stream is still blocked, and deliver (rh, err) via
// the ack once the op resolves.
func TestRunAsyncEnqueueReturnsBeforeStreamResolves(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gate := make(chan struct{})
	gs.stage(gateStagedStream{gate: gate})
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	acks := make(chan ackResult, 1)
	start := time.Now()
	r.RunAsync(context.Background(), "SELECT slow", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("RunAsync blocked %vms before returning (want enqueue-only, <50ms)", elapsed.Milliseconds())
	}
	// The op is still parked inside Stream: no end event yet.
	for _, ev := range gs.snapshot() {
		if ev == "stream:SELECT slow:end" {
			t.Fatal("stream resolved before the gate was released")
		}
	}

	close(gate)
	got := waitAck(t, acks, "gated launch")
	if got.err != nil {
		t.Fatalf("ack err = %v, want nil", got.err)
	}
}

// TestRunQueryAsyncEnqueues mirrors AC1 for the parameterized variant.
func TestRunQueryAsyncEnqueues(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gs.stage(gateStagedStream{})
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	acks := make(chan ackResult, 1)
	r.RunQueryAsync(context.Background(), models.Query{SQL: "SELECT $1", Args: []any{42}}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	if got := waitAck(t, acks, "RunQueryAsync"); got.err != nil {
		t.Fatalf("RunQueryAsync ack err = %v", got.err)
	}
	events := gs.snapshot()
	if len(events) != 2 || events[0] != "stream:SELECT $1:start" || events[1] != "stream:SELECT $1:end" {
		t.Fatalf("events = %v, want one clean stream", events)
	}
}

// TestLaunchQueuePreemptFiresBeforeEachOp mirrors the synchronous
// chokepoint witness for the async queue: within one batch launch, each
// statement runs the preempt hook BEFORE its own session op, exactly
// once.
func TestLaunchQueuePreemptFiresBeforeEachOp(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gs.stage(gateStagedStream{})
	gs.stage(gateStagedStream{})
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	var mu sync.Mutex
	preemptAt := []int{}
	r.SetPreempter(func() bool {
		mu.Lock()
		defer mu.Unlock()
		preemptAt = append(preemptAt, len(gs.snapshot()))
		return false
	})

	var settled sync.WaitGroup
	settled.Add(2)
	r.RunStatementsAsync(context.Background(),
		[]data.StatementLaunch{{SQL: "SELECT 1"}, {SQL: "SELECT 1"}},
		func(_ int, _ *session.RunHandle, _ error) {
			settled.Done()
		})
	settled.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(preemptAt) != 2 {
		t.Fatalf("preempt fired %d times, want 2 (once per statement)", len(preemptAt))
	}
	if preemptAt[0] != 0 {
		t.Fatalf("preempt[0] saw %d session events, want 0 (must fire before the op)", preemptAt[0])
	}
	// By preempt[1] the first statement's stream start+end both happened.
	if preemptAt[1] < 2 {
		t.Fatalf("preempt[1] saw %d session events, want >= 2 (after statement 1 resolved)", preemptAt[1])
	}
}

// TestSecondLaunchCancelsQueuedStatementsOfPriorBatch is AC2 (pre-tab
// window): a newer action's enqueue cancels the prior action's sentinel
// — the statement still QUEUED behind the parked one is suppressed (its
// op never touches the session and acks context.Canceled), while the
// newer action runs clean.
func TestSecondLaunchCancelsQueuedStatementsOfPriorBatch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gate := make(chan struct{})
	gs.stage(gateStagedStream{gate: gate}) // B1 parks the launcher
	gs.stage(gateStagedStream{})           // B2 would run next (suppressed)
	gs.stage(gateStagedStream{})           // C last
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	bAcks := make([]chan ackResult, 2)
	for i := range bAcks {
		bAcks[i] = make(chan ackResult, 1)
	}
	r.RunStatementsAsync(context.Background(),
		[]data.StatementLaunch{{SQL: "SELECT B1"}, {SQL: "SELECT B2"}},
		func(i int, _ *session.RunHandle, err error) { bAcks[i] <- ackResult{err: err} })
	waitForEvent(t, gs, "stream:SELECT B1:start")

	// The newer action cancels the batch's sentinel mid-B1; B1's op runs
	// to completion (detached ctx), its abandoned rh is closed+suppressed,
	// and B2 is acked cancelled without ever starting.
	c := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT C", data.RunOptions{}, func(rh *session.RunHandle, err error) { c <- ackResult{rh, err} })
	close(gate)

	if got := waitAck(t, bAcks[0], "B1"); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("B1 (cancelled mid-op) ack err = %v, want context.Canceled", got.err)
	}
	if got := waitAck(t, bAcks[1], "B2"); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("B2 (cancelled while queued) ack err = %v, want context.Canceled", got.err)
	}
	if got := waitAck(t, c, "C"); got.err != nil {
		t.Fatalf("C ack err = %v, want nil", got.err)
	}

	for _, ev := range gs.snapshot() {
		if strings.HasPrefix(ev, "stream:SELECT B2") {
			t.Fatalf("cancelled statement B2 still reached the session: %v", gs.snapshot())
		}
	}
}

// TestSecondLaunchCancelsInFlightSentinel is AC2 for the mid-op window:
// a launch already INSIDE its session op when the next launch arrives is
// last-wins cancelled via its sentinel — the session call itself is NOT
// ctx-aborted (cancelling pgx mid-query destroys the connection), so the
// op runs to completion, execLaunch promptly Closes the abandoned
// RunHandle, and the ack surfaces context.Canceled with no tab. The
// queue stays strictly sequential throughout.
func TestSecondLaunchCancelsInFlightSentinel(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gate := make(chan struct{})
	gs.stage(gateStagedStream{gate: gate})
	gs.stage(gateStagedStream{})
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	a := make(chan ackResult, 1)
	b := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT A", data.RunOptions{}, func(rh *session.RunHandle, err error) { a <- ackResult{rh, err} })
	// Wait until A is genuinely inside Stream — only then "press Enter" again.
	waitForEvent(t, gs, "stream:SELECT A:start")
	r.RunAsync(context.Background(), "SELECT B", data.RunOptions{}, func(rh *session.RunHandle, err error) { b <- ackResult{rh, err} })

	// A's op was mid-flight at B's enqueue: it runs to completion (the
	// gate release), then the abandoned rh is closed and suppressed.
	close(gate)
	gotA := waitAck(t, a, "A")
	if gotA.rh != nil || !errors.Is(gotA.err, context.Canceled) {
		t.Fatalf("abandoned mid-op ack = (%v, %v), want (nil, context.Canceled)", gotA.rh, gotA.err)
	}
	if got := waitAck(t, b, "B"); got.err != nil {
		t.Fatalf("B ack err = %v, want nil", got.err)
	}

	events := gs.snapshot()
	// Single-flight witness: A's op fully resolved before B's began.
	aEnd, bStart := -1, -1
	for i, ev := range events {
		if ev == "stream:SELECT A:end" {
			aEnd = i
		}
		if ev == "stream:SELECT B:start" && bStart == -1 {
			bStart = i
		}
	}
	if aEnd == -1 || bStart == -1 || aEnd > bStart {
		t.Fatalf("sequence not single-flight: events = %v", events)
	}
}

// TestRunAsyncNoSessionAcksErrNoSession covers the edge path: a launch
// against an unbound runner surfaces ErrNoSession via the ack (the
// controller marshals it into surfaceErr on the UI thread).
func TestRunAsyncNoSessionAcksErrNoSession(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	r := data.NewQueryRunner(nil, drivers.Capabilities{})
	acks := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT 1", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	if got := waitAck(t, acks, "no-session launch"); !errors.Is(got.err, data.ErrNoSession) {
		t.Fatalf("ack err = %v, want ErrNoSession", got.err)
	}
}

// TestRunAsyncPreemptExpiryFencesSession mirrors the AD4 sync guard on
// the async path: a preempt hook reporting expiry fences the session so
// the launch op fails fast with ErrPreemptPending via its ack.
func TestRunAsyncPreemptExpiryFencesSession(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	r := data.NewQueryRunner(gs, drivers.Capabilities{})
	r.SetPreempter(func() bool { return true })

	acks := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT 1", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	if got := waitAck(t, acks, "fenced launch"); !errors.Is(got.err, session.ErrPreemptPending) {
		t.Fatalf("ack err = %v, want ErrPreemptPending", got.err)
	}
}

// TestLaunchQueueSpamStaysFIFOAndLeakFree covers the fan-out edge: a
// 25-statement batch resolves strictly in enqueue order (the
// single-launcher witness — per-caller goroutines could interleave), and
// the queue leaves no goroutines behind (goleak).
func TestLaunchQueueSpamStaysFIFOAndLeakFree(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	batch := make([]data.StatementLaunch, 0, 25)
	for i := 0; i < 25; i++ {
		gs.stage(gateStagedStream{})
		batch = append(batch, data.StatementLaunch{SQL: "SELECT " + string(rune('a'+i))})
	}
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	var acks sync.WaitGroup
	acks.Add(25)
	r.RunStatementsAsync(context.Background(), batch, func(_ int, _ *session.RunHandle, _ error) {
		acks.Done()
	})
	acks.Wait()

	events := gs.snapshot()
	for i := 0; i < 25; i++ {
		wantStart := 2 * i
		want := "stream:SELECT " + string(rune('a'+i)) + ":start"
		if events[wantStart] != want {
			t.Fatalf("event[%d] = %q, want %q (FIFO violated): %v", wantStart, events[wantStart], want, events)
		}
	}
}

// TestRapidActionSpamIsLastWinsAndBounded covers the rapid-Enter edge:
// while the launcher is parked inside a blocker action, many separate
// actions enqueue — only the last survives (every earlier action's
// sentinel is cancelled while still queued), everything settles, and
// goleak is clean (one launcher, no per-caller goroutines).
func TestRapidActionSpamIsLastWinsAndBounded(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	gs := &gateSess{}
	gate := make(chan struct{})
	gs.stage(gateStagedStream{gate: gate}) // blocker parks the launcher
	gs.stage(gateStagedStream{})           // the survivor's stream
	r := data.NewQueryRunner(gs, drivers.Capabilities{})

	blocker := make(chan ackResult, 1)
	r.RunAsync(context.Background(), "SELECT blocker", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		blocker <- ackResult{rh, err}
	})
	waitForEvent(t, gs, "stream:SELECT blocker:start")

	const n = 25
	var acks sync.WaitGroup
	acks.Add(n)
	for i := 0; i < n; i++ {
		r.RunAsync(context.Background(), "SELECT spam", data.RunOptions{}, func(_ *session.RunHandle, _ error) {
			acks.Done()
		})
	}
	close(gate)
	acks.Wait()
	waitAck(t, blocker, "blocker")

	// Every spam action but the last was cancelled pre-op; the survivor
	// ran exactly one clean stream.
	events := gs.snapshot()
	starts, ends := 0, 0
	for _, ev := range events {
		if strings.HasSuffix(ev, "SELECT spam:start") {
			starts++
		}
		if strings.HasSuffix(ev, "SELECT spam:end") {
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("last-wins violated: %d starts / %d ends, want 1/1 (events=%v)", starts, ends, events)
	}
}

// --- real-SQLSession fixtures (sentinel Done span + sequence exclusivity) ---

// recInnerSession is a recording drivers.Session for real-SQLSession
// composition: staged Stream() calls (optionally gated, ctx-honoring) and
// recording Begin/tx. parkedConn (preempt_deadlock_test.go, same package)
// supplies the drivers.Connection.
type recInnerSession struct {
	mu     sync.Mutex
	events []string
	staged []recInnerStream
	lastTx *recTx
	inTx   bool
}

type recInnerStream struct {
	// streamGate parks the Stream() call itself (the pre-resolution
	// window); rowGate parks the returned stream's Next() (the
	// post-resolution drain window). Either may be nil (no park).
	streamGate chan struct{}
	rowGate    chan struct{}
}

func (s *recInnerSession) record(ev string) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *recInnerSession) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.events))
	copy(out, s.events)
	return out
}

func (s *recInnerSession) stage(rs recInnerStream) {
	s.mu.Lock()
	s.staged = append(s.staged, rs)
	s.mu.Unlock()
}

// gatedRowStream parks Next until its gate closes or the ctx cancels,
// then serves a clean EOF.
type gatedRowStream struct {
	gate chan struct{}
	qid  models.QueryID
}

func (g *gatedRowStream) Columns() []models.ColumnMeta { return nil }
func (g *gatedRowStream) QueryID() models.QueryID      { return g.qid }
func (g *gatedRowStream) RowsAffected() int64          { return 0 }
func (g *gatedRowStream) Close() error                 { return nil }
func (g *gatedRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	if g.gate != nil {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return models.Row{}, false, ctx.Err()
		}
	}
	return models.Row{}, false, nil
}

func (s *recInnerSession) Stream(ctx context.Context, q models.Query) (drivers.RowStream, error) {
	s.mu.Lock()
	if len(s.staged) == 0 {
		s.mu.Unlock()
		s.record("stream:" + q.SQL + ":unstaged")
		return nil, errors.New("recInnerSession: no staged streams")
	}
	spec := s.staged[0]
	s.staged = s.staged[1:]
	s.mu.Unlock()

	s.record("stream:" + q.SQL + ":start")
	if spec.streamGate != nil {
		select {
		case <-spec.streamGate:
		case <-ctx.Done():
			s.record("stream:" + q.SQL + ":ctx-aborted")
			return nil, ctx.Err()
		}
	}
	s.record("stream:" + q.SQL + ":end")
	return &gatedRowStream{gate: spec.rowGate, qid: models.QueryID{SessionID: 7, Nonce: 1}}, nil
}

func (s *recInnerSession) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	s.record("begin")
	s.mu.Lock()
	s.inTx = true
	s.lastTx = &recTx{}
	tx := s.lastTx
	s.mu.Unlock()
	return tx, nil
}

func (s *recInnerSession) InTransaction() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inTx
}

func (s *recInnerSession) CurrentTransaction() drivers.Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastTx == nil {
		return nil
	}
	return s.lastTx
}

type recTx struct {
	committed  atomic.Bool
	rolledBack atomic.Bool
}

func (t *recTx) Commit(context.Context) error {
	t.committed.Store(true)
	return nil
}

func (t *recTx) Rollback(context.Context) error {
	t.rolledBack.Store(true)
	return nil
}
func (t *recTx) Savepoint(context.Context, string) error  { return nil }
func (t *recTx) Release(context.Context, string) error    { return nil }
func (t *recTx) RollbackTo(context.Context, string) error { return nil }
func (t *recTx) Savepoints() []string                     { return nil }
func (t *recTx) Status() models.TxStatus                  { return models.TxActive }
func (t *recTx) ObserveError(error)                       {}
func (t *recTx) StatementCount() int                      { return 0 }

// The remaining drivers.Session surface is inert for these tests.
func (s *recInnerSession) Close() error                                             { return nil }
func (s *recInnerSession) ID() models.SessionID                                     { return 7 }
func (s *recInnerSession) ListDatabases(context.Context) ([]models.Database, error) { return nil, nil }
func (s *recInnerSession) ListSchemas(context.Context, string) ([]models.Schema, error) {
	return nil, nil
}

func (s *recInnerSession) ListTables(context.Context, string) ([]*models.Table, error) {
	return nil, nil
}

func (s *recInnerSession) ListColumns(context.Context, string, string) ([]models.Column, error) {
	return nil, nil
}

func (s *recInnerSession) ListIndexes(context.Context, string, string) ([]models.Index, error) {
	return nil, nil
}

func (s *recInnerSession) ListConstraints(context.Context, string, string) ([]models.Constraint, error) {
	return nil, nil
}

func (s *recInnerSession) ListForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *recInnerSession) ListInboundForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *recInnerSession) TableStats(context.Context, string, string) (int64, int64, error) {
	return 0, 0, nil
}
func (s *recInnerSession) ListFunctions(context.Context) ([]string, error) { return nil, nil }
func (s *recInnerSession) DescribeFunction(context.Context, string, string) ([]models.FunctionDetail, error) {
	return nil, nil
}

func (s *recInnerSession) Execute(context.Context, models.Query) (models.Result, error) {
	return models.Result{}, nil
}

func (s *recInnerSession) Explain(context.Context, models.Query, bool) (models.Plan, error) {
	return models.Plan{}, nil
}
func (s *recInnerSession) LiveTxStatus() (models.TxStatus, []string) { return "", nil }
func (s *recInnerSession) Encoder() drivers.Encoder                  { return nopEncoderAlias{} }

type nopEncoderAlias struct{}

func (nopEncoderAlias) EncodeLiteral(any, uint32) string { return "NULL" }

// waitInnerEvent polls the real-session inner events until want appears.
func waitInnerEvent(t *testing.T, s *recInnerSession, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, ev := range s.snapshot() {
			if ev == want {
				return
			}
		}
		select {
		case <-deadline:
			t.Fatalf("inner event %q never fired within 2s; events = %v", want, s.snapshot())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestSentinelDoneSpansResolutionAndTermination is AC3: for a launch
// that has resolved (rh in hand) whose stream is still parked,
// CancelAndWaitActiveRun must block until the RunHandle actually
// terminates — a quit-during-gap caller cannot overtake the drain with a
// Commit.
func TestSentinelDoneSpansResolutionAndTermination(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	rowGate := make(chan struct{})
	inner.stage(recInnerStream{rowGate: rowGate}) // Stream() returns; rows park on rowGate
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT parked", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	got := waitAck(t, acks, "parked launch")
	if got.err != nil || got.rh == nil {
		t.Fatalf("ack = (%v, %v), want clean rh", got.rh, got.err)
	}

	// Park the row drain (the tab's RBM worker stand-in).
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			if _, ok, _ := got.rh.Rows().Next(context.Background()); !ok {
				return
			}
		}
	}()
	// Give the drain goroutine a moment to park inside Next.
	time.Sleep(50 * time.Millisecond)

	waitDone := make(chan struct{})
	go func() {
		runner.CancelAndWaitActiveRun()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("CancelAndWaitActiveRun returned while the RunHandle was still parked")
	case <-time.After(250 * time.Millisecond):
		// Still waiting — the sentinel Done has not closed. Good.
	}

	close(rowGate) // EOF: finish() closes rh.Done(), the watcher closes the sentinel
	<-drained
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAndWaitActiveRun did not return after the RunHandle terminated")
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
}

// TestCancelAndWaitActiveRunOnPendingLaunch proves the pending branch:
// the sentinel cancel does NOT ctx-abort the in-flight session call
// (pgx conn safety) — the op completes, its rh resolves, and the wait
// spans the sentinel Done (resolution + RunHandle termination).
func TestCancelAndWaitActiveRunOnPendingLaunch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	gate := make(chan struct{})
	inner.stage(recInnerStream{streamGate: gate}) // Stream() call parks
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT pending", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	waitInnerEvent(t, inner, "stream:SELECT pending:start")

	waitDone := make(chan struct{})
	go func() {
		runner.CancelAndWaitActiveRun()
		close(waitDone)
	}()
	// The op is mid-flight and detached from the sentinel ctx: the wait
	// must still be parked.
	select {
	case <-waitDone:
		t.Fatal("CancelAndWaitActiveRun returned while the pending op was still in flight")
	case <-time.After(250 * time.Millisecond):
	}

	close(gate) // op resolves; the rh then needs termination to close Done
	got := waitAck(t, acks, "pending launch")
	if got.err != nil || got.rh == nil {
		t.Fatalf("ack = (%v, %v), want a clean rh (detached op completes)", got.rh, got.err)
	}
	select {
	case <-waitDone:
		t.Fatal("CancelAndWaitActiveRun returned before the resolved RunHandle terminated")
	case <-time.After(250 * time.Millisecond):
	}
	_ = got.rh.Rows().Close() // terminate the run: Done closes, wait ends
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAndWaitActiveRun did not return after the RunHandle terminated")
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
}

// TestNewTxAsyncLaunchSequenceExclusive is the multi-op exclusivity
// guard on a REAL SQLSession: launch A's Begin→Stream sequence is atomic
// with respect to launch B (B's Begin fires strictly after A's op
// resolved), and the mid-op cancelled A surfaces context.Canceled.
func TestNewTxAsyncLaunchSequenceExclusive(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	gate := make(chan struct{})
	inner.stage(recInnerStream{streamGate: gate}) // A's Stream parks (mid-op)
	inner.stage(recInnerStream{})                 // B's Stream
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	a := make(chan ackResult, 1)
	b := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT A", data.RunOptions{NewTx: true}, func(rh *session.RunHandle, err error) {
		a <- ackResult{rh, err}
	})
	waitInnerEvent(t, inner, "stream:SELECT A:start")

	// Second action: cancels A's sentinel mid-op, queues behind it.
	runner.RunAsync(context.Background(), "SELECT B", data.RunOptions{NewTx: true}, func(rh *session.RunHandle, err error) {
		b <- ackResult{rh, err}
	})

	// A's op runs to completion (gate release); the abandoned rh is then
	// closed + suppressed as a cancellation.
	close(gate)
	gotA := waitAck(t, a, "A")
	if gotA.rh != nil || !errors.Is(gotA.err, context.Canceled) {
		t.Fatalf("A ack = (%v, %v), want (nil, context.Canceled)", gotA.rh, gotA.err)
	}
	gotB := waitAck(t, b, "B")
	if gotB.err != nil || gotB.rh == nil {
		t.Fatalf("B ack = (%v, %v), want clean rh", gotB.rh, gotB.err)
	}
	// Terminate B's stream so the watcher + session Close can settle.
	_ = gotB.rh.Rows().Close()

	events := inner.snapshot()
	beginA, endA, beginB := -1, -1, -1
	for i, ev := range events {
		switch ev {
		case "begin":
			if beginA == -1 {
				beginA = i
			} else if beginB == -1 {
				beginB = i
			}
		case "stream:SELECT A:end":
			endA = i
		}
	}
	if beginA == -1 || beginB == -1 || endA == -1 {
		t.Fatalf("missing events (beginA=%d endA=%d beginB=%d): %v", beginA, endA, beginB, events)
	}
	if beginA >= endA || endA >= beginB {
		t.Fatalf("Begin_B interleaved inside A's Begin..resolution window: %v", events)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
	// The still-open wrap tx is rolled back at Close.
	inner.mu.Lock()
	rolledBack := inner.lastTx != nil && inner.lastTx.rolledBack.Load()
	inner.mu.Unlock()
	if !rolledBack {
		t.Fatalf("session Close did not roll back the open wrap tx (events=%v)", inner.snapshot())
	}
}

// TestSyncRunPreemptsPendingAsyncLaunch pins the sync/async seam: a
// synchronous Run (the FK-forward / relationship-panel path on a worker
// goroutine) preempts a pending async launch via its sentinel — the
// mid-op session call is NOT ctx-aborted (pgx conn safety), so the sync
// op waits on the real session's streamMu until the abandoned launch's
// op resolves and its RunHandle is promptly closed.
func TestSyncRunPreemptsPendingAsyncLaunch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	inner := &recInnerSession{}
	gate := make(chan struct{})
	inner.stage(recInnerStream{streamGate: gate}) // async launch parks inside Stream()
	inner.stage(recInnerStream{})                 // sync Run's stream
	sess := session.New(&parkedConn{}, inner, session.Options{})
	runner := data.NewQueryRunnerForSession(sess, drivers.Capabilities{HasLiveCancel: true})

	// hookHits counts preempt-chokepoint firings: #1 on the launcher,
	// strictly before the async op's :start event; #2 on the sync Run's
	// goroutine, AFTER its cancelPendingLaunch has resolved + abandoned
	// the pending sentinel (program order inside preemptInFlight).
	var hookHits atomic.Int32
	runner.SetPreempter(func() bool {
		hookHits.Add(1)
		return false
	})

	acks := make(chan ackResult, 1)
	runner.RunAsync(context.Background(), "SELECT async", data.RunOptions{}, func(rh *session.RunHandle, err error) {
		acks <- ackResult{rh, err}
	})
	waitInnerEvent(t, inner, "stream:SELECT async:start")

	syncDone := make(chan error, 1)
	go func() {
		rh, err := runner.Run(context.Background(), "SELECT sync", data.RunOptions{})
		if err == nil && rh != nil {
			_ = rh.Rows().Close()
		}
		syncDone <- err
	}()
	// Pin the interleaving this test exists to cover: the sync preempt
	// must land while the launch is still PENDING (inside its op). Wait
	// for hook hit #2 — proof the sentinel was already abandoned — before
	// releasing the gate. Without this edge the gate release can beat the
	// preempt: the launch then resolves un-abandoned, and the orphan rh
	// (no tab, inert fake Cancel) never releases streamMu.
	deadline := time.Now().Add(2 * time.Second)
	for hookHits.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("preempt hook fired %d times, want 2 (launcher + sync Run)", hookHits.Load())
		}
		time.Sleep(time.Millisecond)
	}
	close(gate)
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("sync Run err = %v (the pending-launch preempt did not free streamMu)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK: sync Run blocked on streamMu; the pending-launch sentinel was not cancelled")
	}
	got := waitAck(t, acks, "async launch")
	if got.rh != nil || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("async ack = (%v, %v), want (nil, context.Canceled) (suppressed, rh closed)", got.rh, got.err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("session Close err = %v", err)
	}
}
