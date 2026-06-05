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
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	if m.Field() != FieldFormat {
		t.Errorf("default field = %v; want FieldFormat", m.Field())
	}
	if m.FormatIdx() != 0 || m.DestinationIdx() != 0 || m.ScopeIdx() != 0 {
		t.Errorf("default indexes = (%d,%d,%d); want (0,0,0)", m.FormatIdx(), m.DestinationIdx(), m.ScopeIdx())
	}
}

func TestExportMenu_MoveField_Clamps(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.MoveField(-1)
	if m.Field() != FieldFormat {
		t.Errorf("MoveField(-1) from FieldFormat = %v; want FieldFormat", m.Field())
	}
	m.MoveField(+1)
	m.MoveField(+1)
	m.MoveField(+1)
	if m.Field() != FieldScope {
		t.Fatalf("after +1,+1,+1 field = %v; want FieldScope", m.Field())
	}
	m.MoveField(+1)
	if m.Field() != FieldScope {
		t.Errorf("MoveField(+1) at FieldScope = %v; want FieldScope (clamp)", m.Field())
	}
}

func TestExportMenu_MoveValue_AdjustsCurrentField(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)

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
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)

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
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), 5, false)
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
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), 3, false)
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
	m := NewExportMenu(formats, defaultDestinations(), defaultScopes(), 5, false)
	// Force selection of the disabled row directly via internal state
	// would violate API; instead, exercise via the same path the caller
	// uses — there's no public setter, so verify the case where
	// initial selection coincidentally lands on it. Simulate by
	// constructing a single-format list where the only row is disabled.
	single := []string{"SQL INSERTs"}
	m2 := NewExportMenu(single, defaultDestinations(), defaultScopes(), 0, false)
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
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, true)
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

func TestExportMenu_Body_HighlightsCursorField(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)

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

	m.MoveField(+1) // → FieldPath (File destination, so Path is navigable)
	body = m.Body()
	if !strings.Contains(body, "> Path:") {
		t.Errorf("after second MoveField(+1), body should mark FieldPath with '> ': %q", body)
	}

	m.MoveField(+1)
	body = m.Body()
	if !strings.Contains(body, "> Scope:") {
		t.Errorf("after third MoveField(+1), body should mark FieldScope with '> ': %q", body)
	}
}

func TestExportMenu_Body_RendersDisabledSQLInsertsAnnotation(t *testing.T) {
	single := []string{"SQL INSERTs"}
	m := NewExportMenu(single, defaultDestinations(), defaultScopes(), 0, false)
	body := m.Body()
	if !strings.Contains(body, "disabled — result is not a single base table") {
		t.Errorf("body should annotate disabled SQL INSERTs row: %q", body)
	}
}

// TestExportMenu_Body_RendersF2DisabledReason verifies that when
// SetSQLInsertsDisabledReason is called (A8 wiring from GridView), the
// disabled annotation reflects the caller-supplied reason instead of
// the legacy default text. dbsavvy-bwq.11 (A8).
func TestExportMenu_Body_RendersF2DisabledReason(t *testing.T) {
	single := []string{"SQL INSERTs"}
	m := NewExportMenu(single, defaultDestinations(), defaultScopes(), 0, false)
	m.SetSQLInsertsDisabledReason("result spans multiple tables")
	body := m.Body()
	if !strings.Contains(body, "disabled: result spans multiple tables") {
		t.Errorf("body should annotate with F2-supplied reason: %q", body)
	}
	if strings.Contains(body, "not a single base table") {
		t.Errorf("body should not fall back to legacy text when reason is set: %q", body)
	}
	if got := m.ConfirmBlockedReason(); !strings.Contains(got, "result spans multiple tables") {
		t.Errorf("ConfirmBlockedReason should include F2 reason: %q", got)
	}
}

func TestExportMenu_Path_PrefillAndAccessors(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	if m.Path() != "" {
		t.Errorf("default Path = %q; want empty", m.Path())
	}
	m.Prefill("/tmp/a.csv")
	if m.Path() != "/tmp/a.csv" {
		t.Errorf("after Prefill, Path = %q; want /tmp/a.csv", m.Path())
	}
	// Format is CSV (default), so a .csv path is stored verbatim.
	m.SetPath("/tmp/custom.csv")
	if m.Path() != "/tmp/custom.csv" {
		t.Errorf("after SetPath, Path = %q; want /tmp/custom.csv", m.Path())
	}
}

