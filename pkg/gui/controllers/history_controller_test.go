package controllers

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// historyEditorBuffer is the EditorBufferReader test double for this
// internal-package test. It mirrors the controllers_test package's
// fakeEditorBuffer (which is unreachable from package controllers):
// Inserted records every InsertAtCursor argument in call order.
type historyEditorBuffer struct {
	Inserted []string
}

func (f *historyEditorBuffer) BufferText() string            { return "" }
func (f *historyEditorBuffer) CursorOffset() int             { return 0 }
func (f *historyEditorBuffer) SelectionText() (string, bool) { return "", false }
func (f *historyEditorBuffer) ReplaceAll(string) error       { return nil }
func (f *historyEditorBuffer) ReplaceSelection(string) error { return nil }
func (f *historyEditorBuffer) InsertAtCursor(text string) error {
	f.Inserted = append(f.Inserted, text)
	return nil
}

// newHistoryContext builds a HISTORY leaf context seeded with rows. SetReload
// with a no-op clears the initial stale flag so HandleFocus does not race the
// test (the context starts stale by design).
func newHistoryContext(rows []query.HistoryRow) *guicontext.HistoryContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.HistoryContextKey,
		ViewName: string(guicontext.HistoryContextKey),
		Kind:     types.MAIN_CONTEXT,
	})
	c := guicontext.NewHistoryContext(base, types.ContextTreeDeps{})
	c.SetRows(rows)
	return c
}

func TestHistoryConfirm_InsertsSelectedSQLAndSwitches(t *testing.T) {
	ctx := newHistoryContext([]query.HistoryRow{{SQL: "SELECT now()"}})
	buf := &historyEditorBuffer{}
	switches := 0
	c := NewHistoryController(nil, CoreDeps{}, ctx, buf, func() { switches++ })

	if err := c.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm err = %v", err)
	}
	if len(buf.Inserted) != 1 || buf.Inserted[0] != "SELECT now()" {
		t.Errorf("Inserted = %#v, want [\"SELECT now()\"]", buf.Inserted)
	}
	if switches != 1 {
		t.Errorf("tab switches = %d, want 1", switches)
	}
}

func TestHistoryClose_SwitchesNoInsert(t *testing.T) {
	ctx := newHistoryContext([]query.HistoryRow{{SQL: "SELECT now()"}})
	buf := &historyEditorBuffer{}
	switches := 0
	c := NewHistoryController(nil, CoreDeps{}, ctx, buf, func() { switches++ })

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

func TestHistoryConfirm_EmptyListIsNoOp(t *testing.T) {
	ctx := newHistoryContext(nil)
	buf := &historyEditorBuffer{}
	switches := 0
	c := NewHistoryController(nil, CoreDeps{}, ctx, buf, func() { switches++ })

	if err := c.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm err = %v", err)
	}
	if len(buf.Inserted) != 0 {
		t.Errorf("Inserted = %#v, want none", buf.Inserted)
	}
	if switches != 0 {
		t.Errorf("tab switches = %d, want 0 (empty selection is a no-op)", switches)
	}
}

func TestHistoryNavigation_ClampsAtBounds(t *testing.T) {
	ctx := newHistoryContext([]query.HistoryRow{
		{SQL: "a"}, {SQL: "b"}, {SQL: "c"},
	})
	c := NewHistoryController(nil, CoreDeps{}, ctx, &historyEditorBuffer{}, func() {})

	// Up at the top clamps to 0.
	if err := c.Up(commands.ExecCtx{}); err != nil {
		t.Fatalf("Up err = %v", err)
	}
	if ctx.Cursor() != 0 {
		t.Errorf("cursor after Up at top = %d, want 0", ctx.Cursor())
	}
	// Last then Down clamps at the final row.
	if err := c.Last(commands.ExecCtx{}); err != nil {
		t.Fatalf("Last err = %v", err)
	}
	if err := c.Down(commands.ExecCtx{}); err != nil {
		t.Fatalf("Down err = %v", err)
	}
	if ctx.Cursor() != 2 {
		t.Errorf("cursor after Down past end = %d, want 2", ctx.Cursor())
	}
	// First returns to the top.
	if err := c.First(commands.ExecCtx{}); err != nil {
		t.Fatalf("First err = %v", err)
	}
	if ctx.Cursor() != 0 {
		t.Errorf("cursor after First = %d, want 0", ctx.Cursor())
	}
}

