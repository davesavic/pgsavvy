package grid

// ansiYankFlashBg is the transient post-yank highlight background — yellow,
// matching the editor's on_yank flash (selection_render.go ansiYankBgOn) so
// a yank looks identical whether it fires in the SQL editor or the result
// grid.
const ansiYankFlashBg = "\x1b[43m"

// yankFlashRange is the transient post-yank highlight rectangle in raw
// row/col index space. wholeRow ignores the column bounds (a row yank tints
// every column of the row regardless of horizontal scroll).
type yankFlashRange struct {
	rowStart, colStart, rowEnd, colEnd int
	wholeRow                           bool
}

// FlashYankCell arms the post-yank highlight over the focused cell and
// returns the epoch identifying this flash; the caller passes it back to
// ClearYankFlash after the flash TTL. Returns 0 (no-op) on an empty grid so
// the caller can skip scheduling a clear.
func (v *View) FlashYankCell() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.rows) == 0 || len(v.cols) == 0 {
		return 0
	}
	return v.setYankFlashLocked(yankFlashRange{
		rowStart: v.cursorRow, colStart: v.cursorCol,
		rowEnd: v.cursorRow, colEnd: v.cursorCol,
	})
}

// FlashYankRow arms the post-yank highlight over every column of the focused
// row and returns the epoch. Returns 0 (no-op) on an empty grid.
func (v *View) FlashYankRow() uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.rows) == 0 || len(v.cols) == 0 {
		return 0
	}
	return v.setYankFlashLocked(yankFlashRange{
		rowStart: v.cursorRow, rowEnd: v.cursorRow, wholeRow: true,
	})
}

// FlashYankSelection arms the post-yank highlight over rows
// startRow..endRow (all columns) and returns the epoch. Returns 0
// (no-op) on an empty grid.
func (v *View) FlashYankSelection(startRow, endRow int) uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.rows) == 0 || len(v.cols) == 0 {
		return 0
	}
	return v.setYankFlashLocked(yankFlashRange{
		rowStart: startRow, rowEnd: endRow, wholeRow: true,
	})
}

// setYankFlashLocked stores r and bumps the epoch. Caller holds v.mu.
func (v *View) setYankFlashLocked(r yankFlashRange) uint64 {
	cp := r
	v.yankFlash = &cp
	v.yankFlashEpoch++
	return v.yankFlashEpoch
}

// ClearYankFlash drops the post-yank highlight, but only when epoch still
// matches the active flash. A later yank that re-armed the flash bumps the
// epoch, so an earlier flash's in-flight clear becomes a no-op (stale-timer
// guard, mirrors editor.Buffer.ClearYankFlash).
func (v *View) ClearYankFlash(epoch uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if epoch != v.yankFlashEpoch {
		return
	}
	v.yankFlash = nil
}

// copyYankFlash returns a defensive copy so the render snapshot can't tear
// against a concurrent SetYankFlash / ClearYankFlash.
func copyYankFlash(f *yankFlashRange) *yankFlashRange {
	if f == nil {
		return nil
	}
	cp := *f
	return &cp
}

// inYankFlash reports whether (row, col) sits inside the snapshot's active
// post-yank highlight. wholeRow ignores the column bounds.
func inYankFlash(snap viewSnapshot, row, col int) bool {
	f := snap.yankFlash
	if f == nil {
		return false
	}
	if row < f.rowStart || row > f.rowEnd {
		return false
	}
	if f.wholeRow {
		return true
	}
	return col >= f.colStart && col <= f.colEnd
}

// applyYankFlashHighlight tints an already-decorated cell with the yellow
// post-yank background. Mirrors applySelectionHighlight's wrap-the-whole-cell
// approach (the styled cell is prefix+content+reset, so the single trailing
// reset bounds the tint to exactly this cell's width).
func applyYankFlashHighlight(decorated string) string {
	return ansiYankFlashBg + decorated + ansiReset
}