func TestExportMenu_Path_SyncsExtensionWithFormat(t *testing.T) {
	// formats[0]=CSV; cycling to JSON Array (idx 3) should rewrite the
	// LAST extension only: a.old.csv → a.old.json.
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.Prefill("/tmp/a.old.csv")
	for range 3 {
		m.MoveValue(+1) // → JSON Array
	}
	if m.FormatLabel() != "JSON Array" {
		t.Fatalf("setup: FormatLabel = %q; want JSON Array", m.FormatLabel())
	}
	if m.Path() != "/tmp/a.old.json" {
		t.Errorf("Path after format cycle = %q; want /tmp/a.old.json", m.Path())
	}
}

// After the user edits the path, cycling the format must keep their
// basename but track the extension to the selected format — the file's
// extension never lies about its contents. dbsavvy-5tq0.
func TestExportMenu_Path_ExtensionFollowsFormatAfterSetPath(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.Prefill("/tmp/a.csv")
	m.SetPath("/tmp/keep.csv")
	for range 2 {
		m.MoveValue(+1) // CSV → TSV → NDJSON
	}
	if m.FormatLabel() != "NDJSON" {
		t.Fatalf("setup: FormatLabel = %q; want NDJSON", m.FormatLabel())
	}
	if m.Path() != "/tmp/keep.ndjson" {
		t.Errorf("Path after SetPath+format cycle = %q; want /tmp/keep.ndjson (basename kept, ext follows format)", m.Path())
	}
}

// Editing the path to a mismatched extension while a non-default format is
// selected normalises the extension immediately to the current format.
// dbsavvy-5tq0.
func TestExportMenu_Path_SetPathNormalisesExtensionToFormat(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.Prefill("/tmp/a.csv")
	for range 2 {
		m.MoveValue(+1) // → NDJSON
	}
	m.SetPath("/tmp/wstestexport.csv") // user types a stale .csv
	if m.Path() != "/tmp/wstestexport.ndjson" {
		t.Errorf("Path after SetPath = %q; want /tmp/wstestexport.ndjson (ext normalised to NDJSON)", m.Path())
	}
}

func TestExportMenu_Path_RenderedOnlyForFileDestination(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.Prefill("/tmp/a.csv")

	body := m.Body()
	if !strings.Contains(body, "Path:        /tmp/a.csv") {
		t.Errorf("File destination body should render Path row: %q", body)
	}

	// Switch Destination → Clipboard (idx 1).
	m.MoveField(+1) // → FieldDestination
	m.MoveValue(+1) // → Clipboard
	if m.DestinationLabel() != "Clipboard" {
		t.Fatalf("setup: DestinationLabel = %q; want Clipboard", m.DestinationLabel())
	}
	body = m.Body()
	if strings.Contains(body, "Path:") {
		t.Errorf("Clipboard destination body should omit Path row: %q", body)
	}
}

func TestExportMenu_Path_SkippedInNavForClipboard(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	// Move to Destination and select Clipboard.
	m.MoveField(+1) // → FieldDestination
	m.MoveValue(+1) // → Clipboard
	// Moving down must skip the hidden FieldPath and land on FieldScope.
	m.MoveField(+1)
	if m.Field() != FieldScope {
		t.Errorf("MoveField(+1) from Destination with Clipboard = %v; want FieldScope (Path skipped)", m.Field())
	}
	// Moving back up must skip FieldPath again, landing on Destination.
	m.MoveField(-1)
	if m.Field() != FieldDestination {
		t.Errorf("MoveField(-1) from Scope with Clipboard = %v; want FieldDestination (Path skipped)", m.Field())
	}
}

func TestExportMenu_Body_RendersWarningFooter(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, true)
	m.SetBufferedFormatIndexes(4, 3)
	m.SetBufferedThresholdLabel("≥ 10000 rows")
	for range 4 {
		m.MoveValue(+1)
	}
	body := m.Body()
	if !strings.Contains(body, "WARNING: buffered format on ≥ 10000 rows — Confirm disabled.") {
		t.Errorf("body should contain warning footer with threshold label: %q", body)
	}
}
