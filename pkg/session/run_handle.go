package session

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// runNoticeBuffer is the per-run NOTICE channel buffer. 64 is sufficient for
// typical DO-block / RAISE NOTICE bursts; overflow drops the oldest pending
// notice (see RunHandle.routeNotice) and increments droppedNotices.
const runNoticeBuffer = 64

// RunHandle is the per-Stream lifecycle handle returned by
// SQLSession.Stream. It owns:
//
//   - the wrapped drivers.RowStream (exposed via Rows())
//   - a buffered notice channel populated by the SQLSession fan-out
//   - a Done channel closed exactly once when the stream terminates
//   - the cancel/finish bookkeeping that releases the queue serializer
//
// Done is closed by finish(), which runs at most once thanks to the
// embedded sync.Once. finish is called from one of three sites: the wrapped
// RowStream's Close, an Ok=false (clean EOF) terminal Next, or the
// Cancel path — whichever happens first wins.
type RunHandle struct {
	queryID models.QueryID
	stmt    string

	rows *wrappedRowStream

	done           chan struct{}
	notices        chan pgconn.Notice
	droppedNotices atomic.Uint64

	// onFinish is invoked from finish() exactly once. SQLSession installs a
	// closure that records history, clears runActive, releases the queue
	// serializer, and unregisters from the CancelRegistry. err is the
	// terminal stream error (nil on clean EOF or successful close).
	onFinish func(err error)
	cancelFn func() error

	once         sync.Once
	rowsObserved atomic.Int64

	// cancelLogged guards the SQLSession.Cancel emit so concurrent Cancel
	// calls for the same qid produce exactly one `evt=query_cancel` log
	// line (AC). Set via CompareAndSwap by SQLSession.Cancel.
	cancelLogged atomic.Bool

	// noticeHook is installed by SQLSession.Stream so routeNotice can emit
	// a structured `evt=notice` log line for every received notice. nil
	// when no hook is set (notice still routed to the channel).
	noticeHook func(pgconn.Notice)
}

// newRunHandle constructs a RunHandle wrapping rs. The caller is responsible
// for installing onFinish + cancelFn before exposing the handle (SQLSession
// does this within Stream).
func newRunHandle(rs drivers.RowStream, stmt string) *RunHandle {
	rh := &RunHandle{
		queryID: rs.QueryID(),
		stmt:    stmt,
		done:    make(chan struct{}),
		notices: make(chan pgconn.Notice, runNoticeBuffer),
	}
	rh.rows = &wrappedRowStream{inner: rs, owner: rh}
	return rh
}

// QueryID returns the driver-stamped identifier of the in-flight query.
// Stable for the life of the handle; safe to call before the first Next().
func (r *RunHandle) QueryID() models.QueryID { return r.queryID }

// Rows returns the wrapped drivers.RowStream. Each Next() / Close() goes
// through wrappedRowStream so the handle can observe terminal events and
// fire finish() exactly once.
func (r *RunHandle) Rows() drivers.RowStream { return r.rows }

// Done returns a channel that is closed when the run terminates (clean EOF,
// caller Close, error, or Cancel). The returned channel is receive-only.
// Close is the only signal — there is no value sent.
func (r *RunHandle) Done() <-chan struct{} { return r.done }

// Notices returns the receive end of this run's notice channel. The channel
// is closed by finish() after the run terminates, so a `for n := range
// rh.Notices()` loop drains naturally to completion.
func (r *RunHandle) Notices() <-chan pgconn.Notice { return r.notices }

// DroppedNotices reports how many incoming notices were discarded because
// the per-run channel buffer was full at delivery time. Diagnostic surface
// only — UI / command-log writers read this after Done closes.
func (r *RunHandle) DroppedNotices() uint64 { return r.droppedNotices.Load() }

// Cancel asks the driver to terminate the in-flight query and returns the
// driver's error (typically nil — Postgres cancel-request is best-effort).
// Idempotent: a second Cancel after Done is a no-op returning nil. The
// run's Done channel closes once the wrapped stream surfaces the terminal
// state (driver-side); Cancel itself does NOT close Done synchronously.
func (r *RunHandle) Cancel() error {
	select {
	case <-r.done:
		return nil
	default:
	}
	if r.cancelFn == nil {
		return nil
	}
	return r.cancelFn()
}

// finish runs the terminal hook exactly once. err is the stream error
// observed at termination, or nil for clean EOF / successful close.
func (r *RunHandle) finish(err error) {
	r.once.Do(func() {
		if r.onFinish != nil {
			r.onFinish(err)
		}
		close(r.notices)
		close(r.done)
	})
}

// routeNotice is invoked by the SQLSession fan-out goroutine for every
// notice received while this RunHandle is the active run. Non-blocking
// send with drop-oldest semantics: when the channel is full we evict one
// pending notice (the oldest in the buffer) and increment droppedNotices.
func (r *RunHandle) routeNotice(n pgconn.Notice) {
	if r.noticeHook != nil {
		r.noticeHook(n)
	}
	select {
	case r.notices <- n:
	default:
		// Drop-oldest to keep the buffer fresh under sustained bursts.
		select {
		case <-r.notices:
		default:
		}
		select {
		case r.notices <- n:
		default:
			r.droppedNotices.Add(1)
			return
		}
		r.droppedNotices.Add(1)
	}
}

// wrappedRowStream is the drivers.RowStream returned by RunHandle.Rows. It
// observes terminal Next() outcomes (ok=false with or without error) and
// Close() invocations and fires owner.finish exactly once.
type wrappedRowStream struct {
	inner  drivers.RowStream
	owner  *RunHandle
	closed atomic.Bool
}

func (w *wrappedRowStream) Columns() []models.ColumnMeta { return w.inner.Columns() }
func (w *wrappedRowStream) QueryID() models.QueryID      { return w.inner.QueryID() }
func (w *wrappedRowStream) RowsAffected() int64          { return w.inner.RowsAffected() }

func (w *wrappedRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	row, ok, err := w.inner.Next(ctx)
	if ok {
		w.owner.rowsObserved.Add(1)
		return row, ok, err
	}
	// ok=false: either clean EOF (err==nil) or terminal error. Fire finish
	// so Done closes promptly without waiting for an explicit Close call.
	w.owner.finish(err)
	return row, ok, err
}

func (w *wrappedRowStream) Close() error {
	if !w.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := w.inner.Close()
	w.owner.finish(err)
	return err
}

var _ drivers.RowStream = (*wrappedRowStream)(nil)
