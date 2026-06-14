package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// CountPredicatedRows resolves the EXACT number of inbound child rows
// referencing a focused parent row through one inbound foreign key. It is the
// on-demand counterpart of EstimatePredicatedRows: where the estimate runs a
// planner-only EXPLAIN, this runs a real COUNT(*) so a FOCUSED inbound line can
// replace its "~estimate" with an exact figure.
//
// fk is an INBOUND constraint (from FKCache.GetReverse): fk.Schema/Table/Columns
// describe the REFERENCING child table + its referencing columns; fk.RefColumns
// describe the parent's referenced columns, paired positionally with refValues
// (the focused parent row's key cell values).
//
// It builds "SELECT COUNT(*) FROM <child> WHERE <childcol> = $1 [AND ...]", runs
// it under the supplied statement timeout, and returns the single scalar.
//
// Returns ErrCompositeMismatch (no query issued) when len(fk.Columns) !=
// len(refValues) or the column list is empty. Any driver/timeout/permission
// error propagates so the caller can fall back to the estimate (timeout) or mark
// the line degraded (permission/other). No row VALUES are logged here (they pass
// through $N params only).
func CountPredicatedRows(ctx context.Context, sess *Session, fk models.ForeignKey, refValues []any, timeout time.Duration) (int64, error) {
	if sess == nil {
		return 0, errors.New("pg: exact-count resolver has no session")
	}
	if len(fk.Columns) == 0 || len(fk.Columns) != len(refValues) {
		return 0, ErrCompositeMismatch
	}

	sql := buildPredicatedCountSQL(fk)
	res, err := sess.Execute(ctx, models.Query{SQL: sql, Args: refValues, Timeout: timeout})
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 || res.Rows[0] == nil || len(res.Rows[0].Values) == 0 {
		return 0, nil
	}
	return coerceCount(res.Rows[0].Values[0]), nil
}

// buildPredicatedCountSQL composes the COUNT(*) against the REFERENCING (child)
// table. Every identifier (schema, table, predicate columns) is quoted via
// QuoteIdent / QuoteQualified; every value is bound as a $N parameter — mirrors
// buildPredicatedEstimateSQL.
func buildPredicatedCountSQL(fk models.ForeignKey) string {
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(QuoteQualified(fk.Schema, fk.Table))
	b.WriteString(" WHERE ")
	for i, col := range fk.Columns {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(QuoteIdent(col))
		fmt.Fprintf(&b, " = $%d", i+1)
	}
	return b.String()
}

// coerceCount narrows the COUNT(*) scalar to int64. Postgres returns bigint, so
// the encoder hands back an int64; the type-switch keeps the helper robust to a
// driver that decodes a different integer width.
func coerceCount(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	default:
		return 0
	}
}
