package grid

import "strings"

// Yank serialises the current selection (or the cursor cell when no
// selection is active) as TSV — tab-separated columns, newline-
// separated rows. The result is also pushed through the configured
// ClipboardWriter so terminal-host integrations (OSC-52 etc) can
// receive it. The string is returned regardless of clipboard outcome
// so tests can assert on the payload without wiring a fake writer.
//
// Format details (chosen for round-trip-ability with spreadsheets):
//
//   - cells are stringified through renderCellPlain → no ANSI escapes
//   - rows separated by '\n'
//   - cells separated by '\t'
//   - embedded tabs/newlines in cell content pass through verbatim
//     (a TSV consumer would already need to handle them; we don't
//     re-encode here because re-encoding would lose round-trippability
//     for the common "this cell contains a tab/newline" case)
//
// Row-mode selection covers every column in the selected rows. Cell
// mode is a single cell. Block mode is a rectangular slice. With no
// active selection, the cell under the cursor is yanked.
func (v *View) Yank() string {
	snap := v.snapshot()
	var out string
	if snap.viewMode == ViewModeExpanded {
		out = buildExpandedYank(snap)
	} else {
		out = buildTSV(snap)
	}
	v.mu.RLock()
	cw := v.clipboard
	v.mu.RUnlock()
	if cw != nil {
		// Best-effort: a clipboard write failure shouldn't drop the
		// yanked string from the function return. Callers can wire a
		// status-bar toast on the writer side if they need to surface
		// failures.
		_ = cw.Write(out)
	}
	return out
}

// YankCell copies the sanitized display value of the focused cell to the
// configured ClipboardWriter and returns it. ok is false when the grid has
// no focused cell (empty result set) so the caller can no-op without a
// panic; in that case no clipboard write is attempted. The returned error
// is the ClipboardWriter's (e.g. ErrClipboardTooLarge / ErrClipboardUnavailable)
// so the caller can surface it as a toast. The value is the same string the
// grid renders (renderCellPlain → SanitizeCellEscapes), never raw server
// bytes.
func (v *View) YankCell() (value string, ok bool, err error) {
	snap := v.snapshot()
	if len(snap.cols) == 0 || len(snap.rows) == 0 {
		return "", false, nil
	}
	r := snap.cursorRow
	c := snap.cursorCol
	if r < 0 || r >= len(snap.rows) || c < 0 || c >= len(snap.cols) {
		return "", false, nil
	}
	var cellVal any
	if c < len(snap.rows[r].Values) {
		cellVal = snap.rows[r].Values[c]
	}
	value = renderCellPlain(cellVal, snap.cols[c])
	return value, true, v.writeClipboard(value)
}

// YankRow copies the focused row as TSV (all columns, in column order) to the
// configured ClipboardWriter and returns it. ok is false when the grid has no
// focused row (empty result set). Cells are sanitized display values
// (renderCellPlain → SanitizeCellEscapes) joined by '\t'. The returned error
// is the ClipboardWriter's so the caller can toast on failure.
func (v *View) YankRow() (value string, ok bool, err error) {
	snap := v.snapshot()
	if len(snap.cols) == 0 || len(snap.rows) == 0 {
		return "", false, nil
	}
	r := snap.cursorRow
	if r < 0 || r >= len(snap.rows) {
		return "", false, nil
	}
	row := snap.rows[r]
	var sb strings.Builder
	for c := range snap.cols {
		if c > 0 {
			sb.WriteByte('\t')
		}
		var cellVal any
		if c < len(row.Values) {
			cellVal = row.Values[c]
		}
		sb.WriteString(renderCellPlain(cellVal, snap.cols[c]))
	}
	value = sb.String()
	return value, true, v.writeClipboard(value)
}

// writeClipboard pushes value through the configured ClipboardWriter under
// the read lock, returning the writer's error. A nil writer (never the case
// after NewView normalises to noopClipboard, but defensive) is a no-op.
func (v *View) writeClipboard(value string) error {
	v.mu.RLock()
	cw := v.clipboard
	v.mu.RUnlock()
	if cw == nil {
		return nil
	}
	return cw.Write(value)
}

// buildExpandedYank serialises the selected records as `col\tvalue\n`
// lines per record, with a blank line between records. Hidden columns
// are skipped (same projection as renderExpanded). Cell values pass
// through renderCellPlain so the sanitizer + truncation rules apply.
// dbsavvy-uv0.7.
func buildExpandedYank(snap viewSnapshot) string {
	if len(snap.cols) == 0 || len(snap.rows) == 0 {
		return ""
	}
	r0, _, r1, _, _ := selectionRange(snap)
	if r0 < 0 {
		r0 = 0
	}
	if r1 >= len(snap.rows) {
		r1 = len(snap.rows) - 1
	}
	if r0 > r1 {
		return ""
	}
	visibleCols := expandedColumnOrder(snap)
	var sb strings.Builder
	for r := r0; r <= r1; r++ {
		if r > r0 {
			sb.WriteByte('\n')
		}
		row := snap.rows[r]
		for _, c := range visibleCols {
			var value any
			if c < len(row.Values) {
				value = row.Values[c]
			}
			sb.WriteString(snap.cols[c].Name)
			sb.WriteByte('\t')
			sb.WriteString(renderCellPlain(value, snap.cols[c]))
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// buildTSV is the pure, side-effect-free TSV serialiser. Split out so
// tests can assert against it without needing a *View.
func buildTSV(snap viewSnapshot) string {
	if len(snap.cols) == 0 || len(snap.rows) == 0 {
		return ""
	}
	r0, c0, r1, c1, _ := selectionRange(snap)
	// Clamp range to actual extents — defensive against a stale
	// cursor that hasn't been re-anchored after SetColumns.
	if r0 < 0 {
		r0 = 0
	}
	if c0 < 0 {
		c0 = 0
	}
	if r1 >= len(snap.rows) {
		r1 = len(snap.rows) - 1
	}
	if c1 >= len(snap.cols) {
		c1 = len(snap.cols) - 1
	}
	if r0 > r1 || c0 > c1 {
		return ""
	}
	var sb strings.Builder
	for r := r0; r <= r1; r++ {
		row := snap.rows[r]
		for c := c0; c <= c1; c++ {
			var value any
			if c < len(row.Values) {
				value = row.Values[c]
			}
			if c > c0 {
				sb.WriteByte('\t')
			}
			sb.WriteString(renderCellPlain(value, snap.cols[c]))
		}
		if r != r1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
