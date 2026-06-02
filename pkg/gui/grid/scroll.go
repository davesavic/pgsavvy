package grid

import "strings"

// Cursor motion + viewport scrolling. The View exposes imperative
// per-step methods (MoveCursorDown / MoveCursorUp / etc) plus half-page
// and jump variants. Keymap wiring lives in 66p.11/66p.12; this file
// just ships the verbs.
//
// Selection follows the cursor: in any non-None mode the anchor stays
// fixed and the selection range expands/contracts as the cursor moves.
// The cursor is bounded by [0, len(rows)) × [0, len(cols)); attempts
// to move past the edge are clamped silently.

// MoveCursorDown advances the cursor by one row, clamped to the last
// loaded row. Triggers an auto-prefetch check at the end (handled
// inside the next Render). In expanded mode this means "next record"
// (records == rows in the projected list). The wrapped-line offset is
// reset so the new record starts at the top.
func (v *View) MoveCursorDown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	// Walk the projected (visible) order, not the raw buffer, so j moves
	// to the row rendered just below the cursor even when a sort/filter
	// has reordered the buffer. cursorRow stays a raw-buffer index.
	// dbsavvy-dr6.
	proj := v.projectionLocked()
	if len(proj) == 0 {
		return
	}
	switch pos := projectedPos(proj, v.cursorRow); {
	case pos < 0:
		v.cursorRow = proj[0] // cursor row not visible (filtered out)
	case pos < len(proj)-1:
		v.cursorRow = proj[pos+1]
	default:
		return // already on the last projected row
	}
	v.expandedLineOffset = 0
}

// MoveCursorUp moves the cursor up by one row, clamped to the first
// projected row. In expanded mode this means "previous record"; wrapped-
// line offset is reset so the prior record starts at the top.
func (v *View) MoveCursorUp() {
	v.mu.Lock()
	defer v.mu.Unlock()
	proj := v.projectionLocked()
	if len(proj) == 0 {
		return
	}
	switch pos := projectedPos(proj, v.cursorRow); {
	case pos < 0:
		v.cursorRow = proj[0] // cursor row not visible (filtered out)
	case pos > 0:
		v.cursorRow = proj[pos-1]
	default:
		return // already on the first projected row
	}
	v.expandedLineOffset = 0
}

// nextVisibleColLocked returns the nearest non-hidden column index
// strictly in direction dir (+1 right, -1 left) from `from`, or -1 when
// none exists. Cursor motion runs in raw-index space but hidden columns
// are filtered out of the render (visibleColumnOrder), so stepping by a
// raw ±1 can park the cursor on a hidden column where it renders
// invisibly. This skips the hidden run instead. Caller holds v.mu.
func (v *View) nextVisibleColLocked(from, dir int) int {
	for c := from + dir; c >= 0 && c < len(v.cols); c += dir {
		if !v.hiddenColSet[c] {
			return c
		}
	}
	return -1
}

// snapCursorOffHiddenLocked moves the cursor to the nearest visible
// column (preferring the right) when it currently sits on a hidden one.
// No-op when the cursor is already on a visible column. Caller holds
// v.mu.
func (v *View) snapCursorOffHiddenLocked() {
	if !v.hiddenColSet[v.cursorCol] {
		return
	}
	if c := v.nextVisibleColLocked(v.cursorCol, +1); c >= 0 {
		v.cursorCol = c
		return
	}
	if c := v.nextVisibleColLocked(v.cursorCol, -1); c >= 0 {
		v.cursorCol = c
	}
}

// MoveCursorLeft moves the cursor to the nearest visible column to the
// left, skipping hidden columns. Clamped to the first visible column;
// does not wrap.
func (v *View) MoveCursorLeft() {
	v.mu.Lock()
	if c := v.nextVisibleColLocked(v.cursorCol, -1); c >= 0 {
		v.cursorCol = c
	}
	v.mu.Unlock()
}

// MoveCursorRight moves the cursor to the nearest visible column to the
// right, skipping hidden columns. Clamped to the last visible column.
func (v *View) MoveCursorRight() {
	v.mu.Lock()
	if c := v.nextVisibleColLocked(v.cursorCol, +1); c >= 0 {
		v.cursorCol = c
	}
	v.mu.Unlock()
}

