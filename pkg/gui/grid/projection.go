package grid

// project returns the ordered list of row indices into snap.rows that
// should be rendered, after composing filter -> hide-cols.
//
// Filter is implemented in filter.go (dbsavvy-uv0.4); hide-cols
// (dbsavvy-uv0.6) keeps its no-op slot here for symmetry — the hide
// operation is on the column axis, not the row axis, so it returns the
// row-index list unchanged. The grid no longer reorders rows for sort;
// ordering is DB-side (dbsavvy-72k.6).
func project(snap viewSnapshot) []int {
	indices := applyFilter(snap)
	indices = applyHideCols(snap, indices) // T6 placeholder (row-axis no-op).
	return indices
}

// projectionLocked returns the projected row-index list (filter -> hide)
// for the View's current state. The caller MUST already hold v.mu
// (read or write). It feeds project() the same projection inputs snapshot()
// hands Render, so cursor navigation walks the exact row order the user
// sees on screen. Centralising the inputs here is what keeps cursorRow (a
// raw-buffer index) and the rendered viewport from drifting apart under an
// active filter. dbsavvy-dr6.
func (v *View) projectionLocked() []int {
	return project(viewSnapshot{
		rows:          v.rows,
		cols:          v.cols,
		cursorCol:     v.cursorCol,
		filterRe:      v.filterState.re,
		filterAllCols: v.filterState.allCols,
		filterActive:  v.filterState.re != nil,
	})
}

// projectedPos returns the position of raw row index rawRow within the
// projected list proj, or -1 when rawRow is absent (e.g. filtered out).
// dbsavvy-dr6.
func projectedPos(proj []int, rawRow int) int {
	for p, raw := range proj {
		if raw == rawRow {
			return p
		}
	}
	return -1
}

// applyFilter returns the row indices that match the snapshot's filter
// state. When no filter is active the identity index slice is returned.
func applyFilter(snap viewSnapshot) []int {
	if !snap.filterActive || snap.filterRe == nil {
		out := make([]int, len(snap.rows))
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0, len(snap.rows))
	for i, row := range snap.rows {
		if rowMatchesLocked(row, snap.cols, snap.filterRe, snap.filterAllCols, snap.cursorCol) {
			out = append(out, i)
		}
	}
	return out
}

// applyHideCols is the dbsavvy-uv0.6 (hide-cols) placeholder. Hide is a
// column-axis operation, not a row-axis one; the slot is kept here for
// symmetry / pipeline composition. T4 leaves the row-index list
// unchanged.
func applyHideCols(_ viewSnapshot, in []int) []int { return in }
