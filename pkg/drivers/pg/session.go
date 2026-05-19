package pg

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// queryNonceCounter is the process-global monotonic source for QueryID.Nonce.
// Stamped on every Execute/Stream so that two queries on the same Session at
// the same instant remain distinguishable. See epic dbsavvy-66p §D5.
var queryNonceCounter atomic.Uint64

// sessionIDCounter is the process-global monotonic source for Session.ID().
// Incremented atomically at construction. See epic dbsavvy-921 D11.
var sessionIDCounter atomic.Uint64

// Session is a stateful checkout of a *Connection. It wraps a single
// pgxpool.Conn for the duration of its lifetime; Close releases the pooled
// connection. Session methods are NOT safe for concurrent use by multiple
// goroutines — callers must serialize. The inFlight guard (D18) panics on
// detected re-entry rather than corrupting protocol state silently.
//
// ListTables is the only public entry point for relation listing; TableLoader
// is package-private machinery exposed to enrichment workers. See Arch-5 of
// the review-plan resolutions for epic dbsavvy-921.
type Session struct {
	conn       *pgxpool.Conn
	id         models.SessionID
	backendPID uint32         // D19 — sized to match pgconn.PgConn.PID()
	secretKey  uint32         // 66p.4 cancel-request authentication; captured from pgconn at construction
	pgConn     *pgconn.PgConn // 66p.5 captured at newSession so Close can unbind from NoticeRouter
	parent     *Connection
	closed     atomic.Bool
	inFlight   atomic.Int32
}

// newSession constructs a *Session bound to pgxConn and parent. Session.ID is
// assigned from sessionIDCounter atomically; backendPID and secretKey are
// captured from the underlying pgconn — both are required by the cancel-request
// wire protocol (epic dbsavvy-66p.4) and remain stable for the life of the
// pgconn. The session is registered with parent.registerCancel so that
// Connection.Cancel can look it up by BackendPID.
func newSession(pgxConn *pgxpool.Conn, parent *Connection) *Session {
	pgc := pgxConn.Conn().PgConn()
	pid := pgc.PID()
	secret := pgc.SecretKey()
	s := &Session{
		conn:       pgxConn,
		id:         models.SessionID(sessionIDCounter.Add(1)),
		backendPID: pid,
		secretKey:  secret,
		pgConn:     pgc,
		parent:     parent,
	}
	parent.registerCancel(pid, secret)
	if parent.notices != nil {
		parent.notices.bindConn(pgc, s.id)
	}
	return s
}

// SecretKey returns the PostgreSQL cancel-request secret key captured from the
// underlying pgconn at session-open time. The value is required to authenticate
// a cancel-request packet for this backend (see epic dbsavvy-66p.4). It is
// non-zero for any session opened against a real Postgres server.
func (s *Session) SecretKey() uint32 { return s.secretKey }

// BackendPID returns the PostgreSQL backend PID captured at session-open. It
// matches the BackendPID stamped into every QueryID produced by Stream/Execute.
func (s *Session) BackendPID() uint32 { return s.backendPID }

// Conn exposes the underlying pgxpool.Conn to same-package loaders that have
// ALREADY acquired the inFlight guard via their calling Session method. It is
// NOT guarded itself — calling it from outside the package is a programmer
// error. See TableLoader for the intended usage pattern.
func (s *Session) Conn() *pgxpool.Conn { return s.conn }

// ID returns the monotonic per-process session identifier.
func (s *Session) ID() models.SessionID { return s.id }

// acquireInFlight panics on use-after-Close or concurrent use. On success the
// inFlight flag is set; callers MUST eventually invoke releaseInFlight (either
// directly, as Stream does for the lifetime of pgRowStream, or via the guard()
// defer wrapper for synchronous list-methods).
func (s *Session) acquireInFlight() {
	if s.closed.Load() {
		panic("session: use after Close")
	}
	if !s.inFlight.CompareAndSwap(0, 1) {
		panic("session: concurrent use")
	}
}

// releaseInFlight clears the inFlight flag. Safe to call repeatedly — Store(0)
// on an already-zero value is a no-op. Streams call this exactly once from
// pgRowStream.Close (which is itself idempotent), so the at-most-once contract
// is enforced at the caller level.
func (s *Session) releaseInFlight() { s.inFlight.Store(0) }

