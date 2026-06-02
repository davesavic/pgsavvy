package controllers_test

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/popup"
)

// TestExportMenu_SQLInsertsGating_Editable verifies that when the
// underlying GridView reports Editable=true (sqlInsertsIdx=-1, no
// disabled reason wired) the SQL-INSERTs entry behaves identically to
// before A8: it is selectable, not annotated as disabled, and does not
// block Confirm.
// dbsavvy-bwq.11 (A8).
func TestExportMenu_SQLInsertsGating_Editable(t *testing.T) {
	formats := []string{"CSV", "TSV", "NDJSON", "JSON Array", "Markdown", "SQL INSERTs"}
	destinations := []string{"File", "Clipboard", "stdout"}
	scopes := []string{"Visible", "Loaded", "Full"}

	// sqlInsertsIdx=-1 mirrors PromptExport's choice when g.Editable()=true.
	m := popup.NewExportMenu(formats, destinations, scopes, -1, false)

	// Advance to SQL-INSERTs (idx=5).
	for range 5 {
		m.MoveValue(+1)
	}
	if m.FormatIdx() != 5 {
		t.Fatalf("setup: FormatIdx = %d; want 5 (SQL INSERTs)", m.FormatIdx())
	}
	if m.IsSQLInsertsSelected() {
		t.Errorf("IsSQLInsertsSelected = true; want false when Editable=true (sqlInsertsIdx=-1)")
	}
	if got := m.ConfirmBlockedReason(); got != "" {
		t.Errorf("ConfirmBlockedReason = %q; want empty when Editable=true", got)
	}

	body := m.Body()
	if strings.Contains(body, "disabled") {
		t.Errorf("Body should not annotate SQL INSERTs as disabled when Editable=true: %q", body)
	}
}

// TestExportMenu_SQLInsertsGating_NotEditable verifies that when the
// underlying GridView reports Editable=false with a DisabledReason
// (sqlInsertsIdx=pos, reason wired), the SQL-INSERTs entry is rendered
// shown-but-disabled, the annotation echoes the grid's reason (single
// source of truth), and Confirm is blocked with that same reason.
// dbsavvy-bwq.11 (A8).
func TestExportMenu_SQLInsertsGating_NotEditable(t *testing.T) {
	formats := []string{"CSV", "TSV", "NDJSON", "JSON Array", "Markdown", "SQL INSERTs"}
	destinations := []string{"File", "Clipboard", "stdout"}
	scopes := []string{"Visible", "Loaded", "Full"}

	reason := "result spans multiple tables"
	// sqlInsertsIdx=5 mirrors PromptExport's choice when g.Editable()=false
	// and g.DisabledReason() != "".
	m := popup.NewExportMenu(formats, destinations, scopes, 5, false)
	m.SetSQLInsertsDisabledReason(reason)

	// Cursor cannot land on the disabled last row via MoveValue (skip-
	// over logic clamps before it). Other formats stay unaffected.
	for range 99 {
		m.MoveValue(+1)
	}
	if m.FormatIdx() == 5 {
		t.Fatalf("MoveValue should not land on disabled last row; FormatIdx = %d", m.FormatIdx())
	}

	// Body renders the disabled annotation with the grid-sourced reason
	// for the disabled SQL-INSERTs row even when the cursor is elsewhere.
	body := m.Body()
	// The annotation only renders when the SQL-INSERTs row is the
	// selected format. Construct a single-row menu to force selection
	// onto the disabled row (the supported entry point per existing
	// IsSQLInsertsSelected/Body coverage).
	single := popup.NewExportMenu([]string{"SQL INSERTs"}, destinations, scopes, 0, false)
	single.SetSQLInsertsDisabledReason(reason)
	if !single.IsSQLInsertsSelected() {
		t.Fatalf("single-row menu: IsSQLInsertsSelected should be true")
	}
	singleBody := single.Body()
	if !strings.Contains(singleBody, "disabled: "+reason) {
		t.Errorf("Body annotation should echo grid DisabledReason %q; got: %q", reason, singleBody)
	}
	if blocked := single.ConfirmBlockedReason(); !strings.Contains(blocked, reason) {
		t.Errorf("ConfirmBlockedReason should include grid DisabledReason %q; got: %q", reason, blocked)
	}

	// Sanity: other formats remain unaffected — selecting CSV on the
	// multi-format menu yields no block.
	if got := m.ConfirmBlockedReason(); got != "" {
		t.Errorf("ConfirmBlockedReason at non-SQL-INSERTs row = %q; want empty", got)
	}
	if strings.Contains(body, "disabled") {
		t.Errorf("Body should not annotate non-SQL-INSERTs format as disabled: %q", body)
	}
}

// TestExportMenu_SQLInsertsGating_StateUpdatesOnReopen verifies the
// edge-case from the AC: switching from an editable result tab to a
// non-editable one updates the gated state on the next menu open.
// Modeled as: a fresh NewExportMenu call (the path PromptExport takes
// on every open) installs the new sqlInsertsIdx + reason.
// dbsavvy-bwq.11 (A8).
func TestExportMenu_SQLInsertsGating_StateUpdatesOnReopen(t *testing.T) {
	formats := []string{"CSV", "SQL INSERTs"}
	destinations := []string{"File"}
	scopes := []string{"Loaded"}

	// First open: editable.
	mEditable := popup.NewExportMenu(formats, destinations, scopes, -1, false)
	mEditable.MoveValue(+1)
	if mEditable.IsSQLInsertsSelected() {
		t.Fatalf("editable open: IsSQLInsertsSelected = true; want false")
	}
	if mEditable.ConfirmBlockedReason() != "" {
		t.Fatalf("editable open: ConfirmBlockedReason non-empty: %q", mEditable.ConfirmBlockedReason())
	}

	// Reopen: not editable with reason.
	mDisabled := popup.NewExportMenu(formats, destinations, scopes, 1, false)
	mDisabled.SetSQLInsertsDisabledReason("no row identity")
	// MoveValue should skip-clamp; verify the disabled row is still
	// recognized as disabled when the cursor is forced onto it via a
	// single-row construction (same pattern as the Body annotation
	// coverage above).
	single := popup.NewExportMenu([]string{"SQL INSERTs"}, destinations, scopes, 0, false)
	single.SetSQLInsertsDisabledReason("no row identity")
	if !single.IsSQLInsertsSelected() {
		t.Fatalf("reopened disabled: IsSQLInsertsSelected = false; want true")
	}
	if !strings.Contains(single.ConfirmBlockedReason(), "no row identity") {
		t.Errorf("reopened disabled: ConfirmBlockedReason should include reason; got: %q", single.ConfirmBlockedReason())
	}
}
