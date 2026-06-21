package pg

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
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
// through an explicit consumer Close.
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

	// cancel is the context.CancelFunc of the statement-timeout deadline
	// derived in Session.Stream (context.WithTimeout). It is captured here
	// so release() can stop the deadline timer EXACTLY ONCE, on the same
	// CAS-guarded path as rows.Close + releaseGuard — no leaked timer
	// goroutine or pooled-connection deadline past release. nil when the
	// stream had no timeout (q.Timeout == 0 and no default ceiling), in
	// which case release() skips it.
	cancel context.CancelFunc

	// sample refreshes the parent Session's cached transaction status. It is
	// invoked by release() ONLY on the clean drain path (EOF / terminal pgx
	// error / explicit Close) AFTER rows.Close, so the pgconn status byte is
	// final. It is deliberately NOT called from the ctx.Done force-close branch
	// of Next, where a Next goroutine may still be blocked on the connection —
	// reading pgconn there would race the protocol (Decision ⑥). nil for
	// sessions without a sampler (e.g. tests).
	sample func()

	closed atomic.Bool

	// rowsAffected captures pgx's CommandTag().RowsAffected() at release
	// time. pgx only populates the command tag inside Rows.Close(), so it
	// is read in release() AFTER s.rows.Close() — reading it earlier (e.g.
	// in the EOF branch of Next before release) yields 0. Surfaced via
	// RowsAffected() so the UI can report "N rows affected" for DML that
	// returns no result rows.
	rowsAffected atomic.Int64
}

// newPgRowStream wraps a freshly-issued pgx.Rows. The caller (Session.Stream)
// is responsible for having already acquired the parent session's inFlight
// guard; releaseGuard MUST be the session.releaseInFlight bound method.
//
// cancel is the statement-timeout deadline's context.CancelFunc (from the
// context.WithTimeout derived in Session.Stream), or nil when the stream has
// no timeout. release() invokes it exactly once so the deadline timer is
// stopped on EOF / terminal-error / explicit Close without leaking.
func newPgRowStream(rows pgx.Rows, qid models.QueryID, releaseGuard func(), cancel context.CancelFunc, sample func()) *pgRowStream {
	return &pgRowStream{
		rows:         rows,
		queryID:      qid,
		columns:      fieldDescriptionsToColumnMetas(rows.FieldDescriptions()),
		releaseGuard: releaseGuard,
		cancel:       cancel,
		sample:       sample,
	}
}

// Columns returns the result-set column metadata. Safe to call before the
// first Next; safe to call after Close (the slice was captured at
// construction).
func (s *pgRowStream) Columns() []models.ColumnMeta { return s.columns }

// QueryID returns the QueryID stamped at Stream() time. Every field
// (SessionID, BackendPID, Started, Nonce) is populated before the stream is
// returned, so callers may read this BEFORE calling Next() to wire cancel
// or result routing.
func (s *pgRowStream) QueryID() models.QueryID { return s.queryID }

// RowsAffected returns the command tag's affected-row count, captured in
// release() once the stream terminates. Returns 0 before termination.
func (s *pgRowStream) RowsAffected() int64 { return s.rowsAffected.Load() }

// Next advances the underlying pgx.Rows by one. The (row, ok, err) triple
// matches drivers.RowStream:
//   - (row, true,  nil) — a row was decoded
//   - (zero, false, nil) — clean end-of-result
//   - (zero, false, err) — pgx error, ctx cancellation, or use-after-Close
//
// watchdog: ctx is now observed. If ctx is cancelled while
// pgx.Rows.Next is blocked on a dead socket, the watchdog goroutine
// force-closes the rows so the read unblocks and streamMu is released
// within a bounded window rather than hanging until TCP keepalive fires
// (potentially minutes).
func (s *pgRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	if s.closed.Load() {
		return models.Row{}, false, ErrRowStreamClosed
	}

	// Fast path: context already done. Do not sample — the connection's tx
	// status may be mid-protocol on a cancelled context (Decision ⑥).
	if err := ctx.Err(); err != nil {
		s.release(false)
		return models.Row{}, false, err
	}

	// Race rows.Next against context cancellation. On a healthy
	// connection rows.Next returns promptly and the goroutine
	// overhead is negligible relative to the network round-trip.
	type nextResult struct {
		ok bool
	}
	ch := make(chan nextResult, 1)
	go func() {
		ch <- nextResult{ok: s.rows.Next()}
	}()

	select {
	case res := <-ch:
		if !res.ok {
			var nextErr error
			if err := s.rows.Err(); err != nil {
				nextErr = wrapPgError(err)
			}
			// EOF or terminal stream error — drop the session inFlight
			// guard now rather than stranding it until the consumer calls
			// Close. CAS-guarded by `closed`. This is the clean-drain path:
			// the Next goroutine has returned, so sampling pgconn is safe.
			s.release(true)
			return models.Row{}, false, nextErr
		}
		vals, err := s.rows.Values()
		if err != nil {
			return models.Row{}, false, wrapPgError(err)
		}
		// Clip oversized cell values at the stream boundary so a
		// wide-payload query can't accumulate hundreds of MB of heap
		// across the buffered rows and stall the process under GC
		// pressure.
		capRowValues(vals, s.columns)
		return models.Row{Values: vals}, true, nil

	case <-ctx.Done():
		// Context cancelled while rows.Next was blocked (dead
		// connection or Stop). Force-close the underlying rows to
		// unblock the goroutine. Close/release is idempotent via
		// the CAS on closed. Do NOT sample: the Next goroutine is still
		// blocked on the connection, so reading pgconn would race the
		// protocol (Decision ⑥).
		s.release(false)
		return models.Row{}, false, ctx.Err()
	}
}

// Close releases the underlying pgx.Rows and drops the parent Session's
// inFlight guard. Idempotent: a second call (or a call after Next observed
// EOF / terminal error and already released) is a no-op and returns nil. The
// closed flag is set BEFORE rows.Close so a concurrent Next observes the
// sentinel rather than a pgx "rows are closed" string.
func (s *pgRowStream) Close() error {
	// Explicit consumer Close is a clean drain — the consumer has stopped
	// calling Next, so sampling the live tx status is safe.
	s.release(true)
	return nil
}

// release is the single-shot release path shared by Close and the EOF /
// terminal-error branch of Next. The CAS on closed makes both call sites
// idempotent: the first caller to win the swap runs the cleanup; every
// subsequent caller observes closed=true and returns without touching
// rows.Close or releaseGuard.
// release runs the single-shot cleanup. sample requests a refresh of the
// parent Session's cached transaction status; pass true only on the clean
// drain path (EOF / terminal error / explicit Close) where no Next goroutine
// is still touching the connection, and false on the ctx.Done force-close
// path (Decision ⑥). The sampler runs AFTER rows.Close so the trailing
// ReadyForQuery has settled the pgconn status byte.
func (s *pgRowStream) release(sample bool) {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.rows.Close()
	// pgx populates the command tag inside Rows.Close(), so capture the
	// affected-row count here (after Close) rather than at the EOF branch
	// of Next where it would still be zero.
	s.rowsAffected.Store(s.rows.CommandTag().RowsAffected())
	// Stop the statement-timeout deadline timer (if any) on the same
	// once-guarded path. Calling cancel after the deadline already fired is
	// a documented no-op, so this is safe regardless of WHY we released
	// (EOF, terminal error, timeout, or explicit Close).
	if s.cancel != nil {
		s.cancel()
	}
	if s.releaseGuard != nil {
		s.releaseGuard()
	}
	if sample && s.sample != nil {
		s.sample()
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
