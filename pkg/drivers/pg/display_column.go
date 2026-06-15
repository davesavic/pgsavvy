package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// displayColumnTextTypes are the substrings that mark a column's DataType as
// "text-like" for the display-column heuristic. Matched case-insensitively
// against models.Column.DataType.
var displayColumnTextTypes = []string{"text", "varchar", "character", "name", "char", "bpchar"}

// displayColumnPreferredNames are the column names (lowercased) the heuristic
// prefers, in priority order, when more than one text-like column exists.
var displayColumnPreferredNames = []string{"name", "title", "label"}

// DisplayColumn picks the column whose value best stands in for a row when it
// is previewed in the relationship panel. The heuristic:
//
//  1. Among the NON-primary-key, text-like columns, prefer one literally named
//     "name", then "title", then "label" (first match wins).
//  2. Otherwise the first non-PK text-like column in column order.
//  3. Otherwise (no text-like column) the primary-key column.
//  4. Otherwise the empty string (no usable column).
//
// "text-like" means DataType contains any of text/varchar/character/name/char/
// bpchar (case-insensitive). Columns are considered in the order supplied
// (callers pass ListColumns output, which is position-ordered).
func DisplayColumn(cols []models.Column) string {
	textLike := make([]models.Column, 0, len(cols))
	for _, c := range cols {
		if c.IsPrimaryKey {
			continue
		}
		if isTextLike(c.DataType) {
			textLike = append(textLike, c)
		}
	}

	if name := preferredByName(textLike); name != "" {
		return name
	}
	if len(textLike) > 0 {
		return textLike[0].Name
	}

	for _, c := range cols {
		if c.IsPrimaryKey {
			return c.Name
		}
	}
	return ""
}

// preferredByName returns the first column whose (lowercased) name matches a
// displayColumnPreferredNames entry, honouring the preference order rather
// than column order. Empty string when none match.
func preferredByName(cols []models.Column) string {
	for _, want := range displayColumnPreferredNames {
		for _, c := range cols {
			if strings.EqualFold(c.Name, want) {
				return c.Name
			}
		}
	}
	return ""
}

// isTextLike reports whether a DataType string contains any text-like marker.
func isTextLike(dataType string) bool {
	lower := strings.ToLower(dataType)
	for _, t := range displayColumnTextTypes {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// ErrNoDisplayColumn is returned by ResolveDisplayValue when the referenced
// table exposes no usable display column (no text-like column and no primary
// key). The panel keeps the raw "<col>=<val>" fallback line in that case.
var ErrNoDisplayColumn = errors.New("pg: referenced table has no display column")

// ErrCompositeMismatch is returned when an FK's referencing/referenced column
// lists differ in length — a malformed predicate would result, so the caller
// must fall back to the raw line without issuing a query.
var ErrCompositeMismatch = errors.New("pg: foreign key column lists have mismatched lengths")

// ResolveDisplayValue resolves the parent row's display-column value for one
// outbound foreign key. refValues are the row's FK cell values, paired
// positionally with fk.Columns / fk.RefColumns. It:
//
//  1. lists the referenced table's columns and picks the display column,
//  2. builds "SELECT <disp> FROM <parent> WHERE <refcol>=$1 [AND ...] LIMIT 1"
//     with every identifier quoted and every value parameterized,
//  3. runs it under the supplied statement timeout and returns the single
//     scalar (nil when the parent row is absent).
//
// Returns ErrCompositeMismatch (no query issued) when len(fk.RefColumns) !=
// len(refValues), and ErrNoDisplayColumn when no display column exists. Any
// driver/timeout error propagates; the caller keeps the raw fallback line and
// the panel stays alive. No row VALUES are logged here (they pass through
// $N params only).
func ResolveDisplayValue(ctx context.Context, sess *Session, fk models.ForeignKey, refValues []any, timeout time.Duration) (any, error) {
	if sess == nil {
		return nil, errors.New("pg: display-value resolver has no session")
	}
	if len(fk.RefColumns) != len(refValues) {
		return nil, ErrCompositeMismatch
	}

	cols, err := sess.ListColumns(ctx, fk.RefSchema, fk.RefTable)
	if err != nil {
		return nil, err
	}
	disp := DisplayColumn(cols)
	if disp == "" {
		return nil, ErrNoDisplayColumn
	}

	sql := buildDisplayValueSQL(fk, disp)
	res, err := sess.Execute(ctx, models.Query{SQL: sql, Args: refValues, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 || res.Rows[0] == nil || len(res.Rows[0].Values) == 0 {
		return nil, nil
	}
	return res.Rows[0].Values[0], nil
}

// buildDisplayValueSQL composes the single-scalar lookup against the
// referenced table. Every identifier (schema, table, display column,
// predicate columns) is quoted via QuoteIdent / QuoteQualified; every value
// is bound as a $N parameter — mirrors buildFKForwardSQL (fk_forward.go).
func buildDisplayValueSQL(fk models.ForeignKey, displayCol string) string {
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(QuoteIdent(displayCol))
	b.WriteString(" FROM ")
	b.WriteString(QuoteQualified(fk.RefSchema, fk.RefTable))
	b.WriteString(" WHERE ")
	for i, col := range fk.RefColumns {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(QuoteIdent(col))
		fmt.Fprintf(&b, " = $%d", i+1)
	}
	b.WriteString(" LIMIT 1")
	return b.String()
}
