package grid

// Selection mode entry/exit + range helpers. The vim model:
//
//   - 'v'        → SelectionCell, anchor = cursor at moment of entry
//   - 'V'        → SelectionRow, anchor row = cursor row (col tracks
//                  the full row)
//   - '<C-v>'    → SelectionBlock, anchor = cursor at entry; growing
//                  the cursor in any direction expands the rectangle
//   - <esc>      → ClearSelection (SelectionNone)
//
// All entry methods are idempotent: invoking EnterCellMode while already
// in cell mode just re-anchors at the current cursor (matches vim).

// EnterCellMode flips into single-cell selection mode, anchored at the
// current cursor position.
func (v *View) EnterCellMode() {
	v.mu.Lock()
	v.selMode = SelectionCell
	v.anchorRow = v.cursorRow
	v.anchorCol = v.cursorCol
	v.mu.Unlock()
}

// EnterRowMode flips into row selection mode, anchored at the current
// cursor row. The column anchor is left at 0 because rows are full-width
// selections — Yank ignores anchorCol in row mode.
func (v *View) EnterRowMode() {
	v.mu.Lock()
	v.selMode = SelectionRow
	v.anchorRow = v.cursorRow
	v.anchorCol = 0
	v.mu.Unlock()
}

// EnterBlockMode flips into rectangular block selection mode, anchored
// at the current cursor position. Subsequent cursor moves grow the
// block in any direction. In expanded mode the block model is
// meaningless (one logical column of wrapped lines) so the call falls
// back to SelectionRow per dbsavvy-uv0.7 AC.
func (v *View) EnterBlockMode() {
	v.mu.Lock()
	if normaliseViewMode(v.viewMode) == ViewModeExpanded {
		v.selMode = SelectionRow
		v.anchorRow = v.cursorRow
		v.anchorCol = 0
		v.mu.Unlock()
		return
	}
	v.selMode = SelectionBlock
	v.anchorRow = v.cursorRow
	v.anchorCol = v.cursorCol
	v.mu.Unlock()
}

// ClearSelection returns to SelectionNone. The cursor is unaffected.
func (v *View) ClearSelection() {
	v.mu.Lock()
	v.selMode = SelectionNone
	v.mu.Unlock()
}

// SelectionMode returns the active mode. Useful for status-bar
// indicators and unit tests.
func (v *View) SelectionMode() SelectionMode {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.selMode
}

// selectionRange returns the inclusive (rowStart, colStart, rowEnd,
// colEnd) bounds of the current selection, or all-zero + false when
// no selection is active. Computed under the supplied snapshot so it
// stays consistent with whatever frame is being rendered/yanked.
func selectionRange(snap viewSnapshot) (rowStart, colStart, rowEnd, colEnd int, ok bool) {
	switch snap.selMode {
	case SelectionNone:
		// No-selection collapses to "the cell under the cursor" for
		// yank purposes. Callers that want true "nothing selected"
		// inspect ok==false; the returned coords still describe the
		// cursor cell so a non-mode-aware caller can use them as a
		// single-cell range.
		return snap.cursorRow, snap.cursorCol, snap.cursorRow, snap.cursorCol, false
	case SelectionCell, SelectionBlock:
		r0, r1 := orderInts(snap.anchorRow, snap.cursorRow)
		c0, c1 := orderInts(snap.anchorCol, snap.cursorCol)
		return r0, c0, r1, c1, true
	case SelectionRow:
		r0, r1 := orderInts(snap.anchorRow, snap.cursorRow)
		// Column span is the entire row.
		c0 := 0
		c1 := len(snap.cols) - 1
		if c1 < 0 {
			c1 = 0
		}
		return r0, c0, r1, c1, true
	default:
		return 0, 0, 0, 0, false
	}
}

func orderInts(a, b int) (lo, hi int) {
	if a <= b {
		return a, b
	}
	return b, a
}

// inSelection reports whether the given (row, col) is inside the
// snapshot's selection rectangle. For row mode the column is ignored.
// For SelectionNone, only the exact cursor cell returns true.
func inSelection(snap viewSnapshot, row, col int) bool {
	r0, c0, r1, c1, ok := selectionRange(snap)
	if !ok {
		// No active selection: highlight just the cursor cell.
		return row == snap.cursorRow && col == snap.cursorCol
	}
	if row < r0 || row > r1 {
		return false
	}
	if snap.selMode == SelectionRow {
		return true
	}
	return col >= c0 && col <= c1
}