func TestHistoryGetKeybindings_NavConfirmCloseOnly(t *testing.T) {
	ctx := newHistoryContext(nil)
	c := NewHistoryController(nil, CoreDeps{}, ctx, &historyEditorBuffer{}, func() {})
	got := c.GetKeybindings(types.KeybindingsOpts{})

	// Expected sequences: j, k, gg, G, the h/l/0/$ horizontal-pan bindings
	// shared by every list rail, <cr>, <esc>, plus the QUERY_RAIL `]`/`[`
	// tab-cycle pair. Exactly twelve bindings, no per-character or on-change
	// bindings.
	if len(got) != 12 {
		t.Fatalf("len(bindings) = %d, want 12 (j,k,gg,G,h,l,0,$,<cr>,<esc>,],[)", len(got))
	}

	for _, b := range got {
		if b.Scope != types.HISTORY {
			t.Errorf("binding %s scope = %s, want history", b.ActionID, b.Scope)
		}
	}

	seqKey := func(b *types.ChordBinding) string {
		s := ""
		for _, k := range b.Sequence {
			switch k.Special {
			case types.KeyEnter:
				s += "<cr>"
			case types.KeyEsc:
				s += "<esc>"
			default:
				s += string(k.Code)
			}
		}
		return s
	}
	seen := map[string]bool{}
	for _, b := range got {
		seen[seqKey(b)] = true
	}
	for _, want := range []string{"j", "k", "gg", "G", "h", "l", "0", "$", "<cr>", "<esc>", "]", "["} {
		if !seen[want] {
			t.Errorf("missing binding for sequence %q", want)
		}
	}

	// No <esc> binding may carry a confirm/insert action: assert the close
	// binding routes to HistoryClose, not the list-confirm action.
	for _, b := range got {
		if seqKey(b) == "<esc>" && b.ActionID != HistoryClose {
			t.Errorf("<esc> ActionID = %q, want %q", b.ActionID, HistoryClose)
		}
	}
}

func TestHistoryRegisterActions_ResolveThroughRegistry(t *testing.T) {
	reg := commands.NewRegistry()
	ctx := newHistoryContext(nil)
	c := NewHistoryController(nil, CoreDeps{}, ctx, &historyEditorBuffer{}, func() {})
	c.RegisterActions(reg)

	if !reg.Has(HistoryClose) {
		t.Errorf("registry missing action %q", HistoryClose)
	}
	for _, id := range []string{
		listActionID(commands.ListUp, viewName(types.HISTORY)),
		listActionID(commands.ListDown, viewName(types.HISTORY)),
		listActionID(commands.ListConfirm, viewName(types.HISTORY)),
		listActionID(commands.ListJumpFirst, viewName(types.HISTORY)),
		listActionID(commands.ListJumpLast, viewName(types.HISTORY)),
	} {
		if !reg.Has(id) {
			t.Errorf("registry missing trait action %q", id)
		}
	}
}

// TestHistoryHandleFocus_LoadsOnceWhenStale verifies the leaf reloads on its
// first activation (stale by construction) and not again until MarkStale.
func TestHistoryHandleFocus_LoadsOnceWhenStale(t *testing.T) {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.HistoryContextKey,
		ViewName: string(guicontext.HistoryContextKey),
		Kind:     types.MAIN_CONTEXT,
	})
	ctx := guicontext.NewHistoryContext(base, types.ContextTreeDeps{})
	reloads := 0
	ctx.SetReload(func() { reloads++ })

	// First focus: stale → reloads.
	_ = ctx.HandleFocus(types.OnFocusOpts{})
	if reloads != 1 {
		t.Fatalf("reloads after first focus = %d, want 1", reloads)
	}
	// Second focus: not stale → no reload.
	_ = ctx.HandleFocus(types.OnFocusOpts{})
	if reloads != 1 {
		t.Fatalf("reloads after second focus = %d, want 1 (not stale)", reloads)
	}
	// After MarkStale: reloads again.
	ctx.MarkStale()
	_ = ctx.HandleFocus(types.OnFocusOpts{})
	if reloads != 2 {
		t.Fatalf("reloads after MarkStale+focus = %d, want 2", reloads)
	}
}
