package grid

// project returns the ordered list of row indices into snap.rows that
// should be rendered, after composing filter -> sort -> hide-cols.
//
// Filter is implemented in filter.go (dbsavvy-uv0.4); sort is implemented
// in sort.go (dbsavvy-uv0.5); hide-cols (dbsavvy-uv0.6) keeps its no-op
// slot here for symmetry — the hide operation is on the column axis, not
// the row axis, so it returns the row-index list unchanged.
func project(snap viewSnapshot) []int {
	indices := applyFilter(snap)
	indices = applySort(snap, indices)
	indices = applyHideCols(snap, indices) // T6 placeholder (row-axis no-op).
	return indices
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
