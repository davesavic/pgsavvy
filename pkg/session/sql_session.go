package session

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// noticeAttacher is the optional capability of a drivers.Session: when the
// concrete driver supports notice plumbing (probed via
// Connection.Capabilities().HasNotice) it implements AttachNotice on the
// pg.Session value. SQLSession type-asserts against this private interface
// at construction so it can stay engine-agnostic. AttachNotice itself is
// owned by pkg/drivers/pg.Session (task dbsavvy-66p.5).
type noticeAttacher interface {
	AttachNotice(ch chan<- pgconn.Notice)
}

// sqlSessionNoticeBuffer is the long-lived buffer between the driver's
// notice router and the SQLSession fan-out goroutine. 128 is comfortably
// larger than the per-run buffer (64) so a burst can land in this channel
// while the fan-out goroutine forwards it to the active run.
const sqlSessionNoticeBuffer = 128

// Options configures a SQLSession at New time. HistoryRecorder may be nil
// (the package installs noopHistoryRecorder); Logger may be nil (a no-op
// discard handler is installed if so).
//
// ConnectionPassword is the active connection-profile password. When non-
// empty it is used to scrub literal substring matches in SQL previews and
// pg notice/error text emitted to the log file (AD-14). Empty is the safe
// default — emits then rely on the redactor hook + RedactConnectionString.
type Options struct {
	HistoryRecorder    HistoryRecorder
	Logger             *slog.Logger
	ConnectionPassword string
}

// SQLSession is the driver-agnostic facade that wraps a drivers.Connection
// + drivers.Session pair and adds:
//
//   - a queue serializer (one Stream / Execute in flight at a time)
//   - notice fan-in via a pool-level NoticeRouter (capability-probed)
//   - per-run RunHandle bookkeeping and a CancelRegistry
//   - HistoryRecorder hand-off (panic-safe)
//
// Lifecycle: callers construct via New(); each Execute/Stream/Begin call
// must observe the at-most-one-in-flight invariant (the queue mutex
// enforces it). Close releases driver resources, joins the notice fan-out
// goroutine, and is idempotent.
type SQLSession struct {
	conn     drivers.Connection
	inner    drivers.Session
	registry *CancelRegistry
	history  HistoryRecorder
	logger   *slog.Logger
	connPwd  string

	streamMu  sync.Mutex
	runActive atomic.Pointer[RunHandle]

	// noticeCh is the long-lived sink fed by the driver's notice router; it
	// is set up at New when the inner session implements noticeAttacher and
	// the driver's Capabilities().HasNotice is true. Nil otherwise.
	noticeCh chan pgconn.Notice
	noticeWG sync.WaitGroup

	closeOnce sync.Once
	closed    atomic.Bool
}

// New constructs a SQLSession over conn + inner. inner MUST be a session
// freshly acquired from conn (the caller's responsibility — the same
// session is used for the lifetime of this SQLSession). When the driver
// advertises HasNotice and inner implements the noticeAttacher capability,
// a long-lived notice channel is attached and a fan-out goroutine is
// started to forward notices to whichever RunHandle is currently active.
func New(conn drivers.Connection, inner drivers.Session, opts Options) *SQLSession {
	s := &SQLSession{
		conn:     conn,
		inner:    inner,
		registry: NewCancelRegistry(),
		history:  opts.HistoryRecorder,
		logger:   opts.Logger,
		connPwd:  opts.ConnectionPassword,
	}
	if s.history == nil {
		s.history = noopHistoryRecorder{}
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}

	if na, ok := inner.(noticeAttacher); ok {
		s.noticeCh = make(chan pgconn.Notice, sqlSessionNoticeBuffer)
		na.AttachNotice(s.noticeCh)
		s.noticeWG.Add(1)
		go s.fanOutNotices()
	}

	return s
}

// SessionID returns the underlying driver session's identifier.
func (s *SQLSession) SessionID() models.SessionID { return s.inner.ID() }

// InTransaction reports whether the underlying session currently has an
// open transaction. Delegates straight to the driver.
func (s *SQLSession) InTransaction() bool { return s.inner.InTransaction() }

// CurrentTransaction returns the in-progress driver Transaction, or nil.
func (s *SQLSession) CurrentTransaction() drivers.Transaction { return s.inner.CurrentTransaction() }

