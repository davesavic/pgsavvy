package pg

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/drivers"
)

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
