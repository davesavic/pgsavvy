package grid

import (
	"reflect"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// dirtyCellMarker is the single-character glyph appended to a cell that
// carries a staged edit. The bullet sits flush against the value with no
// separator so it stays visually attached. dbsavvy-bwq.6 (A3).
const dirtyCellMarker = "●"

// gutterModifiedMarker is the single-character glyph rendered in the row
// gutter for any row that has at least one staged edit. Mirrors the vim
// `:set list`-style modified marker. dbsavvy-bwq.6 (A3).
const gutterModifiedMarker = "M"

// DecorateDirtyCell returns the cell's display string with a trailing dirty
// marker (`●`) appended and wrapped in the DirtyCellBg theme style when
// isDirty is true. When isDirty is false the value is returned unchanged
// — callers do not need to branch.
//
// The style argument is the dereferenced *theme.Style for DirtyCellBg
// (typically theme.Current().DirtyCellBg). The zero Style yields no ANSI
// escapes, so a theme that hasn't wired DirtyCellBg yet still produces the
// `value●` marker without colour decoration. The cell-style stack already
// applies type-aware foreground colouring upstream; this decorator only
// adds the dirty-state tint + glyph so callers can layer it on top of the
// already-styled value. dbsavvy-bwq.6 (A3).
func DecorateDirtyCell(value string, isDirty bool, style theme.Style) string {
	if !isDirty {
		return value
	}
	return wrapWithStyle(value+dirtyCellMarker, style)
}

// RowHasPendingEdit reports whether any edit in set matches the supplied
// row primary key. A nil set or empty rowPK returns false; nil rowPK is
// treated as "row identity unavailable" so the caller renders no gutter
// marker rather than matching every edit with a zero-length PK (which Add
// rejects, so this is defence-in-depth). dbsavvy-bwq.6 (A3).
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
// dbsavvy-bwq.6 (A3).
func CellHasPendingEdit(set *models.PendingEditSet, rowPK []any, column string) bool {
	if set == nil || len(rowPK) == 0 || column == "" || set.IsEmpty() {
		return false
	}
	for _, e := range set.Edits() {
		if e.Column == column && pkSliceEqual(e.PrimaryKey, rowPK) {
			return true
		}
	}
	return false
}

// GutterMarker returns the gutter glyph to render alongside rowIdx. When
// any edit in set carries a PrimaryKey matching rowPK, the modified
// marker ("M") is returned; otherwise the empty string. The rowIdx
// parameter is reserved for callers that want to disambiguate by buffer
// index in the future; the current implementation matches on rowPK only.
// dbsavvy-bwq.6 (A3).
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
// dbsavvy-bwq.6 (A3).
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
