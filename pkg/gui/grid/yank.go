package grid

import (
	"fmt"
	"strings"
)

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

// YankSelection serialises the current selection (or cursor row when no
// selection is active) with column headers in the configured format and
// publishes through the ClipboardWriter. The format is read from
// YankFormat(). ok is false when the grid is empty (no rows or no cols).
// Returns ErrClipboardTooLarge when the serialized payload exceeds the
// configured maximum.
func (v *View) YankSelection() (value string, ok bool, err error) {
	v.mu.RLock()
	snap := v.snapshot()
	yankFmt := v.yankFormat
	maxBytes := v.maxClipboardBytes
	v.mu.RUnlock()

	if len(snap.cols) == 0 || len(snap.rows) == 0 {
		return "", false, nil
	}

	r0, c0, r1, c1, selOK := selectionRange(snap)
	if !selOK {
		r0 = snap.cursorRow
		r1 = snap.cursorRow
		c0 = 0
		c1 = max(len(snap.cols)-1, 0)
	}

	if r0 < 0 {
		r0 = 0
	}
	if r1 >= len(snap.rows) {
		r1 = len(snap.rows) - 1
	}
	if c0 < 0 {
		c0 = 0
	}
	if c1 >= len(snap.cols) {
		c1 = len(snap.cols) - 1
	}
	if r0 > r1 || c0 > c1 {
		return "", false, nil
	}

	var serialized string
	switch yankFmt {
	case "json":
		serialized = buildYankJSON(snap, r0, c0, r1, c1)
	case "csv":
		serialized = buildYankCSV(snap, r0, c0, r1, c1)
	case "ndjson":
		serialized = buildYankNDJSON(snap, r0, c0, r1, c1)
	default:
		serialized = buildYankTSV(snap, r0, c0, r1, c1)
	}

	if int64(len(serialized)) > maxBytes {
		return serialized, true, ErrClipboardTooLarge
	}

	return serialized, true, v.writeClipboard(serialized)
}

// YankRowWithHeaders serialises the cursor row with column headers in
// the configured format and publishes through the ClipboardWriter.
func (v *View) YankRowWithHeaders() (value string, ok bool, err error) {
	v.mu.RLock()
	snap := v.snapshot()
	yankFmt := v.yankFormat
	maxBytes := v.maxClipboardBytes
	v.mu.RUnlock()

	if len(snap.cols) == 0 || len(snap.rows) == 0 {
		return "", false, nil
	}
	r := snap.cursorRow
	if r < 0 || r >= len(snap.rows) {
		return "", false, nil
	}

	lastCol := max(len(snap.cols)-1, 0)

	var serialized string
	switch yankFmt {
	case "json":
		serialized = buildYankJSON(snap, r, 0, r, lastCol)
	case "csv":
		serialized = buildYankCSV(snap, r, 0, r, lastCol)
	case "ndjson":
		serialized = buildYankNDJSON(snap, r, 0, r, lastCol)
	default:
		serialized = buildYankTSV(snap, r, 0, r, lastCol)
	}

	if int64(len(serialized)) > maxBytes {
		return serialized, true, ErrClipboardTooLarge
	}

	return serialized, true, v.writeClipboard(serialized)
}

// buildExpandedYank serialises the selected records as `col\tvalue\n`
// lines per record, with a blank line between records. Hidden columns
// are skipped (same projection as renderExpanded). Cell values pass
// through renderCellPlain so the sanitizer + truncation rules apply.
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

// buildYankJSON returns a JSON array of objects. Each object maps column
// names to string cell values (via renderCellPlain).
func buildYankJSON(snap viewSnapshot, r0, c0, r1, c1 int) string {
	var sb strings.Builder
	sb.WriteString("[")
	for r := r0; r <= r1; r++ {
		if r > r0 {
			sb.WriteString(",")
		}
		sb.WriteString("\n  {")
		row := snap.rows[r]
		for c := c0; c <= c1; c++ {
			if c > c0 {
				sb.WriteString(",")
			}
			sb.WriteString("\n    ")
			sb.WriteString(jsonString(snap.cols[c].Name))
			sb.WriteString(": ")
			var cellVal any
			if c < len(row.Values) {
				cellVal = row.Values[c]
			}
			sb.WriteString(jsonString(renderCellPlain(cellVal, snap.cols[c])))
		}
		sb.WriteString("\n  }")
	}
	sb.WriteString("\n]")
	return sb.String()
}

// buildYankNDJSON returns newline-delimited JSON objects, one per row.
// Each object maps column names to string cell values.
func buildYankNDJSON(snap viewSnapshot, r0, c0, r1, c1 int) string {
	var sb strings.Builder
	for r := r0; r <= r1; r++ {
		if r > r0 {
			sb.WriteString("\n")
		}
		sb.WriteString("{")
		row := snap.rows[r]
		for c := c0; c <= c1; c++ {
			if c > c0 {
				sb.WriteString(",")
			}
			sb.WriteString(jsonString(snap.cols[c].Name))
			sb.WriteString(":")
			var cellVal any
			if c < len(row.Values) {
				cellVal = row.Values[c]
			}
			sb.WriteString(jsonString(renderCellPlain(cellVal, snap.cols[c])))
		}
		sb.WriteString("}")
	}
	return sb.String()
}

// buildYankCSV returns RFC 4180 CSV with a header row and data rows.
func buildYankCSV(snap viewSnapshot, r0, c0, r1, c1 int) string {
	var sb strings.Builder
	comma := byte(',')
	for c := c0; c <= c1; c++ {
		if c > c0 {
			sb.WriteByte(comma)
		}
		name := SanitizeCellEscapes(snap.cols[c].Name)
		sb.WriteString(Rfc4180Quote(name, comma))
	}
	for r := r0; r <= r1; r++ {
		sb.WriteString("\r\n")
		row := snap.rows[r]
		for c := c0; c <= c1; c++ {
			if c > c0 {
				sb.WriteByte(comma)
			}
			var cellVal any
			if c < len(row.Values) {
				cellVal = row.Values[c]
			}
			sb.WriteString(Rfc4180Quote(renderCellPlain(cellVal, snap.cols[c]), comma))
		}
	}
	return sb.String()
}

// buildYankTSV returns TSV with a header row (extends the existing
// buildTSV to include headers and respect column range).
func buildYankTSV(snap viewSnapshot, r0, c0, r1, c1 int) string {
	var sb strings.Builder
	tab := byte('\t')
	for c := c0; c <= c1; c++ {
		if c > c0 {
			sb.WriteByte(tab)
		}
		sb.WriteString(SanitizeCellEscapes(snap.cols[c].Name))
	}
	for r := r0; r <= r1; r++ {
		sb.WriteByte('\n')
		row := snap.rows[r]
		for c := c0; c <= c1; c++ {
			if c > c0 {
				sb.WriteByte(tab)
			}
			var cellVal any
			if c < len(row.Values) {
				cellVal = row.Values[c]
			}
			sb.WriteString(renderCellPlain(cellVal, snap.cols[c]))
		}
	}
	return sb.String()
}

// jsonString returns the JSON-encoded form of s (a quoted string with
// escapes). Uses a simple encoder to avoid importing encoding/json and
// the potential for circular deps.
func jsonString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch b {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if b < 0x20 {
				sb.WriteString(fmt.Sprintf(`\u%04x`, b))
			} else {
				sb.WriteByte(b)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
