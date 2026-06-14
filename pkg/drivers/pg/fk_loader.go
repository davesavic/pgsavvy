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
// 'c'->CASCADE, 'n'->SET NULL, 'd'->SET DEFAULT). See B1.
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

// TableReltuples returns the pg_class.reltuples upper-bound row estimate
// for (schema, table). reltuples is a float4 in the catalog; positive
// values are the planner's row estimate (round up to derive `~N rows`),
// zero means "table has zero rows", and a negative value (typically -1)
// signals "no ANALYZE since last reset", which the caller renders as
// `~? rows`. Returns 0 + an error when the table is not found. (B6).
func (s *Session) TableReltuples(ctx context.Context, schema, table string) (float32, error) {
	defer s.guard()()
	const q = `SELECT c.reltuples FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`
	row := s.conn.QueryRow(ctx, q, schema, table)
	var rt float32
	if err := row.Scan(&rt); err != nil {
		return 0, wrapPgError(err)
	}
	return rt, nil
}

// ListInboundForeignKeys returns every foreign-key constraint whose
// REFERENCED side is (schema, table) — i.e. the FKs that point AT this
// table from other tables. The model shape mirrors ListForeignKeys: ref*
// fields hold the input table (schema, table), and Schema/Table/Columns
// hold the REFERENCING table. The result is empty (non-nil) when no other
// table references this one. Self-referencing FKs appear in the result
// just like any other reference. See B6.
func (s *Session) ListInboundForeignKeys(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	defer s.guard()()
	s.parent.warnIfPostgresGE18()
	rows, err := s.conn.Query(ctx, sqlListInboundForeignKeys, schema, table)
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
