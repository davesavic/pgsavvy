package grid

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// ViewModeGrid / ViewModeExpanded are the two render modes a grid View
// can be in. Defaults to ViewModeGrid; flipped via SetViewMode. The
// AppState scalar `LastResultViewMode` round-trips this string verbatim
// for the global session-preference defined in §12.3.
const (
	ViewModeGrid     = "grid"
	ViewModeExpanded = "expanded"
)

// expandedGutterMin / expandedGutterMax bound the column-name gutter
// width in expanded mode. Per §12.3 the gutter is sized to
// max(len(col_name)) clamped to [12, 32].
const (
	expandedGutterMin = 12
	expandedGutterMax = 32
)

// normaliseViewMode returns the canonical view-mode string. Empty /
// unknown values fall back to ViewModeGrid so a corrupt AppState entry
// can't strand the user in an unrecognised mode.
func normaliseViewMode(m string) string {
	if m == ViewModeExpanded {
		return ViewModeExpanded
	}
	return ViewModeGrid
}

// renderExpanded turns snap into psql `\x`-style record output:
//
//	-[ RECORD n of ~total ]----------
//	col_a       | value_a
//	col_b       | value_b (wrapped continues here)
//	            |   on the next line if too long
//	-[ RECORD n+1 of ~total ]--------
//	...
//
// Each record formats only when its index is within
// [snap.rowOffset-1, snap.rowOffset+innerH+1] — the +/- 1 overscan keeps
// scrolling smooth without formatting every record on every paint (the
// AC explicitly forbids "formats every record on Render" for a 1M-row
// result). Cell values pass through SanitizeCellEscapes so server-side
// terminal escapes cannot bleed.
func renderExpanded(snap viewSnapshot, innerW, innerH int) string {
	if len(snap.cols) == 0 {
		return emptyResultText(snap)
	}
	if len(snap.rows) == 0 {
		// Empty body — just a header banner so the user sees the mode.
		return expandedSeparator(0, snap.estimatedRows, innerW)
	}
	if innerW < 4 {
		innerW = 4
	}
	// innerH is reserved for future per-viewport sizing; the current
	// overscan policy is cursor-relative, so the param is intentionally
	// unused here.
	_ = innerH
	gutter := expandedGutterWidth(snap)
	valueWidth := max(
		// "gutter | value"
		innerW-gutter-3, 4)

	// Determine the active record from the cursor. cursorRow is a raw-
	// buffer index; the displayed record is its position within the
	// projected (filter -> sort -> hide) order, so translate it through
	// projectedPos rather than indexing the projection with the raw value
	// directly — otherwise an active sort double-projects and expanded
	// mode shows a different record than the one j/k landed on. Falls back
	// to the first projected record when the cursor's row isn't visible
	// (e.g. filtered out), matching the grid-mode clamp.
	indices := project(snap)
	if len(indices) == 0 {
		return expandedSeparator(0, snap.estimatedRows, innerW)
	}
	cursorIdx := max(projectedPos(indices, snap.cursorRow), 0)

	// Overscan: format from cursor-1 to as many records as fit in the
	// viewport, plus one record past the bottom edge. We bias around
	// the cursor rather than the rowOffset because in expanded mode the
	// "viewport" is one record at a time — extra records are pre-formatted
	// so j/k feels instantaneous.
	start := max(cursorIdx-1, 0)
	end := min(
		// include one record after cursor
		cursorIdx+2, len(indices))

	var sb strings.Builder
	for i := start; i < end; i++ {
		recordOneBased := i + 1
		sb.WriteString(expandedSeparator(recordOneBased, expandedTotalEstimate(snap, len(indices)), innerW))
		sb.WriteByte('\n')
		row := snap.rows[indices[i]]
		visibleCols := expandedColumnOrder(snap)
		for _, c := range visibleCols {
			label := snap.cols[c].Name
			var value any
			if c < len(row.Values) {
				value = row.Values[c]
			}
			plain := renderCellPlain(value, snap.cols[c])
			for _, line := range expandedRecordLines(label, plain, gutter, valueWidth) {
				sb.WriteString(line)
				sb.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// expandedGutterWidth computes the gutter width for the snapshot:
// max(len(col_name)) clamped to [expandedGutterMin, expandedGutterMax].
// Hidden columns are excluded so toggling a long name into hidden
// shrinks the gutter on the next render.
func expandedGutterWidth(snap viewSnapshot) int {
	maxLen := 0
	for _, c := range expandedColumnOrder(snap) {
		if l := len(snap.cols[c].Name); l > maxLen {
			maxLen = l
		}
	}
	if maxLen < expandedGutterMin {
		return expandedGutterMin
	}
	if maxLen > expandedGutterMax {
		return expandedGutterMax
	}
	return maxLen
}

// expandedColumnOrder returns the column indices to render in expanded
// mode in natural left-to-right order, honoring snap.hidden. The
// horizontal scroll offset is NOT applied — expanded mode shows every
// column of the active record; horizontal motion scrolls long values
// inside the value column instead.
func expandedColumnOrder(snap viewSnapshot) []int {
	if len(snap.cols) == 0 {
		return nil
	}
	out := make([]int, 0, len(snap.cols))
	for c := range snap.cols {
		out = append(out, c)
	}
	return filterHidden(out, snap.hidden)
}

// expandedTotalEstimate returns the row count to surface in the
// separator banner: prefer EstimatedRows when known, otherwise the
// projected (filtered) loaded count. 0 means "unknown" — rendered as
// "?" by expandedSeparator.
func expandedTotalEstimate(snap viewSnapshot, projected int) int64 {
	if snap.estimatedRows > 0 {
		return snap.estimatedRows
	}
	return int64(projected)
}

// expandedSeparator builds the "-[ RECORD n of ~total ]--..." line,
// padded to innerW. n==0 renders the empty-body banner without a record
// number.
func expandedSeparator(n int, total int64, innerW int) string {
	var label string
	switch {
	case n == 0 && total == 0:
		label = "[ no records ]"
	case n == 0:
		label = fmt.Sprintf("[ no records of ~%d ]", total)
	case total <= 0:
		label = fmt.Sprintf("[ RECORD %d of ? ]", n)
	default:
		label = fmt.Sprintf("[ RECORD %d of ~%d ]", n, total)
	}
	prefix := "-"
	if innerW <= len(prefix)+len(label) {
		return prefix + label
	}
	dashes := innerW - len(prefix) - len(label)
	return prefix + label + strings.Repeat("-", dashes)
}

// expandedRecordLines wraps value to the value column. The first line
// shows `name | value`; continuation lines show `<gutter spaces> | rest`.
// name is truncated to the gutter width with an ellipsis when needed.
func expandedRecordLines(name, value string, gutter, valueWidth int) []string {
	displayName := truncateToWidth(name, gutter)
	padded := displayName + strings.Repeat(" ", gutter-displayCols(displayName))
	if value == "" {
		return []string{padded + " | "}
	}
	chunks := wrapValue(value, valueWidth)
	out := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		if i == 0 {
			out = append(out, padded+" | "+ch)
		} else {
			out = append(out, strings.Repeat(" ", gutter)+" | "+ch)
		}
	}
	return out
}

// wrapValue splits s into chunks of at most width DISPLAY columns,
// respecting existing newlines (each \n forces a wrap). Chunks are cut
// on rune boundaries (never mid-byte) and wide runes are kept whole, so
// a multibyte / CJK value never corrupts into invalid UTF-8. Values
// that fit in one chunk return a single-element slice. width must be
// > 0.
func wrapValue(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	// First split on explicit \n so multi-line cell content is preserved.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapLineToWidth(line, width)...)
	}
	return out
}

// wrapLineToWidth breaks line (no embedded newlines) into chunks of at
// most width display columns, cutting on rune boundaries. A wide rune
// that would straddle the boundary starts the next chunk instead of
// being split.
func wrapLineToWidth(line string, width int) []string {
	var out []string
	var b strings.Builder
	used := 0
	for _, r := range line {
		w := runewidth.RuneWidth(r)
		if used+w > width && used > 0 {
			out = append(out, b.String())
			b.Reset()
			used = 0
		}
		b.WriteRune(r)
		used += w
	}
	out = append(out, b.String())
	return out
}
