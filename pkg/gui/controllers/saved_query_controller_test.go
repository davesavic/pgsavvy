package controllers

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// savedEditorBuffer is the EditorBufferReader test double mirroring
// historyEditorBuffer.
type savedEditorBuffer struct {
	Inserted []string
}

func (f *savedEditorBuffer) BufferText() string            { return "" }
func (f *savedEditorBuffer) CursorOffset() int             { return 0 }
func (f *savedEditorBuffer) SelectionText() (string, bool) { return "", false }
func (f *savedEditorBuffer) ReplaceAll(string) error       { return nil }
func (f *savedEditorBuffer) ReplaceSelection(string) error { return nil }
func (f *savedEditorBuffer) InsertAtCursor(text string) error {
	f.Inserted = append(f.Inserted, text)
	return nil
}

// savedConfirmCall records one Confirm invocation's callbacks so the test can
// drive onYes/onNo explicitly (the fake never auto-runs them).
type savedConfirmCall struct {
	OnYes func() error
	OnNo  func() error
}

// savedFakeConfirm is the internal-package ConfirmHelper double (the
// controllers_test fakeConfirm is unreachable from package controllers).
type savedFakeConfirm struct {
	calls []savedConfirmCall
}

func (f *savedFakeConfirm) Confirm(_, _ string, onYes, onNo func() error) error {
	f.calls = append(f.calls, savedConfirmCall{OnYes: onYes, OnNo: onNo})
	return nil
}
func (f *savedFakeConfirm) Yes() error { return nil }
func (f *savedFakeConfirm) No() error  { return nil }

func newSavedQueryContext(rows []models.SavedQuery) *guicontext.SavedQueryContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.SavedQueryContextKey,
		ViewName: string(guicontext.SavedQueryContextKey),
		Kind:     types.MAIN_CONTEXT,
	})
	c := guicontext.NewSavedQueryContext(base, types.ContextTreeDeps{})
	c.SetRows(rows)
	return c
}

func TestSavedQueryConfirm_InsertsSelectedSQLAndSwitches(t *testing.T) {
	ctx := newSavedQueryContext([]models.SavedQuery{{Name: "a", SQL: "SELECT 1"}})
	buf := &savedEditorBuffer{}
	switches := 0
	c := NewSavedQueryController(nil, CoreDeps{}, UIDeps{}, ctx, buf, func() { switches++ }, nil, "")

	if err := c.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm err = %v", err)
	}
	if len(buf.Inserted) != 1 || buf.Inserted[0] != "SELECT 1" {
		t.Errorf("Inserted = %#v, want [\"SELECT 1\"]", buf.Inserted)
	}
	if switches != 1 {
		t.Errorf("tab switches = %d, want 1", switches)
	}
}

func TestSavedQueryClose_SwitchesNoInsert(t *testing.T) {
	ctx := newSavedQueryContext([]models.SavedQuery{{Name: "a", SQL: "SELECT 1"}})
	buf := &savedEditorBuffer{}
	switches := 0
	c := NewSavedQueryController(nil, CoreDeps{}, UIDeps{}, ctx, buf, func() { switches++ }, nil, "")

	if err := c.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close err = %v", err)
	}
	if switches != 1 {
		t.Errorf("tab switches = %d, want 1", switches)
	}
	if len(buf.Inserted) != 0 {
		t.Errorf("Inserted = %#v, want none", buf.Inserted)
	}
}

// seedQueries writes a queries.yml with the supplied rows to an in-memory fs
// and returns (fs, path).
func seedQueries(t *testing.T, rows []models.SavedQuery) (afero.Fs, string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	const path = "/queries.yml"
	for _, r := range rows {
		if err := config.UpsertQuery(fs, path, r); err != nil {
			t.Fatalf("seed UpsertQuery(%q) err = %v", r.Name, err)
		}
	}
	return fs, path
}

