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
	return buildUpdateSQL(table, pkCols, edit, mode, true)
}

func buildUpdateSQL(table models.Ref, pkCols []string, edit models.PendingEdit, mode SQLMode, guardOldValue bool) (string, []any) {
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
	if !guardOldValue {
		return b.String(), args
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

// RowUpdateStatement updates all staged columns of one original row atomically.
// EditIndexes maps its affected-row count back to the input edit snapshot.
type RowUpdateStatement struct {
	SQL         string
	Args        []any
	EditIndexes []int
	OriginalPK  []any
}

// BuildUpdateBatch is shared by execution and preview. SET expressions read the
// original row. Only an explicitly authorized overwrite may omit old-value guards.
func BuildUpdateBatch(table models.Ref, pkCols []string, edits []models.PendingEdit, mode SQLMode, guardOldValue bool) ([]RowUpdateStatement, error) {
	if len(pkCols) == 0 {
		return nil, ErrMissingPKColumns
	}
	groups := make([]RowUpdateStatement, 0)
	for i, e := range edits {
		if len(e.PrimaryKey) != len(pkCols) {
			return nil, ErrPKLengthMismatch
		}
		found := -1
		for j := range groups {
			if pkSliceEqual(groups[j].OriginalPK, e.PrimaryKey) {
				found = j
				break
			}
		}
		if found < 0 {
			groups = append(groups, RowUpdateStatement{OriginalPK: append([]any(nil), e.PrimaryKey...)})
			found = len(groups) - 1
		}
		for _, j := range groups[found].EditIndexes {
			if edits[j].Column == e.Column {
				return nil, fmt.Errorf("cell apply: duplicate staged column %q", e.Column)
			}
		}
		groups[found].EditIndexes = append(groups[found].EditIndexes, i)
	}
	for gi := range groups {
		g := &groups[gi]
		var b strings.Builder
		value := func(v any, oid uint32) {
			if mode == SQLModeLiteral {
				b.WriteString(pg.QuoteLiteral(v, oid))
			} else {
				g.Args = append(g.Args, v)
				fmt.Fprintf(&b, "$%d", len(g.Args))
			}
		}
		b.WriteString("UPDATE " + pg.QuoteQualified(table.Schema, table.Table) + " SET ")
		for j, i := range g.EditIndexes {
			if j > 0 {
				b.WriteString(", ")
			}
			e := edits[i]
			b.WriteString(pg.QuoteIdent(e.Column) + " = ")
			if e.Kind == models.Expression {
				b.WriteString(e.NewExpr)
			} else {
				value(e.NewValue, e.TypeOID)
			}
		}
		b.WriteString(" WHERE ")
		for j, col := range pkCols {
			if j > 0 {
				b.WriteString(" AND ")
			}
			b.WriteString(pg.QuoteIdent(col) + " = ")
			value(g.OriginalPK[j], 0)
		}
		if guardOldValue {
			for _, i := range g.EditIndexes {
				e := edits[i]
				b.WriteString(" AND " + pg.QuoteIdent(e.Column) + " IS NOT DISTINCT FROM ")
				value(e.OldValue, e.TypeOID)
			}
		}
		b.WriteString(" RETURNING ")
		for j, col := range pkCols {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pg.QuoteIdent(col))
		}
		g.SQL = b.String()
	}
	return groups, nil
}