// guard panics on use-after-Close or concurrent use. Returns a release
// function that must be deferred. Every synchronous public Session method that
// touches s.conn MUST start with: defer s.guard()(). Long-lived holders
// (Stream) call acquireInFlight / releaseInFlight directly instead.
func (s *Session) guard() func() {
	s.acquireInFlight()
	return s.releaseInFlight
}

// Close releases the pooled connection. Idempotent: second and subsequent
// calls return nil without re-releasing. Close intentionally does NOT take
// the inFlight guard — it is the terminator and must always proceed. The
// closed flag is set BEFORE Release so any concurrent late-callers see the
// "use after Close" panic rather than a use-after-free on pgxpool.
func (s *Session) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.parent.unregisterCancel(s.backendPID)
	if s.parent.notices != nil {
		// Order matters: unbind the pgconn → sid mapping FIRST so any
		// notice currently in flight can no longer find a subscriber
		// after this point; then Unsubscribe so the subscriber map no
		// longer references the (possibly soon-closed) caller channel.
		s.parent.notices.unbindConn(s.pgConn)
		s.parent.notices.Unsubscribe(s.id)
	}
	s.conn.Release()
	s.parent.sessions.Add(-1)
	return nil
}

// AttachNotice registers ch as the destination for NOTICE / WARNING / INFO
// messages received on this Session's underlying connection. The channel is
// sent values (not pointers) — pgx delivers *pgconn.Notice and the router
// dereferences exactly once per delivered notice. Sends are non-blocking:
// when ch is full, the notice is dropped and DroppedNotices increments.
//
// AttachNotice may be called at any time after AcquireSession; it is
// automatically Unsubscribe'd by Session.Close. A second AttachNotice
// overwrites the prior channel. The caller owns ch and is responsible for
// closing it (if at all) AFTER Session.Close returns — Close does not close
// the channel.
func (s *Session) AttachNotice(ch chan<- pgconn.Notice) {
	if s.parent.notices == nil {
		return
	}
	s.parent.notices.Subscribe(s.id, ch)
}

// DroppedNotices reports the count of notices that arrived while the
// subscriber channel was full and were therefore discarded. Useful as a
// diagnostic in the messages writer (epic dbsavvy-66p.13). Zero when no
// notices have been dropped (including when AttachNotice was never called).
func (s *Session) DroppedNotices() uint64 {
	if s.parent.notices == nil {
		return 0
	}
	return s.parent.notices.droppedFor(s.id)
}

