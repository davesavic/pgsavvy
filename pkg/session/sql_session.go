package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ErrDisconnected is returned by Execute/Stream/Begin when the session
// has been marked connection-dead via SetDisconnected. hq5.6.
var ErrDisconnected = errors.New("session: connection lost")

// ErrPreemptPending is returned by Stream/Execute/Explain/Begin when a prior
// query's worker did not exit within the bounded preempt Stop-wait and is
// still holding streamMu. The fence lifts when that wedged worker finally
// exits (its onFinish releases streamMu and clears the flag). gr7e.2 (AD4).
var ErrPreemptPending = errors.New("session: prior query still terminating")

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

// cancelDialBound caps the wall-clock cost of a single cancel attempt. The
// cancel issues an out-of-band CancelRequest over a fresh connection; on a
// dead or slow host the dial (and the subsequent close-wait read) could
// otherwise block indefinitely, freezing a UI thread that calls Cancel
// synchronously (e.g. PreemptInFlight). 3s is well above a healthy
// round-trip yet short enough to stay responsive. AD5.
const cancelDialBound = 3 * time.Second

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

	// noticeFlush is the unbuffered barrier a finishing run uses to drain any
	// notices still queued in noticeCh before its per-run channel closes. The
	// unbuffered handoff forces finish() to block until the fan-out goroutine
	// is back at its select — i.e. after any in-flight routeNotice completes —
	// so a notice emitted by an instantly-finishing query is delivered, not
	// dropped at EOF. fanoutDone is closed when the fan-out exits so a flush
	// during Close cannot block forever.
	noticeFlush chan flushReq
	fanoutDone  chan struct{}

	closeOnce sync.Once
	closed    atomic.Bool

	// disconnected is set when IsConnectionDead classifies a terminal error.
	// Once true the session rejects new Execute/Stream/Begin attempts and the
	// UI renders the schema rail dimmed. hq5.6.
	disconnected atomic.Bool

	// preemptPending is set when a bounded preempt Stop-wait expired with the
	// prior worker still live (streamMu still held). Once true the session
	// rejects new Execute/Stream/Explain/Begin attempts with ErrPreemptPending
	// until the wedged worker's onFinish releases streamMu and clears it. gr7e.2.
	preemptPending atomic.Bool

	// fkCache is the per-Connection foreign-key cache. Constructed lazily on
	// first FKCache() call so SQLSessions whose callers never need FK metadata
	// pay zero cost. Guarded by fkCacheOnce. See pkg/session/fk_cache.go and
	// task dbsavvy-bwq.13.
	fkCache     *FKCache
	fkCacheOnce sync.Once

	settings *SettingsSnapshot
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
		settings: NewSettingsSnapshot(),
	}
	if s.history == nil {
		s.history = noopHistoryRecorder{}
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	// AD-87v: every event emitted by SQLSession is in the "db" category. Pre-
	// bind cat=db on s.logger so the CategoryFilterHandler in the production
	// handler chain (RedactingHandler → CategoryFilterHandler → TeeHandler)
	// routes session events to the file sink. The deleted slog_bridge.go used
	// to inject this attr globally; SQLSession is the right ownership
	// boundary now.
	s.logger = s.logger.With(slog.String("cat", "db"))

	if na, ok := inner.(noticeAttacher); ok {
		s.noticeCh = make(chan pgconn.Notice, sqlSessionNoticeBuffer)
		s.noticeFlush = make(chan flushReq)
		s.fanoutDone = make(chan struct{})
		na.AttachNotice(s.noticeCh)
		s.noticeWG.Add(1)
		go s.fanOutNotices()
	}

	return s
}

// SessionID returns the underlying driver session's identifier.
func (s *SQLSession) SessionID() models.SessionID { return s.inner.ID() }

// FKCache returns the per-Connection foreign-key cache, constructing it on
// first call. The loader wraps the inner driver session's ListForeignKeys.
// Subsequent calls return the same cache instance for the lifetime of this
// SQLSession; closing the SQLSession drops it. See dbsavvy-bwq.13 / ADR-8.
func (s *SQLSession) FKCache() *FKCache {
	s.fkCacheOnce.Do(func() {
		s.fkCache = NewFKCache(func(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
			return s.inner.ListForeignKeys(ctx, schema, table)
		})
		s.fkCache.SetReverseLoader(func(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
			return s.inner.ListInboundForeignKeys(ctx, schema, table)
		})
	})
	return s.fkCache
}

// InTransaction reports whether the underlying session currently has an
// open transaction. Delegates straight to the driver.
func (s *SQLSession) InTransaction() bool { return s.inner.InTransaction() }

// CurrentTransaction returns the in-progress driver Transaction, or nil.
func (s *SQLSession) CurrentTransaction() drivers.Transaction { return s.inner.CurrentTransaction() }

// SettingsSnapshot returns the session's mutable settings map.
func (s *SQLSession) SettingsSnapshot() *SettingsSnapshot { return s.settings }

// SetDisconnected marks the session as connection-dead. Once set, new
// Execute/Stream/Begin calls return ErrDisconnected. Idempotent. hq5.6.
func (s *SQLSession) SetDisconnected(v bool) { s.disconnected.Store(v) }

// MarkPreemptPending fences the session after a bounded preempt Stop-wait
// expired with the prior worker still live. New Execute/Stream/Explain/Begin
// calls return ErrPreemptPending until the wedged worker's onFinish releases
// streamMu and clears the flag. Called by the QueryRunner on bound-expiry.
// gr7e.2.
func (s *SQLSession) MarkPreemptPending() { s.preemptPending.Store(true) }

