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
	out := buildTSV(snap)
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
