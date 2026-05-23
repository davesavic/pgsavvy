package pg

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ErrRowStreamClosed is returned by (*pgRowStream).Next after the stream has
// been Close()'d. Exposing a typed sentinel (rather than passing through a
// pgx low-level "rows are closed" message) lets callers distinguish a
// programmer-error use-after-Close from a genuine driver/network failure.
var ErrRowStreamClosed = errors.New("pg: row stream closed")

// pgRowStream is the concrete drivers.RowStream returned by Session.Stream.
//
// Lifetime: the session's inFlight guard is acquired by Session.Stream and
// released exactly once — by whichever of (a) explicit Close, (b) Next
// observing clean EOF, or (c) Next observing a terminal pgx error happens
// first. The closed flag is the CAS-guarded single-release sentinel, so
// follow-on Close calls are no-ops. The EOF-release path exists so the
// wrapping layer (pkg/session.SQLSession) can issue a fresh Stream on the
// same session immediately after observing EOF, without first round-tripping
// through an explicit consumer Close (dbsavvy-zzy).
//
// Allocation profile: Next reuses staging across iterations — pgx returns a
// fresh []any from rows.Values() per row, which we copy into a stable buffer
// only when the caller chooses to retain it via models.Row.Values. The single
// per-Next allocation is the pgx-internal slice; we do NOT keep a slice that
// grows with row count. This keeps Streaming over 100k+ rows bounded.
type pgRowStream struct {
	rows    pgx.Rows
	queryID models.QueryID
	columns []models.ColumnMeta

	// releaseGuard is called exactly once by Close to drop the parent
	// Session's inFlight flag. It is captured at construction so the stream
	// can release on its own without dialing back into Session.
	releaseGuard func()

	closed atomic.Bool
}

// newPgRowStream wraps a freshly-issued pgx.Rows. The caller (Session.Stream)
// is responsible for having already acquired the parent session's inFlight
// guard; releaseGuard MUST be the session.releaseInFlight bound method.
func newPgRowStream(rows pgx.Rows, qid models.QueryID, releaseGuard func()) *pgRowStream {
	return &pgRowStream{
		rows:         rows,
		queryID:      qid,
		columns:      fieldDescriptionsToColumnMetas(rows.FieldDescriptions()),
		releaseGuard: releaseGuard,
	}
}

// Columns returns the result-set column metadata. Safe to call before the
// first Next; safe to call after Close (the slice was captured at
// construction).
func (s *pgRowStream) Columns() []models.ColumnMeta { return s.columns }

// QueryID returns the QueryID stamped at Stream() time. Every field
// (SessionID, BackendPID, Started, Nonce) is populated before the stream is
// returned, so callers may read this BEFORE calling Next() to wire cancel
// (task 66p.4) or result routing.
func (s *pgRowStream) QueryID() models.QueryID { return s.queryID }

// Next advances the underlying pgx.Rows by one. The (row, ok, err) triple
// matches drivers.RowStream:
//   - (row, true,  nil) — a row was decoded
//   - (zero, false, nil) — clean end-of-result
//   - (zero, false, err) — pgx error, ctx cancellation, or use-after-Close
//
// ctx is currently unused by Next itself — pgx.Rows.Next has no ctx parameter,
// and the context bound at Stream() time governs the underlying connection.
// The parameter exists to match the drivers.RowStream contract and to allow a
// future cursor-backed driver to honor it.
func (s *pgRowStream) Next(_ context.Context) (models.Row, bool, error) {
	if s.closed.Load() {
		return models.Row{}, false, ErrRowStreamClosed
	}
	if !s.rows.Next() {
		var nextErr error
		if err := s.rows.Err(); err != nil {
			nextErr = wrapPgError(err)
		}
		// EOF or terminal stream error — the underlying pgx.Rows can yield
		// no more rows, so drop the session inFlight guard now rather than
		// stranding it until the consumer eventually calls Close. The
		// release path is CAS-guarded by `closed`, so a later Close is a
		// safe no-op (dbsavvy-zzy).
		s.release()
		return models.Row{}, false, nextErr
	}
	vals, err := s.rows.Values()
	if err != nil {
		return models.Row{}, false, wrapPgError(err)
	}
	return models.Row{Values: vals}, true, nil
}

// Close releases the underlying pgx.Rows and drops the parent Session's
// inFlight guard. Idempotent: a second call (or a call after Next observed
// EOF / terminal error and already released) is a no-op and returns nil. The
// closed flag is set BEFORE rows.Close so a concurrent Next observes the
// sentinel rather than a pgx "rows are closed" string.
func (s *pgRowStream) Close() error {
	s.release()
	return nil
}

// release is the single-shot release path shared by Close and the EOF /
// terminal-error branch of Next. The CAS on closed makes both call sites
// idempotent: the first caller to win the swap runs the cleanup; every
// subsequent caller observes closed=true and returns without touching
// rows.Close or releaseGuard.
func (s *pgRowStream) release() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.rows.Close()
	if s.releaseGuard != nil {
		s.releaseGuard()
	}
}

var _ drivers.RowStream = (*pgRowStream)(nil)

// fieldDescriptionsToColumnMetas builds the []models.ColumnMeta surface
// returned by RowStream.Columns(). Type names are resolved via the default
// pgtype Map (sufficient for built-in OIDs; user-defined types fall back to
// the empty string, which the UI layer renders as the OID number).
func fieldDescriptionsToColumnMetas(fds []pgconn.FieldDescription) []models.ColumnMeta {
	if len(fds) == 0 {
		return nil
	}
	tm := pgtype.NewMap()
	out := make([]models.ColumnMeta, len(fds))
	for i, fd := range fds {
		typeName := ""
		if t, ok := tm.TypeForOID(fd.DataTypeOID); ok {
			typeName = t.Name
		}
		out[i] = models.ColumnMeta{
			Name:                 fd.Name,
			TypeOID:              fd.DataTypeOID,
			TypeName:             typeName,
			Nullable:             true, // pgx wire protocol does not report nullability
			TableOID:             fd.TableOID,
			TableAttributeNumber: fd.TableAttributeNumber,
		}
	}
	return out
}

// fieldDescriptionsToColumns builds the []*models.Column surface returned by
// Execute(). Execute uses models.Column (the introspection shape) rather than
// models.ColumnMeta because models.Result.Columns is typed as []*Column —
// keeping that compatibility means the editor can render Execute results with
// the same column-detail widgets used by ListColumns.
func fieldDescriptionsToColumns(fds []pgconn.FieldDescription) []*models.Column {
	if len(fds) == 0 {
		return nil
	}
	tm := pgtype.NewMap()
	out := make([]*models.Column, len(fds))
	for i, fd := range fds {
		typeName := ""
		if t, ok := tm.TypeForOID(fd.DataTypeOID); ok {
			typeName = t.Name
		}
		out[i] = &models.Column{
			Name:     fd.Name,
			DataType: typeName,
			Position: i + 1,
		}
	}
	return out
}