// ListDatabases runs the embedded list_databases.sql against the underlying
// connection and returns a flat []models.Database slice.
func (s *Session) ListDatabases(ctx context.Context) ([]models.Database, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	rows, err := s.conn.Query(ctx, sqlListDatabases)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	var out []models.Database
	for rows.Next() {
		var d models.Database
		if err := rows.Scan(&d.Name, &d.Owner, &d.Encoding); err != nil {
			return nil, wrapPgError(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}

// ListSchemas returns every namespace visible to the current connection. The
// db argument is documented-ignored in v1: a single pgx pool is bound to one
// database, so passing a different name here cannot cross-database query.
func (s *Session) ListSchemas(ctx context.Context, _ string) ([]models.Schema, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	rows, err := s.conn.Query(ctx, sqlListSchemas)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	var out []models.Schema
	for rows.Next() {
		var sch models.Schema
		if err := rows.Scan(&sch.Name, &sch.Owner); err != nil {
			return nil, wrapPgError(err)
		}
		out = append(out, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}

// ListTables is the only public entry to relation listing on a Session. It
// delegates to a freshly-constructed TableLoader and runs the synchronous
// fast-path (no asynchronous stats enrichment); the onWorker/renderFunc
// callbacks are wired in epic E5 when the UI layer can dispatch background
// work. See Arch-5.
func (s *Session) ListTables(ctx context.Context, schema string) ([]*models.Table, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	loader := newTableLoader(s)
	return loader.Load(ctx, schema, nil, func(_ func() error) {}, func() {})
}

// ListColumns returns the columns of (schema, table). IsPrimaryKey is
// populated by intersecting the column names with the column lists of any
// IsPrimary index on the same relation (avoids a second round-trip catalog
// join).
func (s *Session) ListColumns(ctx context.Context, schema, table string) ([]models.Column, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	cols, err := s.listColumnsNoGuard(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	indexes, err := s.listIndexesNoGuard(ctx, schema, table)
	if err != nil {
		return nil, err
	}
	pkNames := map[string]struct{}{}
	for _, ix := range indexes {
		if !ix.IsPrimary {
			continue
		}
		for _, c := range ix.Columns {
			pkNames[c] = struct{}{}
		}
	}
	if len(pkNames) > 0 {
		for i := range cols {
			if _, ok := pkNames[cols[i].Name]; ok {
				cols[i].IsPrimaryKey = true
			}
		}
	}
	return cols, nil
}

// listColumnsNoGuard is the inner column-listing helper. Callers MUST already
// hold the Session inFlight guard.
func (s *Session) listColumnsNoGuard(ctx context.Context, schema, table string) ([]models.Column, error) {
	rows, err := s.conn.Query(ctx, sqlListColumns, schema, table)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	var out []models.Column
	for rows.Next() {
		var c models.Column
		var def *string
		var desc *string
		if err := rows.Scan(&c.Name, &c.DataType, &def, &c.Nullable, &c.Position, &desc); err != nil {
			return nil, wrapPgError(err)
		}
		if def != nil {
			c.Default = *def
		}
		if desc != nil {
			c.Description = *desc
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}

// ListIndexes returns the indexes defined on (schema, table).
func (s *Session) ListIndexes(ctx context.Context, schema, table string) ([]models.Index, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	return s.listIndexesNoGuard(ctx, schema, table)
}

// listIndexesNoGuard is the inner index-listing helper. Callers MUST already
// hold the Session inFlight guard (used both by ListIndexes and by
// ListColumns to avoid re-entrant guard violations).
func (s *Session) listIndexesNoGuard(ctx context.Context, schema, table string) ([]models.Index, error) {
	rows, err := s.conn.Query(ctx, sqlListIndexes, schema, table)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	var out []models.Index
	for rows.Next() {
		var ix models.Index
		if err := rows.Scan(&ix.Name, &ix.Schema, &ix.Table, &ix.Columns, &ix.IsUnique, &ix.IsPrimary, &ix.Method); err != nil {
			return nil, wrapPgError(err)
		}
		out = append(out, ix)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}

// ListConstraints returns every check, unique, primary-key, foreign-key and
// not-null constraint on (schema, table).
func (s *Session) ListConstraints(ctx context.Context, schema, table string) ([]models.Constraint, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	rows, err := s.conn.Query(ctx, sqlListConstraints, schema, table)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	var out []models.Constraint
	for rows.Next() {
		var c models.Constraint
		if err := rows.Scan(&c.Name, &c.Schema, &c.Table, &c.Kind, &c.Columns, &c.Definition); err != nil {
			return nil, wrapPgError(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}

// DescribeFunction returns drivers.ErrNotImplemented in v1; the pg_proc
// introspection lands in a later epic.
func (s *Session) DescribeFunction(_ context.Context, _, _ string) (models.FunctionDetail, error) {
	defer s.guard()()
	return models.FunctionDetail{}, drivers.ErrNotImplemented
}

// Execute runs q.SQL with q.Args and materializes the entire result set into
// a models.Result. Columns is populated from pgx FieldDescriptions; Rows is a
// row-major copy of pgx.Rows.Values(); RowsAffected is taken from the command
// tag; Duration spans the wall-clock from query dispatch to materialization.
// A *pgconn.PgError is mapped to *drivers.QueryError via wrapPgError. The
// inFlight guard is held for the entire call. Cancel/NOTICE/EXPLAIN are out
// of scope (see epic dbsavvy-66p §D5; tasks 66p.4–66p.6).
func (s *Session) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	defer s.guard()()

	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	start := time.Now()
	rows, err := s.conn.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		return models.Result{}, wrapPgError(err)
	}
	defer rows.Close()

	cols := fieldDescriptionsToColumns(rows.FieldDescriptions())

	var out []*models.Row
	for rows.Next() {
		vals, vErr := rows.Values()
		if vErr != nil {
			return models.Result{}, wrapPgError(vErr)
		}
		out = append(out, &models.Row{Values: vals})
	}
	if err := rows.Err(); err != nil {
		return models.Result{}, wrapPgError(err)
	}

	return models.Result{
		Columns:      cols,
		Rows:         out,
		RowsAffected: rows.CommandTag().RowsAffected(),
		Duration:     time.Since(start),
	}, nil
}

// Stream issues q and returns a *pgRowStream that lazily walks the result set.
// The Session inFlight guard is acquired by Stream and released by the
// returned stream on whichever of (a) explicit Close, (b) a Next call that
// observes clean EOF, or (c) a Next call that observes a terminal pgx error
// happens first. Calling Session.Stream (or any other Session method) again
// before one of those release events fires panics with "session: concurrent
// use". Caller-side serialization of multiple streams on a single Session is
// the responsibility of the calling layer (see pkg/session.SQLSession, task
// 66p.7). The QueryID returned by the stream is fully populated (SessionID,
// BackendPID, Started, Nonce all non-zero) BEFORE the first Next() call
// returns; QueryID() may safely be read up front.
func (s *Session) Stream(ctx context.Context, q models.Query) (drivers.RowStream, error) {
	s.acquireInFlight()

	// q.Timeout is intentionally NOT applied here in v1: a derived ctx
	// would require a cancel func captured by the stream so Close can
	// release it; that plumbing belongs with task 66p.4 (Cancel).
	started := time.Now()
	rows, err := s.conn.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		s.releaseInFlight()
		return nil, wrapPgError(err)
	}

	qid := models.QueryID{
		SessionID:  s.id,
		BackendPID: s.backendPID,
		Started:    started,
		Nonce:      queryNonceCounter.Add(1),
	}

	return newPgRowStream(rows, qid, s.releaseInFlight), nil
}

// Explain runs EXPLAIN against q.SQL in both FORMAT JSON (parsed into
// models.Plan.Node) and the default text format (joined into
// models.Plan.RawText). When analyze is true, ANALYZE is included in both
// statements — the caller is responsible for ensuring this is safe (no
// side-effect-producing statements without a transaction; the auto-rollback
// wrapping lives in the controller layer, task 66p.11). q.Args are forwarded
// to pgx and substituted for $N placeholders in the EXPLAIN'd statement.
//
// Failure of EITHER the JSON or the text EXPLAIN returns an error and a
// zero-value Plan; we deliberately do not silently degrade because both
// formats are part of the contract surfaced to the UI tree renderer.
//
// q.Timeout, when positive, is applied as a context.WithTimeout to the
// caller's ctx for the duration of the call. The inFlight guard is held for
// the whole call.
func (s *Session) Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error) {
	defer s.guard()()

	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	jsonSQL := "EXPLAIN (FORMAT JSON) " + q.SQL
	textSQL := "EXPLAIN " + q.SQL
	if analyze {
		jsonSQL = "EXPLAIN (ANALYZE, FORMAT JSON) " + q.SQL
		textSQL = "EXPLAIN ANALYZE " + q.SQL
	}

	// JSON format: a single row, single column carrying the JSON document.
	var rawJSON []byte
	if err := s.conn.QueryRow(ctx, jsonSQL, q.Args...).Scan(&rawJSON); err != nil {
		return models.Plan{}, wrapPgError(err)
	}

	plan, err := parsePlanJSON(rawJSON)
	if err != nil {
		return models.Plan{}, err
	}

	// Text format: one row per output line.
	rows, err := s.conn.Query(ctx, textSQL, q.Args...)
	if err != nil {
		return models.Plan{}, wrapPgError(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return models.Plan{}, wrapPgError(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return models.Plan{}, wrapPgError(err)
	}
	plan.RawText = strings.Join(lines, "\n")
	return plan, nil
}

// Begin returns an untyped-nil Transaction and drivers.ErrNotImplemented in
// v1. Returning untyped nil (not a typed-nil pointer) guarantees that
// `tx == nil` is true for the caller.
func (s *Session) Begin(_ context.Context, _ models.TxOptions) (drivers.Transaction, error) {
	defer s.guard()()
	return nil, drivers.ErrNotImplemented
}

// InTransaction reports whether this Session currently has an open
// transaction. v1 has no transaction support, so this is always false.
func (s *Session) InTransaction() bool { return false }

// CurrentTransaction returns the in-progress Transaction, or nil if none. v1
// returns nil unconditionally.
func (s *Session) CurrentTransaction() drivers.Transaction { return nil }

var _ drivers.Session = (*Session)(nil)
