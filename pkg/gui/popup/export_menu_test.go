package popup

import (
	"strings"
	"testing"
)

func defaultFormats() []string {
	return []string{"CSV", "TSV", "NDJSON", "JSON Array", "Markdown", "SQL INSERTs"}
}

func defaultDestinations() []string {
	return []string{"File", "Clipboard"}
}

func defaultScopes() []string {
	return []string{"Loaded", "Full"}
}

func TestExportMenu_InitialState(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, false)
	if m.Field() != FieldFormat {
		t.Errorf("default field = %v; want FieldFormat", m.Field())
	}
	if m.FormatIdx() != 0 || m.DestinationIdx() != 0 || m.ScopeIdx() != 0 {
		t.Errorf("default indexes = (%d,%d,%d); want (0,0,0)", m.FormatIdx(), m.DestinationIdx(), m.ScopeIdx())
	}
}

func TestExportMenu_MoveField_Clamps(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, false)
	m.MoveField(-1)
	if m.Field() != FieldFormat {
		t.Errorf("MoveField(-1) from FieldFormat = %v; want FieldFormat", m.Field())
	}
	m.MoveField(+1)
	m.MoveField(+1)
	if m.Field() != FieldScope {
		t.Fatalf("after +1,+1 field = %v; want FieldScope", m.Field())
	}
	m.MoveField(+1)
	if m.Field() != FieldScope {
		t.Errorf("MoveField(+1) at FieldScope = %v; want FieldScope (clamp)", m.Field())
	}
}

func TestExportMenu_MoveValue_AdjustsCurrentField(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, false)

	m.MoveValue(+1)
	if m.FormatIdx() != 1 {
		t.Errorf("FormatIdx after +1 = %d; want 1", m.FormatIdx())
	}

	m.MoveField(+1) // → FieldDestination
	m.MoveValue(+1)
	if m.DestinationIdx() != 1 {
		t.Errorf("DestinationIdx after +1 = %d; want 1", m.DestinationIdx())
	}

	m.MoveField(+1) // → FieldScope
	m.MoveValue(+1)
	if m.ScopeIdx() != 1 {
		t.Errorf("ScopeIdx after +1 = %d; want 1", m.ScopeIdx())
	}
}

func TestExportMenu_MoveValue_ClampsAtBounds(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, false)

	m.MoveValue(-5)
	if m.FormatIdx() != 0 {
		t.Errorf("FormatIdx after -5 = %d; want 0", m.FormatIdx())
	}

	m.MoveValue(+999)
	if m.FormatIdx() != len(defaultFormats())-1 {
		t.Errorf("FormatIdx after +999 = %d; want %d", m.FormatIdx(), len(defaultFormats())-1)
	}
}

func TestExportMenu_SkipsDisabledSQLInserts_LastIndexClamps(t *testing.T) {
	// sqlInsertsIdx=5 (last); from formatIdx=4, MoveValue(+1) cannot
	// advance past the disabled last row → clamp at 4.
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), 5, false, false)
	// advance to idx=4
	for range 4 {
		m.MoveValue(+1)
	}
	if m.FormatIdx() != 4 {
		t.Fatalf("setup: FormatIdx = %d; want 4", m.FormatIdx())
	}
	m.MoveValue(+1)
	if m.FormatIdx() != 4 {
		t.Errorf("FormatIdx after +1 with disabled last = %d; want 4 (clamp before disabled)", m.FormatIdx())
	}
}

func TestExportMenu_SkipsDisabledSQLInserts_MiddleIndexSkips(t *testing.T) {
	// sqlInsertsIdx=3 (middle); from formatIdx=2 MoveValue(+1) skips
	// disabled idx=3 and lands on idx=4.
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), 3, false, false)
	for range 2 {
		m.MoveValue(+1)
	}
	if m.FormatIdx() != 2 {
		t.Fatalf("setup: FormatIdx = %d; want 2", m.FormatIdx())
	}
	m.MoveValue(+1)
	if m.FormatIdx() != 4 {
		t.Errorf("FormatIdx after +1 across disabled middle = %d; want 4", m.FormatIdx())
	}
}

func TestExportMenu_ConfirmBlockedWhenSQLInsertsDisabled(t *testing.T) {
	formats := defaultFormats()
	m := NewExportMenu(formats, defaultDestinations(), defaultScopes(), 5, false, false)
	// Force selection of the disabled row directly via internal state
	// would violate API; instead, exercise via the same path the caller
	// uses — there's no public setter, so verify the case where
	// initial selection coincidentally lands on it. Simulate by
	// constructing a single-format list where the only row is disabled.
	single := []string{"SQL INSERTs"}
	m2 := NewExportMenu(single, defaultDestinations(), defaultScopes(), 0, false, false)
	if !m2.IsSQLInsertsSelected() {
		t.Fatalf("with single disabled row, IsSQLInsertsSelected should be true")
	}
	if m2.ConfirmBlockedReason() == "" {
		t.Errorf("ConfirmBlockedReason should be non-empty when SQL INSERTs disabled")
	}

	// And in the multi-format case, the default selection (idx=0=CSV)
	// is NOT the disabled row.
	if m.IsSQLInsertsSelected() {
		t.Errorf("default selection should not be SQL INSERTs")
	}
	if m.ConfirmBlockedReason() != "" {
		t.Errorf("ConfirmBlockedReason at default = %q; want empty", m.ConfirmBlockedReason())
	}
}

