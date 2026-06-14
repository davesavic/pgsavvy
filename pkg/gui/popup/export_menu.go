package popup

import (
	"path/filepath"
	"strings"
)

// ExportMenuField identifies the currently-focused selector row.
type ExportMenuField int

const (
	FieldFormat ExportMenuField = iota
	FieldDestination
	FieldPath
	FieldScope
)

// ExportMenu is the state object backing the <leader>oe export menu.
// Three independent enums:
//   - Format:      index into formats[]
//   - Destination: index into destinations[]
//   - Scope:       index into scopes[]
//
// Plus a "field cursor" indicating which row is currently being adjusted.
//
// The menu is NOT itself a gocui context; rendering is done by the
// caller via Body() (renderable text). The chord wiring + focus stack
// integration is the orchestrator's responsibility.
type ExportMenu struct {
	formats      []string
	destinations []string
	scopes       []string

	formatIdx int
	destIdx   int
	scopeIdx  int
	field     ExportMenuField

	// path is the editable File destination path. The Format↔extension
	// sync keeps path's extension in step with the selected format at all
	// times: the user owns the basename/dir (via SetPath), the format owns
	// the extension, so the filename never lies about its contents.
	path string

	// sqlInsertsIdx is the index into formats[] of the "SQL INSERTs" row
	// when shown-but-disabled; -1 when the row should not be rendered as
	// disabled (i.e. row identity is present and SQL INSERTs is fully
	// enabled, OR the row is absent entirely).
	sqlInsertsIdx int

	// sqlInsertsDisabledReason is the inline reason rendered next to the
	// disabled SQL-INSERTs row and surfaced via ConfirmBlockedReason.
	// Empty falls back to the historical default text.
	// Sourced from GridView.DisabledReason() so the
	// menu reuses F2's single source of truth.
	sqlInsertsDisabledReason string

	// markdownIdx and jsonArrayIdx track which format indexes are
	// "buffered" so the menu knows when to hard-block Confirm under
	// bufferedThresholdExceeded. -1 when not registered.
	markdownIdx  int
	jsonArrayIdx int

	bufferedThresholdExceeded bool
	bufferedThresholdLabel    string
}

// NewExportMenu constructs the state. formats[] should already include or
// exclude "SQL INSERTs" based on HasRowIdentity. sqlInsertsIdx is the
// position of "SQL INSERTs" in formats[] when shown-but-disabled (i.e., the
// caller WANTS the label visible-but-grey); pass -1 to indicate the row
// should not be rendered at all.
func NewExportMenu(formats, destinations, scopes []string, sqlInsertsIdx int, bufferedThresholdExceeded bool) *ExportMenu {
	return &ExportMenu{
		formats:                   append([]string(nil), formats...),
		destinations:              append([]string(nil), destinations...),
		scopes:                    append([]string(nil), scopes...),
		formatIdx:                 0,
		destIdx:                   0,
		scopeIdx:                  0,
		field:                     FieldFormat,
		sqlInsertsIdx:             sqlInsertsIdx,
		markdownIdx:               -1,
		jsonArrayIdx:              -1,
		bufferedThresholdExceeded: bufferedThresholdExceeded,
	}
}

// Field returns the currently-focused selector row.
func (m *ExportMenu) Field() ExportMenuField { return m.field }

// IsPathFieldActive reports whether the Path row is the active field AND
// editable (File destination). The 'i' edit-path binding gates on this so
// the seeded PROMPT only opens when there is a Path to edit.
func (m *ExportMenu) IsPathFieldActive() bool {
	return m.field == FieldPath && m.pathVisible()
}

// MoveField adjusts the field cursor by d, clamping to
// [FieldFormat, FieldScope]. FieldPath is skipped while the Path row is
// hidden (Destination != File) so the cursor never lands on an
// unrendered row, mirroring the disabled SQL-INSERTs skip-nav.
func (m *ExportMenu) MoveField(d int) {
	n := int(m.field) + d
	n = max(n, int(FieldFormat))
	n = min(n, int(FieldScope))
	if !m.pathVisible() && n == int(FieldPath) {
		n += sign(d)
	}
	m.field = ExportMenuField(n)
}

// sign returns the direction of d as -1, 0 or +1.
func sign(d int) int {
	if d < 0 {
		return -1
	}
	if d > 0 {
		return 1
	}
	return 0
}

// pathVisible reports whether the Path row is rendered/navigable. The
// path is a File-only destination, so it is hidden for Clipboard.
func (m *ExportMenu) pathVisible() bool {
	return m.DestinationLabel() == "File"
}

