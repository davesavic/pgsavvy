package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// queryNonceCounter is the process-global monotonic source for QueryID.Nonce.
// Stamped on every Execute/Stream so that two queries on the same Session at
// the same instant remain distinguishable.
var queryNonceCounter atomic.Uint64

// sessionIDCounter is the process-global monotonic source for Session.ID().
// Incremented atomically at construction.
var sessionIDCounter atomic.Uint64

// Session is a stateful checkout of a *Connection. It wraps a single
// pgxpool.Conn for the duration of its lifetime; Close releases the pooled
// connection. Session methods are NOT safe for concurrent use by multiple
// goroutines — callers must serialize. The inFlight guard (D18) panics on
// detected re-entry rather than corrupting protocol state silently.
//
// ListTables is the only public entry point for relation listing; TableLoader
// is package-private machinery exposed to enrichment workers. See Arch-5 of
// the review-plan resolutions.
type Session struct {
	conn       *pgxpool.Conn
	id         models.SessionID
	backendPID uint32         // D19 — sized to match pgconn.PgConn.PID()
	secretKey  uint32         // cancel-request authentication; captured from pgconn at construction
	pgConn     *pgconn.PgConn // captured at newSession so Close can unbind from NoticeRouter
	parent     *Connection
	closed     atomic.Bool
	inFlight   atomic.Int32
	openedAt   time.Time // session_open timestamp; used for session_close ms field
	activeTx   *pgTransaction
}

// newSession constructs a *Session bound to pgxConn and parent. Session.ID is
// assigned from sessionIDCounter atomically; backendPID and secretKey are
// captured from the underlying pgconn — both are required by the cancel-request
// wire protocol and remain stable for the life of the
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
		openedAt:   time.Now(),
	}
	parent.registerCancel(pid, secret)
	if parent.notices != nil {
		parent.notices.bindConn(pgc, s.id)
	}
	logs.Event(pkgLogger(), "db", "session_open",
		slog.Uint64("sid", uint64(s.id)),
		slog.Uint64("backend_pid", uint64(pid)),
	)
	return s
}

// SecretKey returns the PostgreSQL cancel-request secret key captured from the
// underlying pgconn at session-open time. The value is required to authenticate
// a cancel-request packet for this backend. It is
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
	logs.Event(pkgLogger(), "db", "session_close",
		slog.Uint64("sid", uint64(s.id)),
		slog.Int64("ms", time.Since(s.openedAt).Milliseconds()),
	)
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
// diagnostic in the messages writer. Zero when no
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

// volatilityFromChar maps pg_proc.provolatile (i/s/v) to the human-readable
// volatility label stored on models.FunctionDetail. Unknown chars pass through
// unchanged so a future Postgres value is visible rather than silently dropped.
func volatilityFromChar(c string) string {
	switch c {
	case "i":
		return "IMMUTABLE"
	case "s":
		return "STABLE"
	case "v":
		return "VOLATILE"
	default:
		return c
	}
}

// argModeFromChar maps a pg_proc.proargmodes element to the FunctionArg.Mode
// label. The fallback (used when proargmodes is NULL, encoded as 'i' by the
// SQL) is IN. Unknown chars pass through unchanged.
func argModeFromChar(c string) string {
	switch c {
	case "i":
		return "IN"
	case "o":
		return "OUT"
	case "b":
		return "INOUT"
	case "v":
		return "VARIADIC"
	case "t":
		return "TABLE"
	default:
		return c
	}
}

// describeFunctionArg is the per-element shape of the JSON args column produced
// by sql/describe_function.sql. mode carries the raw pg_proc char.
type describeFunctionArg struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

