package grid

import (
	"reflect"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// gutterModifiedMarker is the single-character glyph rendered in the row
// gutter for any row that has at least one staged edit. Mirrors the vim
// `:set list`-style modified marker.
const gutterModifiedMarker = "M"

// DecorateDirtyCell returns the cell's display string wrapped in the
// DirtyCellBg theme style when isDirty is true. When isDirty is false the
// value is returned unchanged — callers do not need to branch.
//
// The style argument is the dereferenced *theme.Style for DirtyCellBg
// (typically theme.Current().DirtyCellBg). The zero Style yields no ANSI
// escapes, so a theme that hasn't wired DirtyCellBg yet returns the value
// untouched. The cell-style stack already applies type-aware foreground
// colouring upstream; this decorator only layers the dirty-state
// background tint on top of the already-styled value. The edit is signalled
// by background colour alone — no per-cell glyph.
func DecorateDirtyCell(value string, isDirty bool, style theme.Style) string {
	if !isDirty {
		return value
	}
	return wrapWithStyle(value, style)
}

// RowHasPendingEdit reports whether any edit in set matches the supplied
// row primary key. A nil set or empty rowPK returns false; nil rowPK is
// treated as "row identity unavailable" so the caller renders no gutter
// marker rather than matching every edit with a zero-length PK (which Add
// rejects, so this is defence-in-depth).
func RowHasPendingEdit(set *models.PendingEditSet, rowPK []any) bool {
	if set == nil || len(rowPK) == 0 || set.IsEmpty() {
		return false
	}
	for _, e := range set.Edits() {
		if pkSliceEqual(e.PrimaryKey, rowPK) {
			return true
		}
	}
	return false
}

// CellHasPendingEdit reports whether set carries a staged edit for the
// (rowPK, column) pair. Used by the per-cell render path to decide
// whether DecorateDirtyCell should apply the dirty tint + glyph.
func CellHasPendingEdit(set *models.PendingEditSet, rowPK []any, column string) bool {
	_, ok := cellPendingEdit(set, rowPK, column)
	return ok
}

// cellPendingEdit returns the staged edit for the (rowPK, column) pair and
// true when one exists. The per-cell render path uses it to substitute the
// staged NewValue for the stale DB value so an unsaved edit is visible.
// A nil/empty set, empty rowPK, or empty column yields
// (zero, false).
func cellPendingEdit(set *models.PendingEditSet, rowPK []any, column string) (models.PendingEdit, bool) {
	if set == nil || len(rowPK) == 0 || column == "" || set.IsEmpty() {
		return models.PendingEdit{}, false
	}
	for _, e := range set.Edits() {
		if e.Column == column && pkSliceEqual(e.PrimaryKey, rowPK) {
			return e, true
		}
	}
	return models.PendingEdit{}, false
}

// rowPKValues extracts the primary-key value slice for a row using the
// SELECT-order identity column indexes. Returns nil when any index is out
// of range (treated as "row identity unavailable" — the caller then renders
// no dirty decoration rather than risk a bad match).
func rowPKValues(row models.Row, rowIdentity []int) []any {
	if len(rowIdentity) == 0 {
		return nil
	}
	pk := make([]any, len(rowIdentity))
	for i, idx := range rowIdentity {
		if idx < 0 || idx >= len(row.Values) {
			return nil
		}
		pk[i] = row.Values[idx]
	}
	return pk
}

// GutterMarker returns the gutter glyph to render alongside rowIdx. When
// any edit in set carries a PrimaryKey matching rowPK, the modified
// marker ("M") is returned; otherwise the empty string. The rowIdx
// parameter is reserved for callers that want to disambiguate by buffer
// index in the future; the current implementation matches on rowPK only.
func GutterMarker(rowIdx int, set *models.PendingEditSet, rowPK []any) string {
	_ = rowIdx
	if RowHasPendingEdit(set, rowPK) {
		return gutterModifiedMarker
	}
	return ""
}

// pkSliceEqual compares two primary-key value slices for equality.
// Mirrors models.pkEqual (which is unexported); duplicated here to keep
// the grid package decoupled from internal model helpers.
func pkSliceEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