// Execute runs q on the inner session, holding the queue mutex for the
// duration. Fires HistoryRecorder.Record exactly once. The inner driver
// remains the source of truth for RowsAffected / Duration.
func (s *SQLSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()

	sid := s.SessionID()
	preview := sqlPreview(q.SQL, s.connPwd)
	s.logger.LogAttrs(ctx, slog.LevelDebug, "exec_start",
		slog.String("evt", "exec_start"),
		slog.Uint64("sid", uint64(sid)),
		slog.String("sql_preview", preview),
		slog.Int("params_count", len(q.Args)),
		slog.Any("params_hashes", paramsHashes(q.Args)),
	)

	start := time.Now()
	res, err := s.inner.Execute(ctx, q)
	durMs := time.Since(start).Milliseconds()
	rows := int64(0)
	if err == nil {
		rows = res.RowsAffected
	}
	endAttrs := []slog.Attr{
		slog.String("evt", "exec_end"),
		slog.Uint64("sid", uint64(sid)),
		slog.Int64("ms", durMs),
		slog.Int64("rows_affected", rows),
	}
	if err != nil {
		endAttrs = append(endAttrs, slog.String("err", err.Error()))
	}
	s.logger.LogAttrs(ctx, slog.LevelDebug, "exec_end", endAttrs...)

	if !LoggingSuppressed(ctx) {
		s.recordHistory(q.SQL, durMs, rows, err == nil)
	}
	return res, err
}

// Explain delegates to the inner driver session's Explain. The queue mutex
// is held for the duration of the call so an EXPLAIN cannot interleave
// with an in-flight Execute / Stream on the same session.
func (s *SQLSession) Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.inner.Explain(ctx, q, analyze)
}

// Stream issues q on the inner session and returns a RunHandle whose Rows()
// is the wrapped stream. The queue mutex is acquired BEFORE the driver
// call; release is deferred to finish() so the next caller (waiting on
// streamMu via this method) sees the queue free only after the current
// run terminates (clean EOF, caller Close, error, or Cancel).
func (s *SQLSession) Stream(ctx context.Context, q models.Query) (*RunHandle, error) {
	s.streamMu.Lock()

	suppressLog := LoggingSuppressed(ctx)
	sid := s.SessionID()
	preview := sqlPreview(q.SQL, s.connPwd)

	s.logger.LogAttrs(ctx, slog.LevelDebug, "stream_start",
		slog.String("evt", "stream_start"),
		slog.Uint64("sid", uint64(sid)),
		slog.String("sql_preview", preview),
		slog.Int("params_count", len(q.Args)),
		slog.Any("params_hashes", paramsHashes(q.Args)),
	)

	rs, err := s.inner.Stream(ctx, q)
	if err != nil {
		s.streamMu.Unlock()
		s.logger.LogAttrs(ctx, slog.LevelDebug, "stream_end",
			slog.String("evt", "stream_end"),
			slog.Uint64("sid", uint64(sid)),
			slog.Int64("rows_observed", 0),
			slog.Uint64("dropped_notices", 0),
			slog.String("term_err", err.Error()),
			slog.Int64("ms", 0),
		)
		// A Stream that fails before producing a RowStream still counts as
		// a terminated run for the recorder.
		if !suppressLog {
			s.recordHistory(q.SQL, 0, 0, false)
		}
		return nil, err
	}

	rh := newRunHandle(rs, q.SQL)
	start := time.Now()

	// noticeHook emits a structured `evt=notice` line for every notice
	// received on this run; message_preview is pre-scrubbed for the
	// connection-password substring AND DSN-shaped strings (AC).
	connPwd := s.connPwd
	logger := s.logger
	rh.noticeHook = func(n pgconn.Notice) {
		logger.LogAttrs(context.Background(), slog.LevelDebug, "notice",
			slog.String("evt", "notice"),
			slog.Uint64("sid", uint64(sid)),
			slog.String("severity", n.Severity),
			slog.String("code", n.Code),
			slog.String("message_preview", noticePreview(n.Message, connPwd)),
		)
	}

	rh.cancelFn = func() error {
		return s.conn.Cancel(context.Background(), rh.QueryID())
	}
	rh.onFinish = func(termErr error) {
		s.runActive.Store(nil)
		s.registry.Unregister(sid)
		durMs := time.Since(start).Milliseconds()
		termErrStr := ""
		if termErr != nil {
			termErrStr = termErr.Error()
		}
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "stream_end",
			slog.String("evt", "stream_end"),
			slog.Uint64("sid", uint64(sid)),
			slog.Uint64("qid_nonce", rh.QueryID().Nonce),
			slog.Int64("rows_observed", rh.rowsObserved.Load()),
			slog.Uint64("dropped_notices", rh.DroppedNotices()),
			slog.String("term_err", termErrStr),
			slog.Int64("ms", durMs),
		)
		// Clean EOF reports succeeded=true; any other termination is a
		// failure for history purposes (ctx.Canceled, driver errors, ...).
		if !suppressLog {
			s.recordHistory(q.SQL, durMs, rh.rowsObserved.Load(), termErr == nil)
		}
		s.streamMu.Unlock()
	}

	s.runActive.Store(rh)
	s.registry.Register(sid, rh)
	return rh, nil
}

