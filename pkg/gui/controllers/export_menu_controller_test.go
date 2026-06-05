package controllers_test

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeExportMenuMgr records ExportMenuManager dispatches for the
// controller binding tests.
type fakeExportMenuMgr struct {
	editPathCalls int
}

func (f *fakeExportMenuMgr) ExportMenuMoveField(int) {}
func (f *fakeExportMenuMgr) ExportMenuMoveValue(int) {}
func (f *fakeExportMenuMgr) ExportMenuConfirm()      {}
func (f *fakeExportMenuMgr) ExportMenuCancel()       {}
func (f *fakeExportMenuMgr) ExportMenuEditPath()     { f.editPathCalls++ }

// TestExportMenuController_IBindsEditPath asserts the 'i' rune dispatches
// ExportMenuEditPath in EXPORT_MENU/Normal scope.
func TestExportMenuController_IBindsEditPath(t *testing.T) {
	ctrl := controllers.NewExportMenuController(nil, controllers.CoreDeps{}, nil)
	found := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.EXPORT_MENU || len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Code == 'i' && kb.Mode == types.ModeNormal && kb.ActionID == commands.ExportMenuEditPath {
			found = true
		}
	}
	if !found {
		t.Fatal("EXPORT_MENU 'i' binding -> ExportMenuEditPath missing")
	}
}

// TestExportMenuController_EditPathDispatches asserts EditPath delegates to
// the manager.
func TestExportMenuController_EditPathDispatches(t *testing.T) {
	mgr := &fakeExportMenuMgr{}
	ctrl := controllers.NewExportMenuController(nil, controllers.CoreDeps{}, mgr)
	if err := ctrl.EditPath(commands.ExecCtx{}); err != nil {
		t.Fatalf("EditPath returned error: %v", err)
	}
	if mgr.editPathCalls != 1 {
		t.Fatalf("ExportMenuEditPath called %d times; want 1", mgr.editPathCalls)
	}
}

// TestExportMenu_IsPathFieldActive verifies the predicate is true only when
// the Path row is the cursor AND the destination is File.
func TestExportMenu_IsPathFieldActive(t *testing.T) {
	m := popup.NewExportMenu([]string{"CSV"}, []string{"File", "Clipboard"}, []string{"Loaded"}, -1, false)
	if m.IsPathFieldActive() {
		t.Error("IsPathFieldActive true on FieldFormat")
	}
	m.MoveField(+1) // Destination
	m.MoveField(+1) // Path
	if !m.IsPathFieldActive() {
		t.Error("IsPathFieldActive false on FieldPath/File")
	}
	// Switch destination to Clipboard (from the Destination row): Path is
	// no longer File-only-visible, so the predicate must be false.
	m.MoveField(-1) // back to Destination
	m.MoveValue(+1) // Clipboard
	m.MoveField(+1) // would-be Path; skips since Path hidden under Clipboard
	if m.IsPathFieldActive() {
		t.Error("IsPathFieldActive true after switching to Clipboard")
	}
}

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