// DescribeFunction returns one models.FunctionDetail per overload of
// (schema, name) — Postgres permits many pg_proc rows sharing schema+name with
// distinct argument types. A non-existent (schema, name) yields an empty slice
// and a nil error. schema + name are bound as query params ($1/$2); no
// identifier interpolation. Every query/scan error is wrapped via wrapPgError
// and logged through logs.Event.
func (s *Session) DescribeFunction(ctx context.Context, schema, name string) ([]models.FunctionDetail, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	rows, err := s.conn.Query(ctx, sqlDescribeFunction, schema, name)
	if err != nil {
		werr := wrapPgError(err)
		logs.Event(pkgLogger(), "db", "describe_function_error",
			slog.String("schema", schema),
			slog.String("name", name),
			slog.String("err", werr.Error()),
		)
		return nil, werr
	}
	defer rows.Close()
	var out []models.FunctionDetail
	for rows.Next() {
		var (
			fd       models.FunctionDetail
			vol      string
			argsJSON []byte
		)
		if err := rows.Scan(&fd.Schema, &fd.Name, &fd.ReturnType, &vol, &fd.Language, &argsJSON); err != nil {
			werr := wrapPgError(err)
			logs.Event(pkgLogger(), "db", "describe_function_error",
				slog.String("schema", schema),
				slog.String("name", name),
				slog.String("err", werr.Error()),
			)
			return nil, werr
		}
		fd.Volatility = volatilityFromChar(vol)
		var raw []describeFunctionArg
		if err := json.Unmarshal(argsJSON, &raw); err != nil {
			logs.Event(pkgLogger(), "db", "describe_function_error",
				slog.String("schema", schema),
				slog.String("name", name),
				slog.String("err", err.Error()),
			)
			return nil, fmt.Errorf("describe_function: decode args for %s.%s: %w", schema, name, err)
		}
		fd.Args = make([]models.FunctionArg, 0, len(raw))
		for _, a := range raw {
			fd.Args = append(fd.Args, models.FunctionArg{
				Name: a.Name,
				Type: a.Type,
				Mode: argModeFromChar(a.Mode),
			})
		}
		out = append(out, fd)
	}
	if err := rows.Err(); err != nil {
		werr := wrapPgError(err)
		logs.Event(pkgLogger(), "db", "describe_function_error",
			slog.String("schema", schema),
			slog.String("name", name),
			slog.String("err", werr.Error()),
		)
		return nil, werr
	}
	return out, nil
}

// Execute runs q.SQL with q.Args and materializes the entire result set into
// a models.Result. Columns is populated from pgx FieldDescriptions; Rows is a
// row-major copy of pgx.Rows.Values(); RowsAffected is taken from the command
// tag; Duration spans the wall-clock from query dispatch to materialization.
// A *pgconn.PgError is mapped to *drivers.QueryError via wrapPgError. The
// inFlight guard is held for the entire call. Cancel/NOTICE/EXPLAIN are out
// of scope.
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
// the responsibility of the calling layer (see pkg/session.SQLSession).
// The QueryID returned by the stream is fully populated (SessionID,
// BackendPID, Started, Nonce all non-zero) BEFORE the first Next() call
// returns; QueryID() may safely be read up front.
func (s *Session) Stream(ctx context.Context, q models.Query) (drivers.RowStream, error) {
	s.acquireInFlight()

	// Apply the per-query statement-timeout ceiling, when set, to the SAME
	// ctx that governs both the search_path SET and the streaming Query.
	// The derived context.CancelFunc is handed to the returned pgRowStream,
	// which invokes it exactly once in release() (EOF / terminal error /
	// explicit Close) so the deadline timer never leaks past the stream.
	// q.Timeout == 0 leaves ctx untouched (no ceiling) and cancel stays nil.
	// The non-zero q.Timeout is the override the run
	// path sets when it wants a different ceiling than the configured
	// default; the default itself is folded into q.Timeout by the caller.
	var cancel context.CancelFunc
	if q.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
	}

	// Resolve unqualified object names against q.DefaultSchema (then public)
	// for this statement. No-op when empty.
	if stmt := searchPathStmt(q.DefaultSchema); stmt != "" {
		if _, err := s.conn.Exec(ctx, stmt); err != nil {
			if cancel != nil {
				cancel()
			}
			s.releaseInFlight()
			return nil, wrapPgError(err)
		}
	}

	started := time.Now()
	rows, err := s.conn.Query(ctx, q.SQL, q.Args...)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		s.releaseInFlight()
		return nil, wrapPgError(err)
	}

	qid := models.QueryID{
		SessionID:  s.id,
		BackendPID: s.backendPID,
		Started:    started,
		Nonce:      queryNonceCounter.Add(1),
	}

	return newPgRowStream(rows, qid, s.releaseInFlight, cancel), nil
}

