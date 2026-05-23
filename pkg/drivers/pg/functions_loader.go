package pg

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ListFunctions returns the names of FUNCTION routines visible in the
// session's current_schemas (excluding implicit pg_catalog). Names are
// returned sorted alphabetically; duplicates from overloaded signatures
// collapse to a single entry per name because routine_name alone is
// projected. See epic dbsavvy-bwq §13.3 (child .20).
func (s *Session) ListFunctions(ctx context.Context) ([]string, error) {
	defer s.guard()()
	rows, err := s.conn.Query(ctx, sqlListFunctions)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	return scanFunctionNames(rows)
}

// scanFunctionNames consumes a *pgx.Rows iterator whose column shape
// matches sql/list_functions.sql (single text column) and returns the
// materialized slice. Factored out so unit tests can exercise the scan
// logic without standing up a live database. The returned slice is
// always non-nil so callers can rely on len()==0 for the empty case.
func scanFunctionNames(rows pgx.Rows) ([]string, error) {
	out := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, wrapPgError(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}