// HalfPageDown jumps the cursor down by half the typical page (treated
// as half of ResultPageSize rows for the no-viewport case). The Render
// pass clamps scroll afterwards so the cursor stays on screen. In
// expanded mode this scrolls inside the active record by a half-page
// of wrapped lines; when the line offset overruns the record's content
// the cursor advances to the next record.
func (v *View) HalfPageDown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if normaliseViewMode(v.viewMode) == ViewModeExpanded {
		// Step inside the current record by half a viewport's worth.
		step := expandedHalfPageStep
		v.expandedLineOffset += step
		return
	}
	step := ResultPageSize / 2
	if step < 1 {
		step = 1
	}
	// Step half a page through the projected order so the move tracks
	// what's on screen under an active sort/filter. dbsavvy-dr6.
	proj := v.projectionLocked()
	if len(proj) == 0 {
		return
	}
	pos := projectedPos(proj, v.cursorRow)
	if pos < 0 {
		pos = 0
	}
	target := pos + step
	if target > len(proj)-1 {
		target = len(proj) - 1
	}
	v.cursorRow = proj[target]
}

// HalfPageUp is the symmetric counterpart of HalfPageDown.
func (v *View) HalfPageUp() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if normaliseViewMode(v.viewMode) == ViewModeExpanded {
		step := expandedHalfPageStep
		v.expandedLineOffset -= step
		if v.expandedLineOffset < 0 {
			v.expandedLineOffset = 0
		}
		return
	}
	step := ResultPageSize / 2
	if step < 1 {
		step = 1
	}
	proj := v.projectionLocked()
	if len(proj) == 0 {
		return
	}
	pos := projectedPos(proj, v.cursorRow)
	if pos < 0 {
		pos = 0
	}
	target := pos - step
	if target < 0 {
		target = 0
	}
	v.cursorRow = proj[target]
}

// expandedHalfPageStep is the half-page distance in wrapped lines used
// by HalfPageDown / HalfPageUp when the view is in expanded mode.
// Chosen to roughly match a typical 24-line viewport / 2.
const expandedHalfPageStep = 12

// WrappedLineDown advances the wrapped-line cursor inside the active
// record by one line (J chord in expanded mode). In grid mode this is
// a no-op so callers can wire the chord unconditionally without checking
// viewMode at the dispatch site. dbsavvy-uv0.7.
func (v *View) WrappedLineDown() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if normaliseViewMode(v.viewMode) != ViewModeExpanded {
		return
	}
	v.expandedLineOffset++
}

// WrappedLineUp is the symmetric counterpart of WrappedLineDown. K
// chord in expanded mode. dbsavvy-uv0.7.
func (v *View) WrappedLineUp() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if normaliseViewMode(v.viewMode) != ViewModeExpanded {
		return
	}
	if v.expandedLineOffset > 0 {
		v.expandedLineOffset--
	}
}

// HorizScrollLeft scrolls the viewport one column-step to the left.
// In expanded mode this shifts the value-column horizontal offset; in
// grid mode it walks the cursor left (mirrors MoveCursorLeft). The
// expanded-mode line offset is reused as a 1-D scroll because expanded
// view only ever has one logical column of content. dbsavvy-uv0.7.
func (v *View) HorizScrollLeft() {
	if v.ViewMode() == ViewModeGrid {
		v.MoveCursorLeft()
		return
	}
	v.mu.Lock()
	if v.colOffset > 0 {
		v.colOffset--
	}
	v.mu.Unlock()
}

// HorizScrollRight scrolls the viewport one column-step to the right.
// dbsavvy-uv0.7.
func (v *View) HorizScrollRight() {
	if v.ViewMode() == ViewModeGrid {
		v.MoveCursorRight()
		return
	}
	v.mu.Lock()
	v.colOffset++
	v.mu.Unlock()
}

// JumpColFirst moves the cursor to the first column (0). In expanded
// mode it resets the 1-D horizontal scroll, mirroring HorizScrollLeft.
// dbsavvy-2fq.
func (v *View) JumpColFirst() {
	if v.ViewMode() != ViewModeGrid {
		v.mu.Lock()
		v.colOffset = 0
		v.mu.Unlock()
		return
	}
	v.mu.Lock()
	// First *visible* column: start before col 0 and step right past any
	// hidden leading columns so the cursor never lands on a hidden one.
	if c := v.nextVisibleColLocked(-1, +1); c >= 0 {
		v.cursorCol = c
	}
	v.mu.Unlock()
}