// searchPathStmt builds the SET search_path statement that makes unqualified
// object names resolve against schema first, then public. The schema is
// quoted via pgx.Identifier.Sanitize so names with special characters (or a
// crafted name) cannot break out of the identifier. Returns "" when schema is
// empty, which callers treat as "leave the search_path untouched".
func searchPathStmt(schema string) string {
	if schema == "" {
		return ""
	}
	return "SET search_path TO " + pgx.Identifier{schema}.Sanitize() + ", public"
}

// Explain runs EXPLAIN against q.SQL in both FORMAT JSON (parsed into
// models.Plan.Node) and the default text format (joined into
// models.Plan.RawText). When analyze is true, ANALYZE is included in both
// statements — the caller is responsible for ensuring this is safe (no
// side-effect-producing statements without a transaction; the auto-rollback
// wrapping lives in the controller layer). q.Args are forwarded
// to pgx and substituted for $N placeholders in the EXPLAIN'd statement.
//
// Failure of EITHER the JSON or the text EXPLAIN returns an error and a
// zero-value Plan; we deliberately do not silently degrade because both
// formats are part of the contract surfaced to the UI tree renderer.
//
// q.Timeout, when positive, is applied as a context.WithTimeout to the
// caller's ctx for the duration of the call. The inFlight guard is held for
// the whole call.
func (s *Session) Explain(ctx context.Context, q models.Query, analyze bool) (plan models.Plan, retErr error) {
	defer s.guard()()

	if q.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Timeout)
		defer cancel()
	}

	// Resolve unqualified object names against q.DefaultSchema (then public)
	// for the EXPLAIN'd statement. No-op when empty.
	if stmt := searchPathStmt(q.DefaultSchema); stmt != "" {
		if _, err := s.conn.Exec(ctx, stmt); err != nil {
			return models.Plan{}, wrapPgError(err)
		}
	}

	start := time.Now()
	log := pkgLogger()
	defer func() {
		attrs := []slog.Attr{
			slog.Uint64("sid", uint64(s.id)),
			slog.Bool("analyze", analyze),
			slog.Int64("ms", time.Since(start).Milliseconds()),
		}
		if retErr != nil {
			attrs = append(attrs, slog.Any("err", retErr.Error()))
		}
		logs.Event(log, "db", "explain", attrs...)
	}()

	// The enriched JSON option set captures diagnostics (buffers, verbose
	// output, server settings) that the bare form discards; only the JSON
	// document is parsed, so textSQL stays a bare EXPLAIN [ANALYZE] for the
	// human-readable RawText pane.
	jsonSQL, textSQL := enrichedExplainSQL(q.SQL, analyze)

	// JSON format: a single row, single column carrying the JSON document.
	var rawJSON []byte
	var notice string
	err := s.conn.QueryRow(ctx, jsonSQL, q.Args...).Scan(&rawJSON)
	if isUnsupportedExplainOption(err) {
		// PG<12 / a server that rejects the enriched option set: retry with
		// the bare option set so EXPLAIN still succeeds, just without the
		// extra diagnostics.
		jsonSQL, textSQL = bareExplainSQL(q.SQL, analyze)
		notice = "EXPLAIN options unsupported by server; showing basic plan"
		err = s.conn.QueryRow(ctx, jsonSQL, q.Args...).Scan(&rawJSON)
	}
	if err != nil {
		return models.Plan{}, wrapPgError(err)
	}

	plan, err = parsePlanJSON(rawJSON)
	if err != nil {
		return models.Plan{}, err
	}
	plan.Notice = notice

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

