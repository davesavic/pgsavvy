package helpers

import (
	"fmt"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// SQLMode selects whether the builder emits parameterized ($N) placeholders
// or inlines literal values.
type SQLMode int

const (
	SQLModeParameterized SQLMode = iota
	SQLModeLiteral
)

// BuildUpdateSQL returns a single UPDATE statement for one PendingEdit.
// Parameterized mode returns the SQL with $N placeholders and the
// corresponding args slice. Literal mode inlines values via
// pg.QuoteLiteral and returns a nil args slice.
//
// WHERE clause shape (both modes):
//
//	WHERE "pk1" = $N AND "pk2" = $M AND "col" IS NOT DISTINCT FROM $L
//
// PK columns use = (not IS NOT DISTINCT FROM) because PKs are guaranteed
// NOT NULL; = preserves query planner optimizations that IS NOT DISTINCT
// FROM may disable. The OldValue guard uses IS NOT DISTINCT FROM for
// optimistic concurrency detection where NULL is possible.
//
// Expression edits (Kind==Expression) splice NewExpr verbatim into the
// SET clause in both modes — no quoting, no parameter binding.
func BuildUpdateSQL(table models.Ref, pkCols []string, edit models.PendingEdit, mode SQLMode) (string, []any) {
	var b strings.Builder
	var args []any

	b.WriteString("UPDATE ")
	b.WriteString(pg.QuoteQualified(table.Schema, table.Table))
	b.WriteString(" SET ")
	b.WriteString(pg.QuoteIdent(edit.Column))
	b.WriteString(" = ")

	if edit.Kind == models.Expression {
		b.WriteString(edit.NewExpr)
	} else if mode == SQLModeLiteral {
		b.WriteString(pg.QuoteLiteral(edit.NewValue, edit.TypeOID))
	} else {
		args = append(args, edit.NewValue)
		fmt.Fprintf(&b, "$%d", len(args))
	}

	b.WriteString(" WHERE ")
	for i, pkc := range pkCols {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(pg.QuoteIdent(pkc))
		b.WriteString(" = ")
		if mode == SQLModeLiteral {
			b.WriteString(pg.QuoteLiteral(edit.PrimaryKey[i], 0))
		} else {
			args = append(args, edit.PrimaryKey[i])
			fmt.Fprintf(&b, "$%d", len(args))
		}
	}
	b.WriteString(" AND ")
	b.WriteString(pg.QuoteIdent(edit.Column))
	b.WriteString(" IS NOT DISTINCT FROM ")
	if mode == SQLModeLiteral {
		b.WriteString(pg.QuoteLiteral(edit.OldValue, edit.TypeOID))
	} else {
		args = append(args, edit.OldValue)
		fmt.Fprintf(&b, "$%d", len(args))
	}

	return b.String(), args
}
