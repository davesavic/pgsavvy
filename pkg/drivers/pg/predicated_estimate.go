package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// EstimatePredicatedRows resolves the planner's row estimate for the inbound
// child rows referencing a focused parent row through one inbound foreign key.
// fk is an INBOUND constraint (from FKCache.GetReverse): fk.Schema/Table/Columns
// describe the REFERENCING child table + its referencing columns; fk.RefColumns
// describe the parent's referenced columns, paired positionally with refValues
// (the focused parent row's key cell values).
//
// It builds "SELECT * FROM <child> WHERE <childcol> = $1 [AND ...]", runs a
// planner-only EXPLAIN (FORMAT JSON, analyze=false) under the supplied timeout,
// and returns the TOP-node "Plan Rows" estimate. The query never executes — it
// only plans — so it is side-effect free.
//
// Returns ErrCompositeMismatch (no query issued) when len(fk.Columns) !=
// len(refValues) or the column list is empty. Any driver/timeout/permission
// error propagates so the caller can mark that line degraded while the rest of
// the panel survives. No row VALUES are logged here (they pass through $N
// params only).
func EstimatePredicatedRows(ctx context.Context, sess *Session, fk models.ForeignKey, refValues []any, timeout time.Duration) (int64, error) {
	if sess == nil {
		return 0, errors.New("pg: predicated-estimate resolver has no session")
	}
	if len(fk.Columns) == 0 || len(fk.Columns) != len(refValues) {
		return 0, ErrCompositeMismatch
	}

	sql := buildPredicatedEstimateSQL(fk)
	plan, err := sess.Explain(ctx, models.Query{SQL: sql, Args: refValues, Timeout: timeout}, false)
	if err != nil {
		return 0, err
	}
	if plan.Node == nil {
		return 0, nil
	}
	return plan.Node.EstRows, nil
}

// buildPredicatedEstimateSQL composes the predicated SELECT against the
// REFERENCING (child) table. Every identifier (schema, table, predicate
// columns) is quoted via QuoteIdent / QuoteQualified; every value is bound as a
// $N parameter — mirrors buildDisplayValueSQL / buildFKReverseSQL.
func buildPredicatedEstimateSQL(fk models.ForeignKey) string {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
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
