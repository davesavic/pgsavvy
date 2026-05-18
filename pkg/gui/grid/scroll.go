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
// inside the next Render).
func (v *View) MoveCursorDown() {
	v.mu.Lock()
	if len(v.rows) > 0 && v.cursorRow < len(v.rows)-1 {
		v.cursorRow++
	}
	v.mu.Unlock()
}

// MoveCursorUp moves the cursor up by one row, clamped to row 0.
func (v *View) MoveCursorUp() {
	v.mu.Lock()
	if v.cursorRow > 0 {
		v.cursorRow--
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
// pass clamps scroll afterwards so the cursor stays on screen.
func (v *View) HalfPageDown() {
	v.mu.Lock()
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
	v.mu.Unlock()
}

// HalfPageUp is the symmetric counterpart of HalfPageDown.
func (v *View) HalfPageUp() {
	v.mu.Lock()
	step := ResultPageSize / 2
	if step < 1 {
		step = 1
	}
	target := v.cursorRow - step
	if target < 0 {
		target = 0
	}
	v.cursorRow = target
	v.mu.Unlock()
}

// JumpFirst moves the cursor to row 0 (gg).
func (v *View) JumpFirst() {
	v.mu.Lock()
	v.cursorRow = 0
	v.mu.Unlock()
}

// JumpLast moves the cursor to the last loaded row (G). Auto-prefetch
// will be considered on the next Render — the cursor lands at the
// tail, well inside PrefetchThreshold.
func (v *View) JumpLast() {
	v.mu.Lock()
	if len(v.rows) > 0 {
		v.cursorRow = len(v.rows) - 1
	}
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

	end := snap.rowOffset + dataRows
	if end > len(snap.rows) {
		end = len(snap.rows)
	}
	for r := snap.rowOffset; r < end; r++ {
		sb.WriteString(renderDataLine(snap, r, innerW))
		if r != end-1 {
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
	return sb.String()
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
		name := padRight(snap.cols[c].Name, w)
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
		visible, decorated := renderCell(value, snap.cols[c])
		// padRight operates on the visible string so width math is
		// correct; we then re-apply the SGR escapes around the
		// padded form.
		padded := padRight(visible, w)
		styled := strings.Replace(decorated, visible, padded, 1)
		// If the cell content was empty / the SGR wrapper produced
		// no decoration, fall back to the padded plain string.
		if styled == decorated && !strings.Contains(decorated, "\x1b[") {
			styled = padded
		}
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
		return out
	}
	start := snap.colOffset
	if start < 0 {
		start = 0
	}
	for c := start; c < len(snap.cols); c++ {
		out = append(out, c)
	}
	return out
}