// JumpColLast moves the cursor to the last visible column ($). It is
// a no-op in expanded mode, which has a single logical column. dbsavvy-2fq.
func (v *View) JumpColLast() {
	if v.ViewMode() != ViewModeGrid {
		return
	}
	v.mu.Lock()
	// Last *visible* column: start past the end and step left past any
	// hidden trailing columns.
	if c := v.nextVisibleColLocked(len(v.cols), -1); c >= 0 {
		v.cursorCol = c
	}
	v.mu.Unlock()
}

// JumpFirst moves the cursor to row 0 (gg). In expanded mode this also
// resets the wrapped-line offset so the first record starts at the top.
func (v *View) JumpFirst() {
	v.mu.Lock()
	// Land on the first projected (visible) row so gg goes to the top of
	// what's on screen, not raw row 0, under an active sort. dbsavvy-dr6.
	if proj := v.projectionLocked(); len(proj) > 0 {
		v.cursorRow = proj[0]
	} else {
		v.cursorRow = 0
	}
	v.expandedLineOffset = 0
	v.mu.Unlock()
}

// JumpLast moves the cursor to the last loaded row (G in grid mode; in
// expanded mode the result-tab controller rebinds G to this method
// instead of ReadToEnd per AD-14). Auto-prefetch is considered on the
// next Render.
func (v *View) JumpLast() {
	v.mu.Lock()
	// Land on the last projected (visible) row, not raw row len-1, so G
	// goes to the bottom of what's on screen under an active sort.
	// dbsavvy-dr6.
	if proj := v.projectionLocked(); len(proj) > 0 {
		v.cursorRow = proj[len(proj)-1]
	}
	v.expandedLineOffset = 0
	v.mu.Unlock()
}

// CursorPosition returns the current (row, col). Read-only; useful
// for tests and status-bar indicators.
func (v *View) CursorPosition() (row, col int) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.cursorRow, v.cursorCol
}

// SetCursor positions the cursor at (row, col), clamping each axis to
// the loaded data range. Out-of-range values clamp to the nearest valid
// cell rather than failing — callers (jump-back, FK navigation) can
// hand in stale entries against a tab whose buffer has since shrunk
// without surfacing an error. Negative inputs clamp to 0.
func (v *View) SetCursor(row, col int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	if n := len(v.rows); n > 0 && row >= n {
		row = n - 1
	} else if n == 0 {
		row = 0
	}
	if n := len(v.cols); n > 0 && col >= n {
		col = n - 1
	} else if n == 0 {
		col = 0
	}
	v.cursorRow = row
	v.cursorCol = col
	// A stale/computed target may land on a hidden column; snap to the
	// nearest visible one so the cursor never renders invisibly.
	v.snapCursorOffHiddenLocked()
}

// renderBody is the pure function that turns a snapshot into the
// styled text Render writes into the gocui.View. Lives here (next to
// the scroll logic) because the layout walk is intertwined with the
// viewport offsets owned by this file.
//
// Output structure:
//
//	<header-line>\n
//	<data-line-0>\n
//	<data-line-1>\n
//	...
//
// The final line is not terminated with a newline so gocui doesn't
// allocate an extra empty trailing line in the buffer.
func renderBody(snap viewSnapshot, innerW, innerH int) string {
	if len(snap.cols) == 0 {
		return EmptyResultIndicator
	}
	dataRows := innerH - 1
	if dataRows < 1 {
		dataRows = 1
	}

	var sb strings.Builder
	moreLeft, moreRight := columnScrollHints(snap, innerW)
	sb.WriteString(renderHeaderLine(snap, innerW, moreLeft, moreRight))
	sb.WriteByte('\n')

	// Iterate over the projected row-index list (filter → sort → hide
	// composition) rather than the raw buffer so non-matching rows are
	// skipped during render. The cursor is a row-index into snap.rows
	// (not into the projection) so j/k still walks raw rows; only the
	// visible rendering honors the filter. dbsavvy-uv0.4.
	indices := project(snap)
	end := snap.rowOffset + dataRows
	if end > len(indices) {
		end = len(indices)
	}
	for i := snap.rowOffset; i < end; i++ {
		sb.WriteString(renderDataLine(snap, indices[i], innerW))
		if i != end-1 {
			sb.WriteByte('\n')
		}
	}
	// Pad with empty lines so the host view doesn't show stale
	// content from a previous frame (gocui's Clear was called before
	// WriteString in Render, but extra padding here is harmless and
	// keeps the visible region uniform).
	for filled := end - snap.rowOffset; filled < dataRows; filled++ {
		sb.WriteByte('\n')
	}
	// Hide-cols footer (dbsavvy-uv0.6). When any column is hidden, append
	// a `hidden: c1, c2` line so users have a visible cue. The line is
	// wrapped in the dim SGR pair (\x1b[2m…\x1b[22m); themes without dim
	// support degrade to plain text.
	if footer := hideFooterLine(snap); footer != "" {
		sb.WriteByte('\n')
		sb.WriteString(footer)
	}
	return sb.String()
}