// TestSavedQueryDelete_ConfirmRemovesAndClampsCursor: confirming the dd gate
// (invoking the recorded onYes) writes the delete to queries.yml and clamps
// the cursor (never resets to 0).
func TestSavedQueryDelete_ConfirmRemovesAndClampsCursor(t *testing.T) {
	rows := []models.SavedQuery{
		{Name: "a", SQL: "SELECT 1"},
		{Name: "b", SQL: "SELECT 2"},
		{Name: "c", SQL: "SELECT 3"},
	}
	fs, path := seedQueries(t, rows)
	ctx := newSavedQueryContext(rows)
	ctx.SetCursor(2) // last row "c"

	confirm := &savedFakeConfirm{}
	c := NewSavedQueryController(nil, CoreDeps{}, UIDeps{Confirm: confirm}, ctx, &savedEditorBuffer{}, func() {}, fs, path)

	got, ok := commandHandler(t, c)
	if !ok {
		t.Fatal("delete action not registered")
	}
	if err := got(commands.ExecCtx{}); err != nil {
		t.Fatalf("delete handler err = %v", err)
	}
	// The Confirm gate was reached but onYes not yet run: queries.yml intact.
	if len(confirm.calls) != 1 {
		t.Fatalf("Confirm calls = %d, want 1", len(confirm.calls))
	}
	remaining, _ := config.LoadQueries(fs, path)
	if len(remaining) != 3 {
		t.Fatalf("before onYes: rows on disk = %d, want 3 (no premature delete)", len(remaining))
	}

	// Run onYes (user confirmed) → on-disk delete + cursor clamp.
	if err := confirm.calls[0].OnYes(); err != nil {
		t.Fatalf("onYes err = %v", err)
	}
	remaining, _ = config.LoadQueries(fs, path)
	if len(remaining) != 2 {
		t.Fatalf("after onYes: rows on disk = %d, want 2", len(remaining))
	}
	for _, r := range remaining {
		if r.Name == "c" {
			t.Errorf("row %q still present after delete", r.Name)
		}
	}
	// Cursor was at 2 (the deleted last row) → clamped to new last index 1.
	if ctx.Cursor() != 1 {
		t.Errorf("cursor after delete = %d, want 1 (clamped, not reset to 0)", ctx.Cursor())
	}
}

// TestSavedQueryDelete_CancelNeverWrites: a cancelled confirm (onYes never
// invoked) must NOT call config.DeleteQuery — queries.yml stays intact.
func TestSavedQueryDelete_CancelNeverWrites(t *testing.T) {
	rows := []models.SavedQuery{{Name: "a", SQL: "SELECT 1"}}
	fs, path := seedQueries(t, rows)
	ctx := newSavedQueryContext(rows)

	confirm := &savedFakeConfirm{}
	c := NewSavedQueryController(nil, CoreDeps{}, UIDeps{Confirm: confirm}, ctx, &savedEditorBuffer{}, func() {}, fs, path)

	got, ok := commandHandler(t, c)
	if !ok {
		t.Fatal("delete action not registered")
	}
	if err := got(commands.ExecCtx{}); err != nil {
		t.Fatalf("delete handler err = %v", err)
	}
	// onYes is NEVER invoked (user cancelled). queries.yml must be untouched.
	remaining, _ := config.LoadQueries(fs, path)
	if len(remaining) != 1 {
		t.Errorf("rows on disk after cancel = %d, want 1 (no delete)", len(remaining))
	}
}

// TestSavedQueryDelete_EmptySelectionNoConfirm: dd on an empty list never
// reaches the Confirm gate.
func TestSavedQueryDelete_EmptySelectionNoConfirm(t *testing.T) {
	ctx := newSavedQueryContext(nil)
	confirm := &savedFakeConfirm{}
	c := NewSavedQueryController(nil, CoreDeps{}, UIDeps{Confirm: confirm}, ctx, &savedEditorBuffer{}, func() {}, afero.NewMemMapFs(), "/queries.yml")

	got, ok := commandHandler(t, c)
	if !ok {
		t.Fatal("delete action not registered")
	}
	if err := got(commands.ExecCtx{}); err != nil {
		t.Fatalf("delete handler err = %v", err)
	}
	if len(confirm.calls) != 0 {
		t.Errorf("Confirm calls = %d, want 0 (empty selection)", len(confirm.calls))
	}
}

// TestSavedQueryHandleFocus_LoadsOnceWhenStale mirrors the history leaf:
// reload fires on first activation, then only after MarkStale.
func TestSavedQueryHandleFocus_LoadsOnceWhenStale(t *testing.T) {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.SavedQueryContextKey,
		ViewName: string(guicontext.SavedQueryContextKey),
		Kind:     types.MAIN_CONTEXT,
	})
	ctx := guicontext.NewSavedQueryContext(base, types.ContextTreeDeps{})
	reloads := 0
	ctx.SetReload(func() { reloads++ })

	_ = ctx.HandleFocus(types.OnFocusOpts{})
	if reloads != 1 {
		t.Fatalf("reloads after first focus = %d, want 1", reloads)
	}
	_ = ctx.HandleFocus(types.OnFocusOpts{})
	if reloads != 1 {
		t.Fatalf("reloads after second focus = %d, want 1 (not stale)", reloads)
	}
	ctx.MarkStale()
	_ = ctx.HandleFocus(types.OnFocusOpts{})
	if reloads != 2 {
		t.Fatalf("reloads after MarkStale+focus = %d, want 2", reloads)
	}
}

// commandHandler registers the saved-query controller's actions and returns
// the QuerySavedDelete handler.
func commandHandler(t *testing.T, c *SavedQueryController) (func(commands.ExecCtx) error, bool) {
	t.Helper()
	reg := commands.NewRegistry()
	c.RegisterActions(reg)
	cmd, ok := reg.Get(commands.QuerySavedDelete)
	if !ok {
		return nil, false
	}
	return cmd.Handler, true
}