// MoveValue adjusts the value at the current field by d, clamping to
// [0, len-1]. When the current field is FieldFormat AND the resulting
// position lands on a disabled SQL-INSERTs row, the cursor continues
// past it in the same direction. If clamping forces stop on the
// disabled row, the cursor reverts to the previous valid value.
func (m *ExportMenu) MoveValue(d int) {
	switch m.field {
	case FieldFormat:
		m.moveFormatValue(d)
		m.syncPathExt()
	case FieldDestination:
		m.destIdx = clamp(m.destIdx+d, 0, len(m.destinations)-1)
	case FieldScope:
		m.scopeIdx = clamp(m.scopeIdx+d, 0, len(m.scopes)-1)
	}
}

// syncPathExt rewrites the Path's extension to match the current format,
// preserving the user's basename/dir. Replacement targets the LAST dot
// (filepath.Ext) so "a.old.csv" → "a.old.json". Called whenever the
// format is cycled or the path is edited, so the extension can never
// drift out of step with the selected format.
func (m *ExportMenu) syncPathExt() {
	if m.path == "" {
		return
	}
	ext := formatExt(m.FormatLabel())
	if ext == "" {
		return
	}
	old := filepath.Ext(m.path)
	m.path = strings.TrimSuffix(m.path, old) + "." + ext
}

// formatExt maps a Format label to its filesystem extension (no leading
// dot). Mirrors ui.extFor; kept here so the menu can rewrite the Path's
// extension when the format is cycled without reaching into package ui.
func formatExt(label string) string {
	switch label {
	case "CSV":
		return "csv"
	case "TSV":
		return "tsv"
	case "NDJSON":
		return "ndjson"
	case "JSON Array":
		return "json"
	case "Markdown":
		return "md"
	case "SQL INSERTs":
		return "sql"
	}
	return ""
}

