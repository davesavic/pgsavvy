package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
)

// StatementTimeoutMessage is the verbatim cancel-reason surfaced when a
// streamed query is cut off by the default-statement-timeout ceiling
// (context.WithTimeout deadline). The result tab renders this string so the
// user can tell a ceiling-induced stop apart from an explicit <leader>x
// cancel, which keeps the plain "cancelled" state. dbsavvy-fow.7 (U15).
const StatementTimeoutMessage = "cancelled: statement timeout"

// IsStatementTimeout reports whether err was caused by a
// context.WithTimeout deadline firing (the statement-timeout ceiling)
// rather than an explicit user cancellation. pgx wraps the underlying
// context error in its own type, so the classification traverses the wrap
// chain via errors.Is.
//
// The distinction is load-bearing: a deadline surfaces
// context.DeadlineExceeded → "cancelled: statement timeout"; a user
// <leader>x / preemption surfaces context.Canceled → the existing plain
// user-cancel state. Both can ride inside the same pgx error wrapper, so
// callers MUST consult IsStatementTimeout before falling back to the
// generic cancel path. dbsavvy-fow.7 (U15).
func IsStatementTimeout(err error) bool {
	if err == nil {
		return false
	}
	// context.Canceled and context.DeadlineExceeded are independent
	// sentinels; a canceled context never resolves as DeadlineExceeded, so
	// a single errors.Is is sufficient to keep the two apart.
	return errors.Is(err, context.DeadlineExceeded)
}

// wrapPgError translates a pgconn.PgError into a *drivers.QueryError so that
// the rest of the codebase can branch on engine-neutral fields (Code,
// Severity, Position, …) instead of importing pgconn. Non-pgconn errors and
// nil are returned unchanged. This helper is intentionally narrow: callers
// should apply it to scan-result errors only and leave context-cancellation
// or connection-acquisition errors alone.
func wrapPgError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	return &drivers.QueryError{
		Raw:        err,
		Code:       pgErr.Code,
		Severity:   pgErr.Severity,
		Hint:       pgErr.Hint,
		Detail:     pgErr.Detail,
		Where:      pgErr.Where,
		Schema:     pgErr.SchemaName,
		Table:      pgErr.TableName,
		Column:     pgErr.ColumnName,
		Constraint: pgErr.ConstraintName,
		Position:   int(pgErr.Position),
	}
}

// IsConnectionDead delegates to drivers.IsConnectionDead. Kept here for
// backwards compatibility with callers that already import pg. hq5.6.
func IsConnectionDead(err error) bool {
	return drivers.IsConnectionDead(err)
}
