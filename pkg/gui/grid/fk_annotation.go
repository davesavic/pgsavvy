package grid

import (
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fkHeaderPrefix is the glyph (U+2192 RIGHTWARDS ARROW + space) prepended to
// the header of any result-set column that participates in a foreign-key
// constraint owned by the result's base table. The prefix is folded into
// both the auto-size width computation (see columns.go::autoSizeFromSampleLocked)
// and the rendered header line (see scroll.go::renderHeaderLine) so the
// indicator never causes the header to overflow the locked column width.
const fkHeaderPrefix = "→ "

// PopulateForeignKeyFlags returns a copy of cols with ColumnMeta.IsForeignKey
// set to true for every column whose Name appears in fks where the FK is
// owned by baseTable (FK.Schema == baseTable.Schema && FK.Table == baseTable.Name).
//
// Composite foreign keys flag every participating column. Self-referencing
// foreign keys (FK.RefSchema/RefTable == baseTable) still match on the owning
// side: a self-FK's Columns slice lists the referencing column on baseTable,
// which is exactly what we want flagged.
//
// An empty fks slice, or a baseTable with empty Schema and Table, results in
// no flags being set (the input columns are returned with IsForeignKey reset
// to false — the caller passes pre-introspection ColumnMeta whose flag may
// carry stale state from a prior schema). Columns absent from any matching
// FK retain IsForeignKey=false.
//
// The input slice is not mutated; a fresh []models.ColumnMeta is returned so
// the caller can install it via View.SetColumns without aliasing concerns.
func PopulateForeignKeyFlags(cols []models.ColumnMeta, fks []models.ForeignKey, baseTable models.Ref) []models.ColumnMeta {
	out := make([]models.ColumnMeta, len(cols))
	copy(out, cols)
	for i := range out {
		out[i].IsForeignKey = false
	}
	if len(out) == 0 || (baseTable.Schema == "" && baseTable.Table == "") {
		return out
	}
	// Build a set of FK-participating column names for the owning table.
	// Membership testing per result-set column is O(1) regardless of FK count.
	fkCols := make(map[string]struct{})
	for _, fk := range fks {
		if fk.Schema != baseTable.Schema || fk.Table != baseTable.Table {
			continue
		}
		for _, c := range fk.Columns {
			fkCols[c] = struct{}{}
		}
	}
	if len(fkCols) == 0 {
		return out
	}
	for i := range out {
		if _, ok := fkCols[out[i].Name]; ok {
			out[i].IsForeignKey = true
		}
	}
	return out
}

// headerLabel returns the string used to render col's header. For FK columns
// the label is fkHeaderPrefix + col.Name; otherwise just col.Name. Used by
// the auto-size seed (columns.go) and the header line renderer (scroll.go)
// so width budget and visible text stay in lock-step.
func headerLabel(col models.ColumnMeta) string {
	if col.IsForeignKey {
		return fkHeaderPrefix + col.Name
	}
	return col.Name
}
