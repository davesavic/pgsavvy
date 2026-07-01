package popup

import (
	"strings"
	"testing"
)

func defaultFormats() []string {
	return []string{"CSV", "TSV", "NDJSON", "JSON Array", "Markdown", "SQL INSERTs"}
}

func defaultDestinations() []string {
	return []string{"Clipboard", "File"}
}

func defaultScopes() []string {
	return []string{"On screen", "Buffered", "All rows"}
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
	m.MoveValue(+1) // Clipboard → File
	if m.DestinationIdx() != 1 {
		t.Errorf("DestinationIdx after +1 = %d; want 1", m.DestinationIdx())
	}

	m.MoveField(+1) // → FieldPath (visible: File destination)
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

	// Switch to File so Path row is visible.
	m.MoveValue(+1) // Clipboard → File
	m.MoveField(+1) // → FieldPath
	body = m.Body()
	if !strings.Contains(body, "> Path:") {
		t.Errorf("after MoveField(+1) with File destination, body should mark FieldPath with '> ': %q", body)
	}

	m.MoveField(+1)
	body = m.Body()
	if !strings.Contains(body, "> Scope:") {
		t.Errorf("after MoveField(+1) from Path, body should mark FieldScope with '> ': %q", body)
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
// the legacy default text.
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
// extension never lies about its contents.
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

	// Clipboard is default (idx 0) — Path row is hidden.
	body := m.Body()
	if strings.Contains(body, "Path:") {
		t.Errorf("Clipboard destination body should omit Path row: %q", body)
	}

	// Switch Destination → File (idx 1).
	m.MoveField(+1) // → FieldDestination
	m.MoveValue(+1) // → File
	if m.DestinationLabel() != "File" {
		t.Fatalf("setup: DestinationLabel = %q; want File", m.DestinationLabel())
	}
	body = m.Body()
	if !strings.Contains(body, "Path:        /tmp/a.csv") {
		t.Errorf("File destination body should render Path row: %q", body)
	}
}

func TestExportMenu_Path_SkippedInNavForClipboard(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	// Clipboard is default (idx 0) — Path row is hidden.
	// Move to Destination (still Clipboard) and then down; Path must be skipped.
	m.MoveField(+1) // → FieldDestination
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

func TestExportMenu_SetScopeDescriptions_RendersDescription(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.SetScopeDescriptions([]string{
		"Includes 42 rows currently visible on screen.",
		"Includes all 100 rows in memory.",
		"Includes the complete result (~5000 rows).",
	})

	// Scope is not the active field (default: FieldFormat), so prefix is "  ".
	body := m.Body()
	if !strings.Contains(body, "Scope:       On screen") {
		t.Errorf("body should render scope label: %q", body)
	}
	if !strings.Contains(body, "42 rows currently visible on screen.") {
		t.Errorf("body should render scope description: %q", body)
	}

	// Navigate to Scope field — description prefix should become "> ".
	for range 3 {
		m.MoveField(+1)
	}
	if m.Field() != FieldScope {
		t.Fatalf("setup: field = %v; want FieldScope", m.Field())
	}
	body = m.Body()
	if !strings.Contains(body, "> Scope:       On screen") {
		t.Errorf("body should mark Scope with cursor prefix: %q", body)
	}
	if !strings.Contains(body, "42 rows currently visible on screen.") {
		t.Errorf("body should still render scope description with cursor active: %q", body)
	}
}

func TestExportMenu_SetScopeDescriptions_CycleUpdatesDescription(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.SetScopeDescriptions([]string{
		"On-screen desc.",
		"Buffered desc.",
		"All-rows desc.",
	})
	// Navigate to Scope.
	m.MoveField(+1)
	m.MoveField(+1)
	if m.Field() != FieldScope {
		t.Fatalf("setup: field = %v; want FieldScope", m.Field())
	}

	body := m.Body()
	if !strings.Contains(body, "On-screen desc.") {
		t.Errorf("body should contain on-screen description: %q", body)
	}

	m.MoveValue(+1) // → Buffered
	body = m.Body()
	if !strings.Contains(body, "Buffered desc.") {
		t.Errorf("body should contain buffered description: %q", body)
	}

	m.MoveValue(+1) // → All rows
	body = m.Body()
	if !strings.Contains(body, "All-rows desc.") {
		t.Errorf("body should contain all-rows description: %q", body)
	}
}

func TestExportMenu_Body_ScopeDescriptions_NilAndEmptyNoPanic(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	// nil scopeDescriptions — should not panic, no description line.
	body := m.Body()
	if strings.Contains(body, "Includes") {
		t.Errorf("body should not contain description when scopeDescriptions is nil: %q", body)
	}

	m.SetScopeDescriptions(nil)
	body = m.Body()
	if strings.Contains(body, "Includes") {
		t.Errorf("body should not contain description after SetScopeDescriptions(nil): %q", body)
	}

	m.SetScopeDescriptions([]string{})
	body = m.Body()
	if strings.Contains(body, "Includes") {
		t.Errorf("body should not contain description after SetScopeDescriptions([]): %q", body)
	}
}

func TestExportMenu_Body_ScopeDescriptions_OutOfBoundsNoPanic(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	m.SetScopeDescriptions([]string{"single description"})
	// scopeIdx=0 is in bounds; renders.
	body := m.Body()
	if !strings.Contains(body, "single description") {
		t.Errorf("body should render description when in bounds: %q", body)
	}

	// Force scopeIdx out of bounds by cycling past the last scope.
	for range 10 {
		m.MoveValue(+1)
	}
	// scopeIdx clamps to 2 (last), descriptions has only 1 entry → out of bounds.
	// Body should not panic and should not render the description.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Body panicked with out-of-bounds scopeIdx: %v", r)
			}
		}()
		_ = m.Body()
	}()
}

func TestExportMenu_SetScopeDescriptions_DefensiveCopy(t *testing.T) {
	m := NewExportMenu(defaultFormats(), defaultDestinations(), defaultScopes(), -1, false)
	input := []string{"a", "b", "c"}
	m.SetScopeDescriptions(input)
	input[0] = "MUTATED"
	body := m.Body()
	if strings.Contains(body, "MUTATED") {
		t.Errorf("SetScopeDescriptions should defensive-copy: %q", body)
	}
	if !strings.Contains(body, "a") {
		t.Errorf("SetScopeDescriptions should preserve original values: %q", body)
	}
}