// Begin opens a transaction on the inner session. The queue mutex is held
// only for the driver call itself — the returned Transaction is owned by
// the caller and subsequent Commit/Rollback go through the driver
// directly, not through SQLSession. Tracking the in-flight tx via
// InTransaction() is the driver's job.
func (s *SQLSession) Begin(ctx context.Context, opts models.TxOptions) (drivers.Transaction, error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	return s.inner.Begin(ctx, opts)
}

// Cancel terminates the in-flight run on session sid via the driver's
// Cancel surface. Returns nil when no run is registered (unknown qid) —
// this matches the AC for the cancel-by-qid path and Postgres' own
// "cancel for unknown PID is silently ignored" semantics.
//
// qid is the source of truth: we look up the registered RunHandle by
// SessionID and confirm it matches. A mismatch (stale qid) returns nil.
func (s *SQLSession) Cancel(qid models.QueryID) error {
	rh, ok := s.registry.Lookup(qid.SessionID)
	if !ok {
		return nil
	}
	if rh.QueryID() != qid {
		return nil
	}
	err := rh.Cancel()
	// Cancel dedup (AC): atomic.Bool on RunHandle guarantees exactly one
	// query_cancel emit per qid even under concurrent Cancel.
	if rh.cancelLogged.CompareAndSwap(false, true) {
		attrs := []slog.Attr{
			slog.String("evt", "query_cancel"),
			slog.Uint64("sid", uint64(qid.SessionID)),
			slog.Uint64("qid_nonce", qid.Nonce),
			slog.Uint64("backend_pid", uint64(qid.BackendPID)),
		}
		if err != nil {
			attrs = append(attrs, slog.String("err", err.Error()))
		}
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "query_cancel", attrs...)
	}
	return err
}

// Close releases the inner session, rolls back any active transaction, and
// joins the notice fan-out goroutine. Idempotent.
func (s *SQLSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.closed.Store(true)

		if rh := s.runActive.Load(); rh != nil {
			_ = rh.Cancel()
			select {
			case <-rh.Done():
			case <-time.After(2 * time.Second):
				s.logger.Warn("SQLSession.Close: timed out waiting for run to terminate",
					"sid", s.inner.ID())
			}
		}

		// Roll back an active transaction so a stale tx doesn't poison the
		// pgxpool conn on Release. Ignore rollback errors — Close must
		// always proceed and surface the driver Close error if any.
		if tx := s.inner.CurrentTransaction(); tx != nil {
			if rbErr := tx.Rollback(context.Background()); rbErr != nil {
				s.logger.Warn("session: tx rollback during Close failed",
					"sid", s.inner.ID(), "err", rbErr)
			}
		}

		err = s.inner.Close()

		// Closing the inner session unsubscribes the notice channel at the
		// driver level, so the fan-out goroutine's range will end after
		// the channel is no longer fed. We close noticeCh ourselves to
		// guarantee termination even if the driver leaves the channel
		// dangling on a non-cooperative path.
		if s.noticeCh != nil {
			close(s.noticeCh)
			s.noticeWG.Wait()
		}
	})
	if err == nil {
		return nil
	}
	return err
}

// fanOutNotices forwards every notice received on noticeCh to the
// currently-active RunHandle. Notices delivered while runActive is nil
// (between runs) are dropped on the floor — the SQLSession does not
// retain them.
func (s *SQLSession) fanOutNotices() {
	defer s.noticeWG.Done()
	for n := range s.noticeCh {
		rh := s.runActive.Load()
		if rh == nil {
			continue
		}
		rh.routeNotice(n)
	}
}

// recordHistory invokes the configured HistoryRecorder under a recover so a
// panic in user code cannot corrupt SQLSession state. The recover is
// logged at slog.Warn level so misbehaviour is at least observable.
func (s *SQLSession) recordHistory(stmt string, durMs, rowsAffected int64, succeeded bool) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Warn("session: HistoryRecorder.Record panicked",
				"recover", r, "sid", s.inner.ID())
		}
	}()
	s.logger.LogAttrs(context.Background(), slog.LevelDebug, "history_record",
		slog.String("evt", "history_record"),
		slog.Uint64("sid", uint64(s.inner.ID())),
		slog.String("sql_preview", sqlPreview(stmt, s.connPwd)),
		slog.Int64("ms", durMs),
		slog.Bool("success", succeeded),
	)
	s.history.Record(stmt, durMs, rowsAffected, succeeded)
}
