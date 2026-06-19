package session_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakeConn implements drivers.Connection with the minimum surface the
// SQLSession exercises: Cancel + AcquireSession (returning a stub error
// when called — SQLSession never re-acquires) + Close stubs.
type fakeConn struct {
	cancelCount       atomic.Int32
	cancelErr         error
	lastCancel        atomic.Pointer[models.QueryID]
	cancelHadDeadline atomic.Bool
	cancelDeadline    atomic.Pointer[time.Time]
}

func (c *fakeConn) Close() error               { return nil }
func (c *fakeConn) Ping(context.Context) error { return nil }
func (c *fakeConn) ServerVersion() string      { return "fake" }
func (c *fakeConn) AcquireSession(context.Context) (drivers.Session, error) {
	return nil, errors.New("fakeConn: AcquireSession not used by these tests")
}

func (c *fakeConn) Cancel(ctx context.Context, qid models.QueryID) error {
	c.cancelCount.Add(1)
	cp := qid
	c.lastCancel.Store(&cp)
	if dl, ok := ctx.Deadline(); ok {
		c.cancelHadDeadline.Store(true)
		d := dl
		c.cancelDeadline.Store(&d)
	}
	return c.cancelErr
}

// fakeRowStream is a deterministic drivers.RowStream that yields `total`
// rows, then EOF. The optional blockOn channel suspends Next() so a test
// can drive Cancel mid-stream.
type fakeRowStream struct {
	qid        models.QueryID
	cols       []models.ColumnMeta
	idx        int
	total      int
	blockOn    chan struct{}
	closed     atomic.Bool
	closeCount atomic.Int32
}

func (s *fakeRowStream) Columns() []models.ColumnMeta { return s.cols }
func (s *fakeRowStream) QueryID() models.QueryID      { return s.qid }
func (s *fakeRowStream) RowsAffected() int64          { return 0 }
func (s *fakeRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	if s.closed.Load() {
		return models.Row{}, false, errors.New("fakeRowStream: closed")
	}
	if s.blockOn != nil {
		select {
		case <-s.blockOn:
		case <-ctx.Done():
			return models.Row{}, false, ctx.Err()
		}
	}
	if s.idx >= s.total {
		return models.Row{}, false, nil
	}
	row := models.Row{Values: []any{s.idx}}
	s.idx++
	return row, true, nil
}

func (s *fakeRowStream) Close() error {
	s.closed.Store(true)
	s.closeCount.Add(1)
	return nil
}

// fakeSess implements drivers.Session. Only the methods SQLSession touches
// have real behaviour; the rest return zero values so the interface is
// satisfied.
type fakeSess struct {
	id         models.SessionID
	streamMu   sync.Mutex
	streams    []func() drivers.RowStream
	executeRes models.Result
	executeErr error
	beginTx    drivers.Transaction
	beginErr   error
	inTx       atomic.Bool
	currentTx  drivers.Transaction
	closeCount atomic.Int32

	noticeCh chan<- pgconn.Notice
}

// AttachNotice satisfies session.noticeAttacher.
func (s *fakeSess) AttachNotice(ch chan<- pgconn.Notice) { s.noticeCh = ch }

func (s *fakeSess) Close() error                                                 { s.closeCount.Add(1); return nil }
func (s *fakeSess) ID() models.SessionID                                         { return s.id }
func (s *fakeSess) ListDatabases(context.Context) ([]models.Database, error)     { return nil, nil }
func (s *fakeSess) ListSchemas(context.Context, string) ([]models.Schema, error) { return nil, nil }
func (s *fakeSess) ListTables(context.Context, string) ([]*models.Table, error)  { return nil, nil }
func (s *fakeSess) ListColumns(context.Context, string, string) ([]models.Column, error) {
	return nil, nil
}

func (s *fakeSess) ListIndexes(context.Context, string, string) ([]models.Index, error) {
	return nil, nil
}

func (s *fakeSess) ListConstraints(context.Context, string, string) ([]models.Constraint, error) {
	return nil, nil
}

func (s *fakeSess) ListForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *fakeSess) ListInboundForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	return nil, nil
}

func (s *fakeSess) TableStats(context.Context, string, string) (int64, int64, error) {
	return 0, 0, nil
}

func (s *fakeSess) ListFunctions(context.Context) ([]string, error) {
	return nil, nil
}

func (s *fakeSess) DescribeFunction(context.Context, string, string) ([]models.FunctionDetail, error) {
	return nil, nil
}

func (s *fakeSess) Execute(context.Context, models.Query) (models.Result, error) {
	return s.executeRes, s.executeErr
}