// IsDisconnected reports whether the session has been marked
// connection-dead via SetDisconnected. hq5.6.
func (s *SQLSession) IsDisconnected() bool { return s.disconnected.Load() }

// TxStatementCount returns the number of statements executed in the current
// transaction, or 0 when no transaction is active.
func (s *SQLSession) TxStatementCount() int {
	if tx := s.inner.CurrentTransaction(); tx != nil {
		return tx.StatementCount()
	}
	return 0
}

// SavepointNames returns the savepoint stack of the current transaction,
// or nil when no transaction is active.
func (s *SQLSession) SavepointNames() []string {
	if tx := s.inner.CurrentTransaction(); tx != nil {
		return tx.Savepoints()
	}
	return nil
}

// Execute runs q on the inner session, holding the queue mutex for the
// duration. Fires HistoryRecorder.Record exactly once. The inner driver
// remains the source of truth for RowsAffected / Duration.
func (s *SQLSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	if s.disconnected.Load() {
		return models.Result{}, ErrDisconnected
	}
	if s.preemptPending.Load() {
		return models.Result{}, ErrPreemptPending
	}
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
	// hq5.6: mark session disconnected on transport-level errors.
	if err != nil && drivers.IsConnectionDead(err) {
		s.disconnected.Store(true)
	}
	return res, err
}

// Explain delegates to the inner driver session's Explain. The queue mutex
// is held for the duration of the call so an EXPLAIN cannot interleave
// with an in-flight Execute / Stream on the same session.
func (s *SQLSession) Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error) {
	if s.disconnected.Load() {
		return models.Plan{}, ErrDisconnected
	}
	if s.preemptPending.Load() {
		return models.Plan{}, ErrPreemptPending
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	plan, err := s.inner.Explain(ctx, q, analyze)
	if err != nil && drivers.IsConnectionDead(err) {
		s.disconnected.Store(true)
	}
	return plan, err
}

// Stream issues q on the inner session and returns a RunHandle whose Rows()
// is the wrapped stream. The queue mutex is acquired BEFORE the driver
// call; release is deferred to finish() so the next caller (waiting on
// streamMu via this method) sees the queue free only after the current
// run terminates (clean EOF, caller Close, error, or Cancel).
func (s *SQLSession) Stream(ctx context.Context, q models.Query) (*RunHandle, error) {
	if s.disconnected.Load() {
		return nil, ErrDisconnected
	}
	if s.preemptPending.Load() {
		return nil, ErrPreemptPending
	}
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
		if drivers.IsConnectionDead(err) {
			s.disconnected.Store(true)
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
		ctx, cancel := context.WithTimeout(context.Background(), cancelDialBound)
		defer cancel()
		return s.conn.Cancel(ctx, rh.QueryID())
	}
	rh.flush = func() { s.flushNotices(rh) }
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
		if termErr != nil {
			if tx := s.inner.CurrentTransaction(); tx != nil {
				tx.ObserveError(termErr)
			}
			// hq5.6: mark session disconnected on transport-level errors
			// so subsequent Execute/Stream/Begin calls fail fast.
			if drivers.IsConnectionDead(termErr) {
				s.disconnected.Store(true)
			}
		}
		// The wedged worker has now exited and is releasing streamMu; lift the
		// preempt fence at the same instant so a fenced session reopens for new
		// queries exactly when the queue is free again. gr7e.2.
		s.preemptPending.Store(false)
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
	if s.disconnected.Load() {
		return nil, ErrDisconnected
	}
	if s.preemptPending.Load() {
		return nil, ErrPreemptPending
	}
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

// flushReq is the barrier message a finishing run sends to the fan-out
// goroutine: route every notice still buffered in noticeCh to rh, then close
// ack. See SQLSession.noticeFlush.
type flushReq struct {
	rh  *RunHandle
	ack chan struct{}
}

// fanOutNotices forwards every notice received on noticeCh to the
// currently-active RunHandle. Notices delivered while runActive is nil
// (between runs) are dropped on the floor — the SQLSession does not
// retain them. A flushReq drains the remaining buffer into the finishing
// run before its per-run channel closes.
func (s *SQLSession) fanOutNotices() {
	defer close(s.fanoutDone)
	defer s.noticeWG.Done()
	for {
		select {
		case n, ok := <-s.noticeCh:
			if !ok {
				return
			}
			if rh := s.runActive.Load(); rh != nil {
				rh.routeNotice(n)
			}
		case req := <-s.noticeFlush:
			closed := s.drainNoticesInto(req.rh)
			close(req.ack)
			if closed {
				return
			}
		}
	}
}

// drainNoticesInto routes every currently-buffered notice to rh without
// blocking. Returns true if noticeCh was closed mid-drain (the fan-out loop
// should then exit).
func (s *SQLSession) drainNoticesInto(rh *RunHandle) bool {
	for {
		select {
		case n, ok := <-s.noticeCh:
			if !ok {
				return true
			}
			rh.routeNotice(n)
		default:
			return false
		}
	}
}

// flushNotices runs the barrier from a finishing run: it blocks until the
// fan-out goroutine has drained noticeCh into rh (so a notice emitted by an
// instantly-finishing query is routed before rh's channel closes), or returns
// immediately if there is no fan-out or it has already exited.
func (s *SQLSession) flushNotices(rh *RunHandle) {
	if s.noticeFlush == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case s.noticeFlush <- flushReq{rh: rh, ack: ack}:
		<-ack
	case <-s.fanoutDone:
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
