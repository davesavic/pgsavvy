package grid

import (
	"strings"

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

// displayWidth budgets terminal cells, including wide and combining characters.
func displayWidth(s string) int {
	return displayCols(s)
}

// displayCols returns the terminal display width of s in columns,
// counting East Asian wide / fullwidth runes (CJK, emoji) as 2 cells
// and combining marks as 0 via go-runewidth. Used by the rune-boundary
// truncation paths so wide chars budget correctly.
func displayCols(s string) int {
	return runewidth.StringWidth(s)
}

// truncateToWidth returns s clamped to at most maxCols display columns,
// cutting on a grapheme boundary and appending a fitting overflow marker
// when content was dropped. The result is always valid UTF-8 — runes
// are never sliced mid-byte — and its display width never exceeds
// maxCols. maxCols ≤ 0 yields "". When s already fits it is returned
// unchanged. A wide rune that would straddle the budget is dropped
// rather than half-rendered.
func truncateToWidth(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	return runewidth.Truncate(s, maxCols, overflowMarker(maxCols))
}

func overflowMarker(w int) string {
	if displayWidth("…") <= w {
		return "…"
	}
	return "."
}

// cellText separates retained source from the overflow marker so search
// highlights cannot accidentally target an ellipsis or padding.
func cellText(s string, w int) (prefix, tail string) {
	if w <= 0 {
		return "", ""
	}
	if displayWidth(s) <= w {
		return s, ""
	}
	if w == 1 {
		prefix = runewidth.Truncate(s, w, "")
		if displayWidth(prefix) == 1 {
			return prefix, ""
		}
		return "", overflowMarker(w)
	}
	tail = overflowMarker(w)
	return runewidth.Truncate(s, w-displayWidth(tail), ""), tail
}

// padRight fills exactly w terminal cells without splitting a grapheme.
// Width one retains a fitting first character for ASCII compatibility;
// an unfittable wide character uses a fitting overflow marker instead.
func padRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	prefix, tail := cellText(s, w)
	text := prefix + tail
	return text + strings.Repeat(" ", w-displayWidth(text))
}
