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
	if len(v.rows) > 0 && v.cursorRow < len(v.rows)-1 {
		v.cursorRow++
		v.expandedLineOffset = 0
	}
	v.mu.Unlock()
}

// MoveCursorUp moves the cursor up by one row, clamped to row 0. In
// expanded mode this means "previous record"; wrapped-line offset is
// reset so the prior record starts at the top.
func (v *View) MoveCursorUp() {
	v.mu.Lock()
	if v.cursorRow > 0 {
		v.cursorRow--
		v.expandedLineOffset = 0
	}
	v.mu.Unlock()
}

// MoveCursorLeft moves the cursor left by one column. Clamped to
// column 0; does not wrap.
func (v *View) MoveCursorLeft() {
	v.mu.Lock()
	if v.cursorCol > 0 {
		v.cursorCol--
	}
	v.mu.Unlock()
}

// MoveCursorRight moves the cursor right by one column. Clamped to
// the last configured column.
func (v *View) MoveCursorRight() {
	v.mu.Lock()
	if len(v.cols) > 0 && v.cursorCol < len(v.cols)-1 {
		v.cursorCol++
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
	target := v.cursorRow + step
	if target > len(v.rows)-1 {
		target = len(v.rows) - 1
	}
	if target < 0 {
		target = 0
	}
	v.cursorRow = target
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
	target := v.cursorRow - step
	if target < 0 {
		target = 0
	}
	v.cursorRow = target
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

// JumpFirst moves the cursor to row 0 (gg). In expanded mode this also
// resets the wrapped-line offset so the first record starts at the top.
func (v *View) JumpFirst() {
	v.mu.Lock()
	v.cursorRow = 0
	v.expandedLineOffset = 0
	v.mu.Unlock()
}

// JumpLast moves the cursor to the last loaded row (G in grid mode; in
// expanded mode the result-tab controller rebinds G to this method
// instead of ReadToEnd per AD-14). Auto-prefetch is considered on the
// next Render.
func (v *View) JumpLast() {
	v.mu.Lock()
	if len(v.rows) > 0 {
		v.cursorRow = len(v.rows) - 1
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
	sb.WriteString(renderHeaderLine(snap, innerW))
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
func renderHeaderLine(snap viewSnapshot, innerW int) string {
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
		sb.WriteByte(' ')
		used += w + 1
	}
	// Apply TableHeader styling around the whole line. We resolve at
	// render time so a theme hot-reload is picked up on next paint.
	line := strings.TrimRight(sb.String(), " ")
	// Pad to innerW so the underline / inverted style covers the
	// full width if the theme sets a background.
	if displayWidth(line) < innerW {
		line = padRight(line, innerW)
	}
	return line
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
		// Pad the visible string to column width, then wrap with the
		// style. We must NOT splice padded into the already-decorated
		// string via strings.Replace: the visible value (e.g. "3" or
		// "5") can collide with a digit inside the SGR prefix itself
		// (e.g. \x1b[35m for magenta), corrupting the escape sequence
		// and dumping its remnants onto the screen.
		visible := renderCellPlain(value, snap.cols[c])
		padded := padRight(visible, w)
		styled := wrapWithStyle(padded, styleForCell(value, snap.cols[c]))
		if inSelection(snap, r, c) {
			styled = applySelectionHighlight(styled)
		}
		sb.WriteString(styled)
		sb.WriteByte(' ')
		used += w + 1
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
