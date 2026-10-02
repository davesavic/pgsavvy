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
// Only records intersecting the viewport are formatted. Cell values pass
// through SanitizeCellEscapes so server-side terminal escapes cannot bleed.
func renderExpanded(snap viewSnapshot, innerW, innerH int) string {
	return expandedViewport(snap, innerW, innerH).body
}

type expandedWindow struct {
	body       string
	rows       []int
	lineOffset int
}

// A record qualifies for visible-scope export when any of its lines,
// including the separator, intersects this window. Export retains whole rows.
func expandedViewport(snap viewSnapshot, innerW, innerH int) expandedWindow {
	window := expandedWindow{}
	if innerW <= 0 || innerH <= 0 {
		return window
	}
	if len(snap.cols) == 0 {
		window.body = expandedSlice(emptyResultText(snap), 0, innerW)
		return window
	}
	if len(snap.rows) == 0 {
		window.body = expandedSlice(expandedSeparator(0, snap.estimatedRows, innerW), 0, innerW)
		return window
	}
	gutter := min(expandedGutterWidth(snap), max(innerW-4, 0))
	valueWidth := max(innerW-gutter-3, 1)

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
		window.body = expandedSlice(expandedSeparator(0, snap.estimatedRows, innerW), 0, innerW)
		return window
	}
	cursorIdx := max(projectedPos(indices, snap.cursorRow), 0)
	visibleCols := expandedColumnOrder(snap)
	lines := make([]string, 0, innerH)
	for i := cursorIdx; i < len(indices) && len(lines) < innerH; i++ {
		record := []string{expandedSeparator(i+1, expandedTotalEstimate(snap, len(indices)), innerW)}
		row := snap.rows[indices[i]]
		for _, c := range visibleCols {
			label := expandedColumnLabel(snap.cols[c].Name)
			var value any
			if c < len(row.Values) {
				value = row.Values[c]
			}
			plain := strings.ReplaceAll(renderCellPlain(value, snap.cols[c]), "\t", " ")
			// Horizontal motion shifts each logical value line, leaving the
			// column-name gutter fixed. Then wrap the remaining value.
			logicalLines := strings.Split(plain, "\n")
			for j, line := range logicalLines {
				logicalLines[j] = expandedSlice(line, max(snap.colOffset, 0), displayCols(line))
			}
			record = append(record, expandedRecordLines(label, strings.Join(logicalLines, "\n"), gutter, valueWidth)...)
		}
		start := 0
		if i == cursorIdx {
			start = min(max(snap.expandedLineOffset, 0), len(record)-1)
			window.lineOffset = start
		}
		end := min(len(record), start+innerH-len(lines))
		for _, line := range record[start:end] {
			lines = append(lines, expandedSlice(line, 0, innerW))
		}
		window.rows = append(window.rows, indices[i])
	}
	window.body = strings.Join(lines, "\n")
	return window
}

// expandedSlice clips plain text in terminal cells without splitting UTF-8
// or emitting half of a wide rune. A left-edge partial rune becomes padding.
func expandedSlice(s string, start, width int) string {
	var b strings.Builder
	pos, used := 0, 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if pos < start {
			pos += w
			if pos > start && used < width {
				b.WriteByte(' ')
				used++
			}
			continue
		}
		if used+w > width {
			break
		}
		b.WriteRune(r)
		used += w
		pos += w
	}
	return b.String()
}

// expandedGutterWidth computes the gutter width for the snapshot:
// max(len(col_name)) clamped to [expandedGutterMin, expandedGutterMax].
// Hidden columns are excluded so toggling a long name into hidden
// shrinks the gutter on the next render.
func expandedGutterWidth(snap viewSnapshot) int {
	maxLen := 0
	for _, c := range expandedColumnOrder(snap) {
		if l := len(expandedColumnLabel(snap.cols[c].Name)); l > maxLen {
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

func expandedColumnLabel(name string) string {
	name = SanitizeCellEscapes(name)
	name = strings.ReplaceAll(name, "\n", " ")
	return strings.ReplaceAll(name, "\t", " ")
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