func TestExportMenu_ConfirmBlockedWhenBufferedThresholdExceeded(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, true, false)
	m.SetBufferedFormatIndexes(4 /*markdown*/, 3 /*jsonArray*/)

	// Advance to Markdown (idx=4).
	for range 4 {
		m.MoveValue(+1)
	}
	if m.FormatIdx() != 4 {
		t.Fatalf("setup: FormatIdx = %d; want 4", m.FormatIdx())
	}
	if m.ConfirmBlockedReason() == "" {
		t.Errorf("ConfirmBlockedReason should be non-empty for buffered format over threshold")
	}

	// Move back to JSON Array (idx=3) — still buffered.
	m.MoveValue(-1)
	if m.FormatIdx() != 3 {
		t.Fatalf("setup: FormatIdx = %d; want 3", m.FormatIdx())
	}
	if m.ConfirmBlockedReason() == "" {
		t.Errorf("ConfirmBlockedReason should be non-empty for JSON Array over threshold")
	}

	// Move to NDJSON (idx=2) — streaming, not blocked by threshold.
	m.MoveValue(-1)
	if m.FormatIdx() != 2 {
		t.Fatalf("setup: FormatIdx = %d; want 2", m.FormatIdx())
	}
	if m.ConfirmBlockedReason() != "" {
		t.Errorf("ConfirmBlockedReason at NDJSON = %q; want empty", m.ConfirmBlockedReason())
	}
}

func TestExportMenu_RequiresFullWithFilterConfirmation(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, true)
	// Default scope is "Loaded" (idx=0) → no confirmation needed.
	if m.RequiresFullWithFilterConfirmation() {
		t.Errorf("RequiresFullWithFilterConfirmation at Loaded = true; want false")
	}

	// Move scope to Full.
	m.MoveField(+1) // dest
	m.MoveField(+1) // scope
	m.MoveValue(+1) // Full
	if m.ScopeLabel() != "Full" {
		t.Fatalf("setup: ScopeLabel = %q; want Full", m.ScopeLabel())
	}
	if !m.RequiresFullWithFilterConfirmation() {
		t.Errorf("RequiresFullWithFilterConfirmation at Full + filterActive + !confirmed = false; want true")
	}
	if m.ConfirmBlockedReason() == "" {
		t.Errorf("ConfirmBlockedReason should be non-empty when full-with-filter unconfirmed")
	}
}

func TestExportMenu_ConfirmedFullWithFilter_ClearsRequirement(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, true)
	m.MoveField(+1)
	m.MoveField(+1)
	m.MoveValue(+1)
	if !m.RequiresFullWithFilterConfirmation() {
		t.Fatalf("setup: should require confirmation")
	}
	m.SetConfirmedFullWithFilter(true)
	if m.RequiresFullWithFilterConfirmation() {
		t.Errorf("RequiresFullWithFilterConfirmation after SetConfirmedFullWithFilter(true) = true; want false")
	}
	if m.ConfirmBlockedReason() != "" {
		t.Errorf("ConfirmBlockedReason after confirm = %q; want empty", m.ConfirmBlockedReason())
	}
}

func TestExportMenu_Body_HighlightsCursorField(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, false)

	body := m.Body()
	if !strings.Contains(body, "> Format:") {
		t.Errorf("body should mark FieldFormat with '> ': %q", body)
	}
	if !strings.Contains(body, "  Destination:") {
		t.Errorf("body should mark non-cursor rows with '  ': %q", body)
	}

	m.MoveField(+1)
	body = m.Body()
	if !strings.Contains(body, "> Destination:") {
		t.Errorf("after MoveField(+1), body should mark FieldDestination with '> ': %q", body)
	}
	if !strings.Contains(body, "  Format:") {
		t.Errorf("after MoveField(+1), Format row should be unmarked: %q", body)
	}

	m.MoveField(+1)
	body = m.Body()
	if !strings.Contains(body, "> Scope:") {
		t.Errorf("after second MoveField(+1), body should mark FieldScope with '> ': %q", body)
	}
}

func TestExportMenu_Body_RendersDisabledSQLInsertsAnnotation(t *testing.T) {
	single := []string{"SQL INSERTs"}
	m := NewExportMenu(single, defaultDestinations(), defaultScopes(), 0, false, false)
	body := m.Body()
	if !strings.Contains(body, "disabled — result is not a single base table") {
		t.Errorf("body should annotate disabled SQL INSERTs row: %q", body)
	}
}

func TestExportMenu_Body_RendersWarningFooter(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, true, false)
	m.SetBufferedFormatIndexes(4, 3)
	m.SetBufferedThresholdLabel("≥ 10000 rows")
	for range 4 {
		m.MoveValue(+1)
	}
	body := m.Body()
	if !strings.Contains(body, "WARNING: buffered format on ≥ 10000 rows — Confirm disabled.") {
		t.Errorf("body should contain warning footer with threshold label: %q", body)
	}

	// And full-with-filter footer.
	m2 := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false, true)
	m2.MoveField(+1)
	m2.MoveField(+1)
	m2.MoveValue(+1)
	body2 := m2.Body()
	if !strings.Contains(body2, "Full scope ignores your filter — press y to confirm") {
		t.Errorf("body should contain full-with-filter footer: %q", body2)
	}
}
