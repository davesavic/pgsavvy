package pg

import (
	"context"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

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
	backendPID uint32 // D19 — sized to match pgconn.PgConn.PID()
	profile    models.Connection
	parent     *Connection
	closed     atomic.Bool
	inFlight   atomic.Int32
}

// newSession constructs a *Session bound to pgxConn and parent. Session.ID is
// assigned from sessionIDCounter atomically; backendPID is captured from the
// underlying pgconn for QueryID stamping in later epics.
func newSession(pgxConn *pgxpool.Conn, parent *Connection) *Session {
	return &Session{
		conn:       pgxConn,
		id:         models.SessionID(sessionIDCounter.Add(1)),
		backendPID: pgxConn.Conn().PgConn().PID(),
		parent:     parent,
	}
}

// Conn exposes the underlying pgxpool.Conn to same-package loaders that have
// ALREADY acquired the inFlight guard via their calling Session method. It is
// NOT guarded itself — calling it from outside the package is a programmer
// error. See TableLoader for the intended usage pattern.
func (s *Session) Conn() *pgxpool.Conn { return s.conn }

// ID returns the monotonic per-process session identifier.
func (s *Session) ID() models.SessionID { return s.id }

// guard panics on use-after-Close or concurrent use. Returns a release
// function that must be deferred. Every public Session method that touches
// s.conn MUST start with: defer s.guard()().
func (s *Session) guard() func() {
	if s.closed.Load() {
		panic("session: use after Close")
	}
	if !s.inFlight.CompareAndSwap(0, 1) {
		panic("session: concurrent use")
	}
	return func() { s.inFlight.Store(0) }
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
	s.conn.Release()
	s.parent.sessions.Add(-1)
	return nil
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

// Execute returns drivers.ErrNotImplemented in v1 — query execution wiring
// lands in a later epic.
func (s *Session) Execute(_ context.Context, _ models.Query) (models.Result, error) {
	defer s.guard()()
	return models.Result{}, drivers.ErrNotImplemented
}

// Stream returns a nil RowStream and drivers.ErrNotImplemented in v1. The
// returned interface value is intentionally untyped-nil so callers may
// short-circuit on `stream == nil` without an unwrap.
func (s *Session) Stream(_ context.Context, _ models.Query) (drivers.RowStream, error) {
	defer s.guard()()
	return nil, drivers.ErrNotImplemented
}

// Explain returns a zero-value Plan and drivers.ErrNotImplemented in v1.
func (s *Session) Explain(_ context.Context, _ models.Query, _ bool) (models.Plan, error) {
	defer s.guard()()
	return models.Plan{}, drivers.ErrNotImplemented
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