// hideFooterLine renders the "hidden: c1, c2" footer in dim style when
// any columns are hidden. Returns "" when nothing is hidden.
// dbsavvy-uv0.6.
func hideFooterLine(snap viewSnapshot) string {
	if len(snap.hidden) == 0 {
		return ""
	}
	names := make([]string, 0, len(snap.hidden))
	for i := 0; i < len(snap.cols); i++ {
		if snap.hidden[i] {
			names = append(names, snap.cols[i].Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "\x1b[2mhidden: " + strings.Join(names, ", ") + "\x1b[22m"
}

// renderHeaderLine assembles the column-name header. Headers use the
// TableHeaderFg style — wrapped in a single SGR pair around the whole
// line so column separators inherit the style too.
func renderHeaderLine(snap viewSnapshot, innerW int, moreLeft, moreRight bool) string {
	var sb strings.Builder
	visibleCols := visibleColumnOrder(snap)
	used := 0
	for _, c := range visibleCols {
		w := effectiveWidth(snap.widths, c)
		// headerLabel folds in the FK `→ ` prefix when col.IsForeignKey;
		// padRight handles the truncation case if a narrow locked width
		// can't accommodate the full label. dbsavvy-bwq.14 (B3).
		name := padRight(headerLabel(snap.cols[c]), w)
		if used+w > innerW {
			break
		}
		sb.WriteString(name)
		sb.WriteString(" │ ")
		used += w + ColSepWidth
	}
	// Apply TableHeader styling around the whole line. We resolve at
	// render time so a theme hot-reload is picked up on next paint.
	line := strings.TrimRight(sb.String(), " │")
	// Pad to innerW so the underline / inverted style covers the
	// full width if the theme sets a background.
	if displayWidth(line) < innerW {
		line = padRight(line, innerW)
	}
	// Overlay the ‹ / › scroll arrows on the header's edge cells so the
	// user can see columns continue past the viewport. Header text carries
	// no per-cell ANSI, so a plain rune overlay is safe here.
	return overlayColumnArrows(line, moreLeft, moreRight)
}

// overlayColumnArrows draws the ◄ / ► horizontal-scroll arrows one cell in
// from each edge of a plain (un-styled) line, leaving the edge-most cell as
// padding so the arrow doesn't sit flush against the view border. ◄ / ► are
// single-cell glyphs (unlike ◀ / ▶, which some terminals widen), so the
// rune overlay keeps the column grid aligned. Safe only for the header line,
// which carries no ANSI styling. dbsavvy column-scroll indicator.
func overlayColumnArrows(line string, left, right bool) string {
	if !left && !right {
		return line
	}
	r := []rune(line)
	if len(r) < 2 {
		return line
	}
	if left {
		r[0] = ' '
		r[1] = '◄'
	}
	if right {
		r[len(r)-1] = ' '
		r[len(r)-2] = '►'
	}
	return string(r)
}

// columnScrollHints reports whether non-hidden columns exist beyond the
// rendered window to the left / right of the current horizontal scroll
// position. Drives the header edge arrows. dbsavvy column-scroll indicator.
func columnScrollHints(snap viewSnapshot, innerW int) (left, right bool) {
	order := visibleColumnOrder(snap)
	right = fitColumns(order, snap.widths, innerW) < len(order)

	// Columns scrolled off the left are the non-hidden ones before
	// colOffset. Column 0 is pinned when frozenFirstCol is on, so it is
	// never counted as hidden-left.
	lo := 0
	if snap.frozenFirstCol {
		lo = 1
	}
	for c := lo; c < snap.colOffset && c < len(snap.cols); c++ {
		if !snap.hidden[c] {
			left = true
			break
		}
	}
	return left, right
}

// fitColumns returns how many leading entries of order fit within innerW,
// using the same per-column accounting (effectiveWidth + ColSepWidth) as
// the renderHeaderLine / renderDataLine layout loops. Kept in lock-step
// with those loops so the scroll hints never disagree with what renders.
func fitColumns(order, widths []int, innerW int) int {
	used := 0
	for i, c := range order {
		w := effectiveWidth(widths, c)
		if used+w > innerW {
			return i
		}
		used += w + ColSepWidth
	}
	return len(order)
}

// renderDataLine renders row r in the snapshot, applying selection
// highlight to any cell inside the selection rectangle. Per-cell
// styling is applied first, then selection highlight is layered on
// top via ANSI reverse-video.
func renderDataLine(snap viewSnapshot, r int, innerW int) string {
	var sb strings.Builder
	row := snap.rows[r]
	visibleCols := visibleColumnOrder(snap)
	used := 0
	for _, c := range visibleCols {
		w := effectiveWidth(snap.widths, c)
		if used+w > innerW {
			break
		}
		var value any
		if c < len(row.Values) {
			value = row.Values[c]
		}
		// Dirty-cell substitution: if this (rowPK, column) carries a staged
		// edit, render the staged NewValue (with the DirtyCellBg tint)
		// instead of the stale DB value so an unsaved edit is visible
		// (dbsavvy-cyh). renderCellPadded pads the plain visible string
		// before wrapping, so a digit in the SGR prefix can never collide
		// with a padded value.
		isDirty := false
		if pk := rowPKValues(row, snap.rowIdentity); pk != nil {
			if e, ok := cellPendingEdit(snap.pendingEdits, pk, snap.cols[c].Name); ok {
				value = e.NewValue
				isDirty = true
			}
		}
		styled := renderCellPadded(value, snap.cols[c], w, isDirty)
		// dbsavvy-2ttm.2: when an in-grid search is active and this cell
		// carries match spans, re-render it with the matched substrings
		// highlighted. Cells without spans (and the no-search state) keep
		// the byte-identical clean path above.
		if spans := cellHighlightSpans(snap, r, c); len(spans) > 0 {
			styled = renderCellPaddedHighlighted(value, snap.cols[c], w, isDirty, spans)
		}
		if inSelection(snap, r, c) {
			styled = applySelectionHighlight(styled)
		}
		sb.WriteString(styled)
		sb.WriteString("   ")
		used += w + ColSepWidth
	}
	return strings.TrimRight(sb.String(), " ")
}

// visibleColumnOrder returns the column indices to render in left-to-
// right order, honoring frozenFirstCol and the horizontal scroll
// offset. When frozenFirstCol is on, column 0 is always first; the
// remainder starts at max(colOffset, 1). When off, the order is the
// natural [colOffset, len(cols)) range.
//
// dbsavvy-uv0.6: indices in snap.hidden are filtered out via
// filterHidden (see hide.go). The filter runs AFTER the frozen-first /
// colOffset composition so hiding column 0 still hides it even when
// frozenFirstCol is on (consistent with user expectation: the overlay
// shows ALL columns including the frozen one).
func visibleColumnOrder(snap viewSnapshot) []int {
	if len(snap.cols) == 0 {
		return nil
	}
	out := make([]int, 0, len(snap.cols))
	if snap.frozenFirstCol {
		out = append(out, 0)
		start := snap.colOffset
		if start < 1 {
			start = 1
		}
		for c := start; c < len(snap.cols); c++ {
			if c == 0 {
				continue
			}
			out = append(out, c)
		}
		return filterHidden(out, snap.hidden)
	}
	start := snap.colOffset
	if start < 0 {
		start = 0
	}
	for c := start; c < len(snap.cols); c++ {
		out = append(out, c)
	}
	return filterHidden(out, snap.hidden)
}