// enrichedExplainSQL builds the JSON and text EXPLAIN statements with the
// enriched option set. The JSON form adds BUFFERS, VERBOSE and SETTINGS so the
// plan document carries diagnostics the bare form discards. The text form stays
// a bare EXPLAIN [ANALYZE]: only the JSON document is parsed, so the text pane
// only needs the human-readable plan.
func enrichedExplainSQL(sql string, analyze bool) (jsonSQL, textSQL string) {
	if analyze {
		return "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON) " + sql,
			"EXPLAIN ANALYZE " + sql
	}
	return "EXPLAIN (VERBOSE, SETTINGS, FORMAT JSON) " + sql,
		"EXPLAIN " + sql
}

// bareExplainSQL builds the minimal EXPLAIN statements used as the fallback when
// a server rejects the enriched option set (e.g. PG<12, which lacks SETTINGS).
func bareExplainSQL(sql string, analyze bool) (jsonSQL, textSQL string) {
	if analyze {
		return "EXPLAIN (ANALYZE, FORMAT JSON) " + sql, "EXPLAIN ANALYZE " + sql
	}
	return "EXPLAIN (FORMAT JSON) " + sql, "EXPLAIN " + sql
}

// isUnsupportedExplainOption reports whether err is a server-side syntax error
// (SQLSTATE 42601), which is what PostgreSQL returns when it does not recognize
// an EXPLAIN option (e.g. SETTINGS on PG<12). Used to trigger the bare-EXPLAIN
// fallback. Any other error is propagated unchanged.
func isUnsupportedExplainOption(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42601"
}

// Begin starts a new transaction on this Session. Only one transaction may be
// active at a time; calling Begin while a transaction is already open returns
// an error. The returned Transaction wraps the underlying pgx.Tx. TxOptions
// are mapped to pgx.TxOptions for isolation level, read-only, and deferrable.
func (s *Session) Begin(ctx context.Context, opts models.TxOptions) (drivers.Transaction, error) {
	defer s.guard()()
	if s.InTransaction() {
		return nil, errors.New("session: transaction already in progress")
	}
	pgxOpts := pgx.TxOptions{
		IsoLevel:       pgx.TxIsoLevel(opts.IsoLevel),
		DeferrableMode: pgx.TxDeferrableMode(boolToDeferrable(opts.Deferrable)),
	}
	if opts.ReadOnly {
		pgxOpts.AccessMode = pgx.ReadOnly
	}
	tx, err := s.conn.BeginTx(ctx, pgxOpts)
	if err != nil {
		return nil, wrapPgError(err)
	}
	t := newPgTransaction(tx)
	s.activeTx = t
	return t, nil
}

// boolToDeferrable maps a boolean to the pgx deferrable mode string.
func boolToDeferrable(d bool) string {
	if d {
		return string(pgx.Deferrable)
	}
	return string(pgx.NotDeferrable)
}

// InTransaction reports whether this Session currently has an active transaction.
func (s *Session) InTransaction() bool {
	return s.activeTx != nil && s.activeTx.status == models.TxActive
}

// CurrentTransaction returns the in-progress Transaction, or nil if none.
// Returns nil for terminal transactions (committed/rolled back).
func (s *Session) CurrentTransaction() drivers.Transaction {
	if !s.InTransaction() {
		return nil
	}
	return s.activeTx
}

// Encoder returns the stateless literal encoder for this Postgres session.
// The same singleton value is returned on every call. See pkg/drivers/pg/encoder.go.
func (s *Session) Encoder() drivers.Encoder { return pgEncoder }

var _ drivers.Session = (*Session)(nil)
