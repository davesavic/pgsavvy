package pg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// ListForeignKeys returns every foreign-key constraint defined on
// (schema, table). The result is empty (non-nil) when the table has no FKs.
// Composite-FK column lists are returned in the matched-position order
// established by conkey/confkey unnest WITH ORDINALITY (conkey[i] pairs with
// confkey[i]). The confupdtype / confdeltype catalog letters are mapped to
// human-readable labels in the SQL ('a'->NO ACTION, 'r'->RESTRICT,
// 'c'->CASCADE, 'n'->SET NULL, 'd'->SET DEFAULT). See B1 / dbsavvy-bwq.12.
func (s *Session) ListForeignKeys(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	rows, err := s.conn.Query(ctx, sqlListForeignKeys, schema, table)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()
	return scanForeignKeys(rows)
}

// scanForeignKeys consumes a *pgx.Rows iterator whose column shape matches
// sql/list_foreign_keys.sql and returns the materialized slice. It is
// factored out so tests can exercise the scan logic against an in-memory
// row source without standing up a live database. The returned slice is
// always non-nil so callers can rely on len()==0 for the empty case.
func scanForeignKeys(rows pgx.Rows) ([]models.ForeignKey, error) {
	out := make([]models.ForeignKey, 0)
	for rows.Next() {
		var fk models.ForeignKey
		if err := rows.Scan(
			&fk.Name,
			&fk.Schema,
			&fk.Table,
			&fk.Columns,
			&fk.RefSchema,
			&fk.RefTable,
			&fk.RefColumns,
			&fk.OnUpdate,
			&fk.OnDelete,
		); err != nil {
			return nil, wrapPgError(err)
		}
		out = append(out, fk)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}
	return out, nil
}
