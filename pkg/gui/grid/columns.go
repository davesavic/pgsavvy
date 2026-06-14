package grid

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

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
	sample := min(len(v.rows), AutoSizeSampleRowCount)
	for c, col := range v.cols {
		// Seed against the rendered header label (col.Name plus the FK
		// `→ ` prefix when IsForeignKey) so the locked width accommodates
		// the glyph and the header never overflows.
		best := displayWidth(headerLabel(col))
		for r := range sample {
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

// displayCols returns the terminal display width of s in columns,
// counting East Asian wide / fullwidth runes (CJK, emoji) as 2 cells
// and combining marks as 0 via go-runewidth. Used by the rune-boundary
// truncation paths so wide chars budget correctly.
func displayCols(s string) int {
	return runewidth.StringWidth(s)
}

// truncateToWidth returns s clamped to at most maxCols display columns,
// cutting on a rune boundary and appending the ellipsis "…" (1 column)
// when content was dropped. The result is always valid UTF-8 — runes
// are never sliced mid-byte — and its display width never exceeds
// maxCols. maxCols ≤ 0 yields "". When s already fits it is returned
// unchanged. A wide rune that would straddle the budget is dropped
// rather than half-rendered.
func truncateToWidth(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxCols {
		return s
	}
	// Reserve one column for the ellipsis. With only one column to give
	// we still must emit the ellipsis so overflow is visible.
	budget := maxCols - 1
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
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
