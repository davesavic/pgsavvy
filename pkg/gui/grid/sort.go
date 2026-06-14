package grid

import (
	"github.com/davesavic/dbsavvy/pkg/models"
)

// sortState carries the in-grid column sort.
//
// dir cycles 0 → +1 → -1 → 0 on successive SetSort calls against the same
// column. A SetSort on a different column resets dir to +1 for that
// column. Sort is reset to {dir: 0} on every SetColumns.
type sortState struct {
	col int // column index; meaningful only when dir != 0
	dir int // 0 = clear, +1 = asc, -1 = desc
}

// active reports whether a sort is currently installed.
func (s sortState) active() bool { return s.dir != 0 }

// Sort direction values exposed as documentation. Not used outside the
// package today; kept as constants so a future external API (e.g. a
// programmatic sort setter) can avoid sentinel ints.
const (
	SortNone = 0
	SortAsc  = 1
	SortDesc = -1
)

// SetSort cycles the sort state on col. The first call for a fresh column
// installs ascending order; the second call on the same column flips to
// descending; the third clears the sort. SetSort on a different column
// always restarts the cycle in ascending order.
//
// col out of range, or an empty buffer, are no-ops (the call still
// advances internal state on a valid col but no rows change).
func (v *View) SetSort(col int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if col < 0 || col >= len(v.cols) {
		return
	}
	if v.sortState.dir == 0 || v.sortState.col != col {
		v.sortState = sortState{col: col, dir: SortAsc}
		return
	}
	switch v.sortState.dir {
	case SortAsc:
		v.sortState.dir = SortDesc
	case SortDesc:
		v.sortState = sortState{}
	}
}

// SetSortIndicator sets the display-only sort indicator (title suffix). Unlike
// SetSort it does not cycle: the caller passes the Tab's authoritative (col,
// dir). dir==SortNone or col out of range clears it. The grid no longer
// reorders rows; ordering is DB-side.
func (v *View) SetSortIndicator(col, dir int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if dir == SortNone || col < 0 || col >= len(v.cols) {
		v.sortState = sortState{}
		return
	}
	v.sortState = sortState{col: col, dir: dir}
}

// SortActive reports whether a sort is currently installed.
func (v *View) SortActive() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.sortState.active()
}

// SortIndicator returns the title-suffix to append when a sort is active,
// e.g. " (sort: name ↑)". Returns "" when no sort is installed or the
// configured column has been removed.
func (v *View) SortIndicator() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return sortIndicatorLocked(v.sortState, v.cols)
}

func sortIndicatorLocked(s sortState, cols []models.ColumnMeta) string {
	if !s.active() {
		return ""
	}
	if s.col < 0 || s.col >= len(cols) {
		return ""
	}
	arrow := "↑"
	if s.dir == SortDesc {
		arrow = "↓"
	}
	return " (sort: " + cols[s.col].Name + " " + arrow + ")"
}
