package grid

import "unicode/utf8"

// autoSizeFromSampleLocked walks the first AutoSizeSampleRowCount rows
// (plus the header) and locks in per-column widths. Called from
// AppendRows under the write lock. Once len(rows) ≥ AutoSizeSampleRowCount
// the widths are committed and widthsLocked flips true; subsequent
// AppendRows skip the resize pass entirely.
//
// Algorithm: each column's width is max(header len, max(cell render
// length) over sampled rows), clamped to [MinColumnWidth, MaxColumnWidth].
// Header text is the column Name; cells are stringified through the
// same renderCell path Render uses so the sample faithfully reflects
// the display width (NULL → "NULL", BLOBs → hex preview, etc).
//
// Pre-conditions: v.mu held in write mode; len(v.widths) == len(v.cols).
func (v *View) autoSizeFromSampleLocked() {
	if len(v.cols) == 0 {
		v.widthsLocked = true
		return
	}
	if len(v.widths) != len(v.cols) {
		v.widths = make([]int, len(v.cols))
		for i := range v.widths {
			v.widths[i] = -1
		}
	}
	sample := len(v.rows)
	if sample > AutoSizeSampleRowCount {
		sample = AutoSizeSampleRowCount
	}
	for c, col := range v.cols {
		// Seed against the rendered header label (col.Name plus the FK
		// `→ ` prefix when IsForeignKey) so the locked width accommodates
		// the glyph and the header never overflows. dbsavvy-bwq.14 (B3).
		best := displayWidth(headerLabel(col))
		for r := 0; r < sample; r++ {
			row := v.rows[r]
			var cell any
			if c < len(row.Values) {
				cell = row.Values[c]
			}
			rendered := renderCellPlain(cell, v.cols[c])
			if w := displayWidth(rendered); w > best {
				best = w
			}
		}
		if best < MinColumnWidth {
			best = MinColumnWidth
		}
		if best > MaxColumnWidth {
			best = MaxColumnWidth
		}
		v.widths[c] = best
	}
	if len(v.rows) >= AutoSizeSampleRowCount {
		v.widthsLocked = true
	}
}

// displayWidth returns the rune count of s. For ASCII this is len(s);
// for unicode it counts code points (not grapheme clusters — combining
// marks count separately, which over-estimates by a small fraction in
// rare cases). Sufficient for column-width sizing where a one-cell
// over-allocation is harmless.
func displayWidth(s string) int {
	return utf8.RuneCountInString(s)
}

// padRight returns s padded with spaces to width w. If s is wider than
// w, it is truncated and a trailing ellipsis "…" is added in place of
// the last visible cell — matching the spec ("`…` truncation on
// overflow"). If w ≤ 1, returns s truncated to w runes verbatim.
func padRight(s string, w int) string {
	dw := displayWidth(s)
	if dw == w {
		return s
	}
	if dw < w {
		// Fast-path ASCII; for unicode just append spaces (each ASCII
		// space is one rune == one cell).
		pad := make([]byte, w-dw)
		for i := range pad {
			pad[i] = ' '
		}
		return s + string(pad)
	}
	// Truncate with ellipsis. Walk runes so we don't slice mid-rune.
	if w < 1 {
		return ""
	}
	runes := []rune(s)
	if w == 1 {
		return string(runes[0])
	}
	// Keep the first (w-1) runes then append "…".
	return string(runes[:w-1]) + "…"
}