func (m *ExportMenu) moveFormatValue(d int) {
	if len(m.formats) == 0 {
		return
	}
	if d == 0 {
		return
	}
	prev := m.formatIdx
	step := 1
	if d < 0 {
		step = -1
	}
	remaining := d
	if remaining < 0 {
		remaining = -remaining
	}
	cur := m.formatIdx
	for remaining > 0 {
		next := cur + step
		if next < 0 || next > len(m.formats)-1 {
			break
		}
		cur = next
		// Skip-over disabled SQL-INSERTs row.
		if cur == m.sqlInsertsIdx {
			// Try to continue past it.
			next2 := cur + step
			if next2 < 0 || next2 > len(m.formats)-1 {
				// Cannot continue past; revert and stop.
				cur = prev
				break
			}
			cur = next2
		}
		remaining--
	}
	m.formatIdx = cur
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// FormatIdx returns the current Format index.
func (m *ExportMenu) FormatIdx() int { return m.formatIdx }

// DestinationIdx returns the current Destination index.
func (m *ExportMenu) DestinationIdx() int { return m.destIdx }

// ScopeIdx returns the current Scope index.
func (m *ExportMenu) ScopeIdx() int { return m.scopeIdx }

// FormatLabel returns the current Format label.
func (m *ExportMenu) FormatLabel() string {
	if m.formatIdx < 0 || m.formatIdx >= len(m.formats) {
		return ""
	}
	return m.formats[m.formatIdx]
}

// DestinationLabel returns the current Destination label.
func (m *ExportMenu) DestinationLabel() string {
	if m.destIdx < 0 || m.destIdx >= len(m.destinations) {
		return ""
	}
	return m.destinations[m.destIdx]
}

// Path returns the current File destination path. Empty until prefilled
// by the caller. Backed by the unexported `path` field, mirroring how
// the label accessors expose their unexported backing state.
func (m *ExportMenu) Path() string { return m.path }

// SetPath sets the File destination path (the user owns the basename/dir)
// and normalises its extension to the current format, so a stale or
// mistyped extension can never reach the export.
func (m *ExportMenu) SetPath(v string) {
	m.path = v
	m.syncPathExt()
}

// Prefill seeds the auto-suggested path. Called once when the menu is
// opened; the path already carries the initial format's extension.
func (m *ExportMenu) Prefill(v string) {
	m.path = v
}

// ScopeLabel returns the current Scope label.
func (m *ExportMenu) ScopeLabel() string {
	if m.scopeIdx < 0 || m.scopeIdx >= len(m.scopes) {
		return ""
	}
	return m.scopes[m.scopeIdx]
}

// IsSQLInsertsSelected reports whether the current format is the
// disabled SQL-INSERTs row.
func (m *ExportMenu) IsSQLInsertsSelected() bool {
	return m.sqlInsertsIdx >= 0 && m.formatIdx == m.sqlInsertsIdx
}

// SetSQLInsertsDisabledReason installs the per-entry disabled-reason
// text used when the SQL-INSERTs row is shown-but-disabled. The caller
// sources the string from GridView.DisabledReason() so the menu reuses
// the editability decision instead of inventing its own. Empty string
// preserves the historical default annotation.
func (m *ExportMenu) SetSQLInsertsDisabledReason(reason string) {
	m.sqlInsertsDisabledReason = reason
}

// sqlInsertsAnnotation returns the inline annotation text rendered next
// to the disabled SQL-INSERTs row. Prefers the F2-sourced reason when
// set; otherwise falls back to the legacy "not a single base table" text
// so callers that never call SetSQLInsertsDisabledReason keep prior UX.
func (m *ExportMenu) sqlInsertsAnnotation() string {
	if m.sqlInsertsDisabledReason != "" {
		return "disabled: " + m.sqlInsertsDisabledReason
	}
	return "disabled — result is not a single base table"
}

// SetBufferedFormatIndexes registers which formats are "buffered" so the
// menu can hard-block Confirm under bufferedThresholdExceeded. Pass -1
// for either index to indicate the format is not present.
func (m *ExportMenu) SetBufferedFormatIndexes(markdownIdx, jsonArrayIdx int) {
	m.markdownIdx = markdownIdx
	m.jsonArrayIdx = jsonArrayIdx
}

// SetBufferedThresholdLabel stores the rendered threshold string used in
// the warning footer (e.g. "≥ 10000 rows"). The menu treats this as
// opaque; the caller is responsible for localization/formatting.
func (m *ExportMenu) SetBufferedThresholdLabel(s string) {
	m.bufferedThresholdLabel = s
}

// ConfirmBlockedReason returns "" when Confirm is allowed; otherwise a
// short human-readable reason. Conditions blocking Confirm:
//   - SQL-INSERTs selected when disabled.
//   - bufferedThresholdExceeded AND format is Markdown or JSON Array.
func (m *ExportMenu) ConfirmBlockedReason() string {
	if m.IsSQLInsertsSelected() {
		if m.sqlInsertsDisabledReason != "" {
			return "SQL INSERTs disabled: " + m.sqlInsertsDisabledReason
		}
		return "SQL INSERTs disabled — result is not a single base table"
	}
	if m.bufferedThresholdExceeded && m.isBufferedFormat() {
		return "buffered format over threshold — pick a streaming format"
	}
	return ""
}

func (m *ExportMenu) isBufferedFormat() bool {
	if m.markdownIdx >= 0 && m.formatIdx == m.markdownIdx {
		return true
	}
	if m.jsonArrayIdx >= 0 && m.formatIdx == m.jsonArrayIdx {
		return true
	}
	return false
}

// Body renders the menu as a text body suitable for writing into a
// gocui view. Layout:
//
//	Export result
//
//	> Format:      CSV
//	  Destination: File
//	  Scope:       Loaded
//
//	(↑/↓ field, ←/→ value, <CR> export, <esc> cancel)
//
// The row matching the field cursor gets a "> " prefix; other rows get
// "  ". When the SQL-INSERTs row is selected and disabled, the Format
// line gets a trailing "(disabled — result is not a single base table)"
// annotation. Buffered-threshold and full-with-filter footers are
// appended when applicable.
func (m *ExportMenu) Body() string {
	var b strings.Builder
	b.WriteString("Export result\n\n")

	b.WriteString(m.rowPrefix(FieldFormat))
	b.WriteString("Format:      ")
	b.WriteString(m.FormatLabel())
	if m.IsSQLInsertsSelected() {
		b.WriteString("  (")
		b.WriteString(m.sqlInsertsAnnotation())
		b.WriteByte(')')
	}
	b.WriteByte('\n')

	b.WriteString(m.rowPrefix(FieldDestination))
	b.WriteString("Destination: ")
	b.WriteString(m.DestinationLabel())
	b.WriteByte('\n')

	if m.pathVisible() {
		b.WriteString(m.rowPrefix(FieldPath))
		b.WriteString("Path:        ")
		b.WriteString(m.path)
		b.WriteByte('\n')
	}

	b.WriteString(m.rowPrefix(FieldScope))
	b.WriteString("Scope:       ")
	b.WriteString(m.ScopeLabel())
	b.WriteByte('\n')

	b.WriteString("\n(↑/↓ field, ←/→ value, <CR> export, <esc> cancel)")

	if m.bufferedThresholdExceeded && m.isBufferedFormat() {
		b.WriteString("\n\nWARNING: buffered format on ")
		if m.bufferedThresholdLabel != "" {
			b.WriteString(m.bufferedThresholdLabel)
		} else {
			b.WriteString("threshold")
		}
		b.WriteString(" — Confirm disabled.")
	}

	return b.String()
}

func (m *ExportMenu) rowPrefix(f ExportMenuField) string {
	if m.field == f {
		return "> "
	}
	return "  "
}