// Stream pops the next pre-staged stream from streams. Tests stage one
// fakeRowStream per Stream call.
func (s *fakeSess) Stream(_ context.Context, _ models.Query) (drivers.RowStream, error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if len(s.streams) == 0 {
		return nil, errors.New("fakeSess: no staged streams")
	}
	next := s.streams[0]
	s.streams = s.streams[1:]
	return next(), nil
}

func (s *fakeSess) Explain(context.Context, models.Query, bool) (models.Plan, error) {
	return models.Plan{}, nil
}

func (s *fakeSess) Begin(context.Context, models.TxOptions) (drivers.Transaction, error) {
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	s.inTx.Store(true)
	s.currentTx = s.beginTx
	return s.beginTx, nil
}
func (s *fakeSess) InTransaction() bool                     { return s.inTx.Load() }
func (s *fakeSess) CurrentTransaction() drivers.Transaction { return s.currentTx }
func (s *fakeSess) Encoder() drivers.Encoder                { return nopEncoder{} }

// nopEncoder is a no-op drivers.Encoder used by the SQLSession fake session.
// It returns "NULL" for any input — these tests do not exercise literal
// encoding.
type nopEncoder struct{}

func (nopEncoder) EncodeLiteral(_ any, _ uint32) string { return "NULL" }

// fakeTx is a minimal drivers.Transaction whose Rollback flips inTx.
type fakeTx struct {
	parent       *fakeSess
	commitErr    error
	rollback     atomic.Int32
	status       models.TxStatus
	observeErrs  []error
	observeErrMu sync.Mutex
}

func (t *fakeTx) Commit(context.Context) error {
	if t.commitErr != nil {
		return t.commitErr
	}
	t.status = models.TxCommitted
	if t.parent != nil {
		t.parent.inTx.Store(false)
		t.parent.currentTx = nil
	}
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	t.rollback.Add(1)
	t.status = models.TxRolledBack
	if t.parent != nil {
		t.parent.inTx.Store(false)
		t.parent.currentTx = nil
	}
	return nil
}
func (t *fakeTx) Savepoint(context.Context, string) error  { return nil }
func (t *fakeTx) Release(context.Context, string) error    { return nil }
func (t *fakeTx) RollbackTo(context.Context, string) error { return nil }
func (t *fakeTx) Savepoints() []string                     { return nil }
func (t *fakeTx) Status() models.TxStatus                  { return t.status }
func (t *fakeTx) ObserveError(err error) {
	t.observeErrMu.Lock()
	t.observeErrs = append(t.observeErrs, err)
	t.observeErrMu.Unlock()
}
func (t *fakeTx) StatementCount() int { return 0 }

// recordingHistory captures every HistoryRecorder.Record call.
type recordingHistory struct {
	mu      sync.Mutex
	calls   []historyCall
	panicOn int // 0 = never; otherwise panic when this call index lands
	count   atomic.Int32
}
type historyCall struct {
	Stmt         string
	DurMs        int64
	RowsAffected int64
	Succeeded    bool
}

func (h *recordingHistory) Record(stmt string, durMs, rows int64, ok bool) {
	idx := int(h.count.Add(1))
	if h.panicOn != 0 && idx == h.panicOn {
		panic("recordingHistory: synthetic panic")
	}
	h.mu.Lock()
	h.calls = append(h.calls, historyCall{Stmt: stmt, DurMs: durMs, RowsAffected: rows, Succeeded: ok})
	h.mu.Unlock()
}

func (h *recordingHistory) snapshot() []historyCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]historyCall, len(h.calls))
	copy(out, h.calls)
	return out
}

// errorTermRowStream is a RowStream that returns termErr on the first Next
// call, simulating a stream that terminates with an error.
type errorTermRowStream struct {
	qid     models.QueryID
	termErr error
	closed  atomic.Bool
}

func (s *errorTermRowStream) Columns() []models.ColumnMeta { return nil }
func (s *errorTermRowStream) QueryID() models.QueryID      { return s.qid }
func (s *errorTermRowStream) RowsAffected() int64          { return 0 }
func (s *errorTermRowStream) Close() error                 { s.closed.Store(true); return nil }
func (s *errorTermRowStream) Next(context.Context) (models.Row, bool, error) {
	return models.Row{}, false, s.termErr
}

// Compile-time guards.
var (
	_ drivers.Connection  = (*fakeConn)(nil)
	_ drivers.Session     = (*fakeSess)(nil)
	_ drivers.RowStream   = (*fakeRowStream)(nil)
	_ drivers.RowStream   = (*errorTermRowStream)(nil)
	_ drivers.Transaction = (*fakeTx)(nil)
)
