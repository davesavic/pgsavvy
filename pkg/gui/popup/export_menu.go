package popup

import "strings"

// ExportMenuField identifies the currently-focused selector row.
type ExportMenuField int

const (
	FieldFormat ExportMenuField = iota
	FieldDestination
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
//
// dbsavvy-uv0.9.
type ExportMenu struct {
	formats      []string
	destinations []string
	scopes       []string

	formatIdx int
	destIdx   int
	scopeIdx  int
	field     ExportMenuField

	// sqlInsertsIdx is the index into formats[] of the "SQL INSERTs" row
	// when shown-but-disabled; -1 when the row should not be rendered as
	// disabled (i.e. row identity is present and SQL INSERTs is fully
	// enabled, OR the row is absent entirely).
	sqlInsertsIdx int

	// markdownIdx and jsonArrayIdx track which format indexes are
	// "buffered" so the menu knows when to hard-block Confirm under
	// bufferedThresholdExceeded. -1 when not registered.
	markdownIdx  int
	jsonArrayIdx int

	bufferedThresholdExceeded bool
	bufferedThresholdLabel    string

	filterActive            bool
	confirmedFullWithFilter bool

	// scopeFullIdx is the index of the "Full" scope in scopes[];
	// resolved at construction by matching label.
	scopeFullIdx int
}

// NewExportMenu constructs the state. formats[] should already include or
// exclude "SQL INSERTs" based on HasRowIdentity. sqlInsertsIdx is the
// position of "SQL INSERTs" in formats[] when shown-but-disabled (i.e., the
// caller WANTS the label visible-but-grey); pass -1 to indicate the row
// should not be rendered at all.
func NewExportMenu(formats, destinations, scopes []string, sqlInsertsIdx int, bufferedThresholdExceeded, filterActive bool) *ExportMenu {
	fullIdx := -1
	for i, s := range scopes {
		if s == "Full" {
			fullIdx = i
			break
		}
	}
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
		filterActive:              filterActive,
		scopeFullIdx:              fullIdx,
	}
}

// Field returns the currently-focused selector row.
func (m *ExportMenu) Field() ExportMenuField { return m.field }

// MoveField adjusts the field cursor by d, clamping to [FieldFormat, FieldScope].
func (m *ExportMenu) MoveField(d int) {
	n := int(m.field) + d
	n = max(n, int(FieldFormat))
	n = min(n, int(FieldScope))
	m.field = ExportMenuField(n)
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
	case FieldDestination:
		m.destIdx = clamp(m.destIdx+d, 0, len(m.destinations)-1)
	case FieldScope:
		m.scopeIdx = clamp(m.scopeIdx+d, 0, len(m.scopes)-1)
	}
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

// SetConfirmedFullWithFilter records that the caller has collected the
// typed-YES confirmation for Full-scope + active-filter export.
func (m *ExportMenu) SetConfirmedFullWithFilter(b bool) {
	m.confirmedFullWithFilter = b
}

// RequiresFullWithFilterConfirmation reports whether the menu needs the
// caller to collect a typed-YES confirmation before Confirm.
func (m *ExportMenu) RequiresFullWithFilterConfirmation() bool {
	if !m.filterActive {
		return false
	}
	if m.scopeFullIdx < 0 || m.scopeIdx != m.scopeFullIdx {
		return false
	}
	return !m.confirmedFullWithFilter
}

// ConfirmBlockedReason returns "" when Confirm is allowed; otherwise a
// short human-readable reason. Conditions blocking Confirm:
//   - SQL-INSERTs selected when disabled.
//   - bufferedThresholdExceeded AND format is Markdown or JSON Array.
//   - filterActive AND scope == Full AND typed-YES not collected.
func (m *ExportMenu) ConfirmBlockedReason() string {
	if m.IsSQLInsertsSelected() {
		return "SQL INSERTs disabled — result is not a single base table"
	}
	if m.bufferedThresholdExceeded && m.isBufferedFormat() {
		return "buffered format over threshold — pick a streaming format"
	}
	if m.RequiresFullWithFilterConfirmation() {
		return "Full scope with active filter requires typed confirmation"
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
		b.WriteString("  (disabled — result is not a single base table)")
	}
	b.WriteByte('\n')

	b.WriteString(m.rowPrefix(FieldDestination))
	b.WriteString("Destination: ")
	b.WriteString(m.DestinationLabel())
	b.WriteByte('\n')

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

	if m.RequiresFullWithFilterConfirmation() {
		b.WriteString("\n\nFull scope ignores your filter — press y to confirm (or change Scope).")
	}

	return b.String()
}

func (m *ExportMenu) rowPrefix(f ExportMenuField) string {
	if m.field == f {
		return "> "
	}
	return "  "
}
