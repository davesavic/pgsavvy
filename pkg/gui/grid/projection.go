package grid

// project returns the ordered list of row indices into snap.rows that
// should be rendered, after composing filter -> sort -> hide-cols.
//
// Filter is implemented here (dbsavvy-uv0.4). Sort (dbsavvy-uv0.5) and
// hide-cols (dbsavvy-uv0.6) keep their no-op slots so the composition
// pipeline is already in place when those tasks land; T5 and T6 fill in
// their slot without touching the surrounding wiring.
func project(snap viewSnapshot) []int {
	indices := applyFilter(snap)
	indices = applySort(snap, indices)     // T5 placeholder.
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

// applySort is the dbsavvy-uv0.5 (sort) placeholder. T4 leaves the
// row-index list unchanged.
func applySort(_ viewSnapshot, in []int) []int { return in }

// applyHideCols is the dbsavvy-uv0.6 (hide-cols) placeholder. Hide is a
// column-axis operation, not a row-axis one; the slot is kept here for
// symmetry / pipeline composition. T4 leaves the row-index list
// unchanged.
func applyHideCols(_ viewSnapshot, in []int) []int { return in }
