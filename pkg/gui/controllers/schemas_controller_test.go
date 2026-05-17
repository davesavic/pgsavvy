package controllers_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
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
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Key.Equals(gocui.NewKeyRune('H')) {
			if err := kb.Handler(); err != nil {
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
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Key.Equals(gocui.NewKeyRune('U')) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("U: %v", err)
			}
		}
	}
	if len(b.Confirm.calls) != 1 {
		t.Fatalf("expected ConfirmHelper.Confirm to fire once on ErrNeedsConfirmation, got %d", len(b.Confirm.calls))
	}
	// First call: predicate-failing (real builtin/profile passed); on
	// confirm-yes the controller MUST re-invoke with empty lists.
	// We simulate the user clicking Yes:
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

// AC: <leader>H arms OneshotArmer with prefix=Common.Cfg().Leader.
func TestSchemasControllerLeaderArmReadsCommonCfgLeader(t *testing.T) {
	cfg := &config.UserConfig{Leader: "<space>"}
	c := &common.Common{}
	c.UserConfig.Store(cfg)

	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(c, b.HelperBag, cur, b.SchemaPicker)
	// Find the leader binding (space, rune ' ').
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Key.Equals(gocui.NewKeyRune(' ')) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("leader: %v", err)
			}
		}
	}
	if len(b.OneShot.calls) != 1 {
		t.Fatalf("OneShot.Arm calls = %d, want 1", len(b.OneShot.calls))
	}
	got := b.OneShot.calls[0]
	if got.Prefix != "<space>" {
		t.Fatalf("Arm.prefix = %q, want %q (G1-C: from Common.Cfg().Leader)", got.Prefix, "<space>")
	}
	if got.Scope != "schemas" {
		t.Fatalf("Arm.scope = %q, want %q", got.Scope, "schemas")
	}
	if _, ok := got.Suffixes['H']; !ok {
		t.Fatalf("Arm.suffixes missing 'H'; got %v", got.Suffixes)
	}
	// Invoking the H suffix toggles show-hidden.
	if err := got.Suffixes['H'](); err != nil {
		t.Fatalf("H suffix: %v", err)
	}
	if b.SchemaPicker.toggleCount != 1 {
		t.Fatalf("ToggleShowHidden count = %d, want 1", b.SchemaPicker.toggleCount)
	}
}

func TestSchemasControllerLeaderFallbacksToSpaceWhenCfgEmpty(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag, cur, b.SchemaPicker)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Key.Equals(gocui.NewKeyRune(' ')) {
			if err := kb.Handler(); err != nil {
				t.Fatalf("leader: %v", err)
			}
		}
	}
	if len(b.OneShot.calls) != 1 || b.OneShot.calls[0].Prefix != "<space>" {
		t.Fatalf("fallback leader prefix = %v, want <space>", b.OneShot.calls)
	}
}
