package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestSchemasControllerHideCallsSchemasHelper(t *testing.T) {
	b := newBag()
	b.SchemaPicker.name = "public"
	b.Active.id = "local"
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag, cur, b.SchemaPicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'H') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("H: %v", err)
			}
		}
	}
	if len(b.Schemas.hideCalls) != 1 || b.Schemas.hideCalls[0] != (hideArgs{"local", "public"}) {
		t.Fatalf("hideCalls = %+v, want one (local, public)", b.Schemas.hideCalls)
	}
}

// AC: ErrNeedsConfirmation routes through ConfirmHelper.
func TestSchemasControllerUnhideOnErrNeedsConfirmationOpensConfirm(t *testing.T) {
	b := newBag()
	b.SchemaPicker.name = "audit"
	b.Active.id = "local"
	b.Schemas.unhideErr = data.ErrNeedsConfirmation
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag, cur, b.SchemaPicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'U') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("U: %v", err)
			}
		}
	}
	if len(b.Confirm.calls) != 1 {
		t.Fatalf("expected ConfirmHelper.Confirm to fire once on ErrNeedsConfirmation, got %d", len(b.Confirm.calls))
	}
	b.Schemas.unhideErr = nil
	if err := b.Confirm.calls[0].OnYes(); err != nil {
		t.Fatalf("OnYes: %v", err)
	}
	if len(b.Schemas.unhideCalls) != 2 {
		t.Fatalf("want 2 unhide calls (first w/ patterns, second w/o); got %d", len(b.Schemas.unhideCalls))
	}
	second := b.Schemas.unhideCalls[1]
	if second.Builtin != nil || second.Profile != nil {
		t.Fatalf("post-confirm UnhideSchema MUST pass nil builtin+profile; got builtin=%v profile=%v", second.Builtin, second.Profile)
	}
}

// AC: <leader>H is published as a 2-key chord (leader placeholder + H).
// The leader placeholder is expanded by keys.KeybindingService.Build at
// trie-insert time; the controller emits KeyLeader untouched.
func TestSchemasControllerLeaderHIsPublishedAsTwoKeyChord(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag, cur, b.SchemaPicker)

	found := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if len(kb.Sequence) == 2 &&
			kb.Sequence[0].Special == types.KeyLeader &&
			kb.Sequence[1].Code == 'H' {
			found = true
			if kb.ActionID != commands.SchemaToggleShowHidden {
				t.Fatalf("<leader>H ActionID = %q, want %q", kb.ActionID, commands.SchemaToggleShowHidden)
			}
		}
	}
	if !found {
		t.Fatal("SchemasController did not publish a <leader>H chord binding")
	}
}

// AC: SchemaToggleShowHidden handler delegates to picker.ToggleShowHidden.
func TestSchemasControllerToggleShowHiddenHandler(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag, cur, b.SchemaPicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, ok := reg.Get(commands.SchemaToggleShowHidden)
	if !ok || cmd == nil {
		t.Fatal("SchemaToggleShowHidden not registered")
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if b.SchemaPicker.toggleCount != 1 {
		t.Fatalf("ToggleShowHidden count = %d, want 1", b.SchemaPicker.toggleCount)
	}
}
