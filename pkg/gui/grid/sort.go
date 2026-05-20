package grid

import (
	"sort"
	"strconv"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// sortState carries the in-grid column sort (dbsavvy-uv0.5).
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

// pg type-OID constants used by the comparator. Hard-coded so the grid
// package doesn't import pgx/pgtype (the grid is meant to be driver-
// agnostic). For non-pg drivers TypeOID will be 0 (or an unknown value)
// and the comparator falls back to lexicographic string compare on the
// rendered cell.
const (
	pgOIDInt2        = 21
	pgOIDInt4        = 23
	pgOIDInt8        = 20
	pgOIDFloat4      = 700
	pgOIDFloat8      = 701
	pgOIDNumeric     = 1700
	pgOIDText        = 25
	pgOIDVarchar     = 1043
	pgOIDBPChar      = 1042
	pgOIDTimestamp   = 1114
	pgOIDTimestamptz = 1184
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

// applySort reorders the supplied row indices according to the snapshot's
// active sort. When no sort is active in is returned unchanged. Sort is
// stable: rows that compare equal preserve their relative order, so the
// rendered output is deterministic even with many NULL or equal keys.
func applySort(snap viewSnapshot, in []int) []int {
	if !snap.sortActive {
		return in
	}
	if snap.sortCol < 0 || snap.sortCol >= len(snap.cols) {
		return in
	}
	if len(in) < 2 {
		return in
	}
	col := snap.sortCol
	dir := snap.sortDir
	cmp := comparatorFor(snap.cols[col])
	out := make([]int, len(in))
	copy(out, in)
	sort.SliceStable(out, func(a, b int) bool {
		ra := snap.rows[out[a]]
		rb := snap.rows[out[b]]
		var va, vb any
		if col < len(ra.Values) {
			va = ra.Values[col]
		}
		if col < len(rb.Values) {
			vb = rb.Values[col]
		}
		ord := cmp(va, vb, snap.cols[col])
		if dir == SortDesc {
			ord = -ord
		}
		return ord < 0
	})
	return out
}

// comparator returns -1, 0, +1 for a < b, ==, > respectively. NULL
// ordering matches PostgreSQL's default: NULL is "greater than" any
// non-NULL value in asc order (i.e. appears at the bottom).
type comparator func(a, b any, col models.ColumnMeta) int

// comparatorFor picks the comparator for a column based on its TypeOID.
// Unknown OIDs collapse to string compare on the rendered cell so non-
// pg drivers still get a deterministic order.
func comparatorFor(col models.ColumnMeta) comparator {
	switch col.TypeOID {
	case pgOIDInt2, pgOIDInt4, pgOIDInt8:
		return compareInt
	case pgOIDFloat4, pgOIDFloat8, pgOIDNumeric:
		return compareFloat
	case pgOIDTimestamp, pgOIDTimestamptz:
		return compareTime
	case pgOIDText, pgOIDVarchar, pgOIDBPChar:
		return compareString
	default:
		return compareString
	}
}

// nullOrder handles NULL placement uniformly: NULL > any non-NULL. Returns
// (cmp, true) when at least one side was NULL; (_, false) when both are
// non-NULL and the type-specific path should take over.
func nullOrder(a, b any) (int, bool) {
	aNil := a == nil
	bNil := b == nil
	if aNil && bNil {
		return 0, true
	}
	if aNil {
		return 1, true
	}
	if bNil {
		return -1, true
	}
	return 0, false
}

func compareInt(a, b any, col models.ColumnMeta) int {
	if c, isNull := nullOrder(a, b); isNull {
		return c
	}
	ai, aok := toInt64(a)
	bi, bok := toInt64(b)
	if !aok || !bok {
		return compareString(a, b, col)
	}
	switch {
	case ai < bi:
		return -1
	case ai > bi:
		return 1
	default:
		return 0
	}
}

func compareFloat(a, b any, col models.ColumnMeta) int {
	if c, isNull := nullOrder(a, b); isNull {
		return c
	}
	af, aok := toFloat64(a)
	bf, bok := toFloat64(b)
	if !aok || !bok {
		return compareString(a, b, col)
	}
	switch {
	case af < bf:
		return -1
	case af > bf:
		return 1
	default:
		return 0
	}
}

func compareTime(a, b any, col models.ColumnMeta) int {
	if c, isNull := nullOrder(a, b); isNull {
		return c
	}
	at, aok := a.(time.Time)
	bt, bok := b.(time.Time)
	if !aok || !bok {
		return compareString(a, b, col)
	}
	switch {
	case at.Before(bt):
		return -1
	case at.After(bt):
		return 1
	default:
		return 0
	}
}

func compareString(a, b any, col models.ColumnMeta) int {
	if c, isNull := nullOrder(a, b); isNull {
		return c
	}
	as := renderCellPlain(a, col)
	bs := renderCellPlain(b, col)
	switch {
	case as < bs:
		return -1
	case as > bs:
		return 1
	default:
		return 0
	}
}

// toInt64 best-effort converts a driver-returned value into an int64.
// Handles every Go integer type, plus the string-form integers that
// numeric / decimal drivers sometimes return.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		if err == nil {
			return i, true
		}
	}
	return 0, false
}

// toFloat64 best-effort converts a driver-returned numeric / float into a
// float64. Falls through to string parsing so pg numerics arriving as
// strings still sort numerically.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}
