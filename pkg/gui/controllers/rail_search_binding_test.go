package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// railSearchBindings is the (key, actionID) set both rails must publish
// in ModeNormal for the highlight+jump search.
//
//	/    → RailSearchPrompt
//	n    → RailSearchNext
//	N    → RailSearchPrev
//	<esc>→ RailSearchClear
var railSearchExpected = []struct {
	desc     string
	rune     rune
	special  types.SpecialKey
	actionID string
}{
	{"/", '/', types.KeyNone, commands.RailSearchPrompt},
	{"n", 'n', types.KeyNone, commands.RailSearchNext},
	{"N", 'N', types.KeyNone, commands.RailSearchPrev},
	{"<esc>", 0, types.KeyEsc, commands.RailSearchClear},
}

// matchRailSearch reports whether b is the ModeNormal/scope binding for
// the given expected entry (rune XOR special), mapped to the right ID.
func matchRailSearch(b *types.ChordBinding, scope types.ContextKey, runeKey rune, special types.SpecialKey, actionID string) bool {
	if b == nil || b.Mode != types.ModeNormal || b.Scope != scope || b.ActionID != actionID {
		return false
	}
	if special != types.KeyNone {
		return isSpecial(b, special)
	}
	return isRune(b, runeKey)
}

func assertRailSearchBindings(t *testing.T, scope types.ContextKey, bindings []*types.ChordBinding) {
	t.Helper()
	for _, want := range railSearchExpected {
		found := false
		for _, kb := range bindings {
			if matchRailSearch(kb, scope, want.rune, want.special, want.actionID) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: missing %q → %s (ModeNormal)", scope, want.desc, want.actionID)
		}
	}
}

// AC: TABLES binds / n N <esc> in ModeNormal to the four rail-search IDs.
func TestTablesControllerPublishesRailSearchBindings(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, cur, b.TablePicker)
	assertRailSearchBindings(t, types.TABLES, ctrl.GetKeybindings(types.KeybindingsOpts{}))
}

// AC: SCHEMAS binds / n N <esc> in ModeNormal to the four rail-search IDs.
func TestSchemasControllerPublishesRailSearchBindings(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, cur, b.SchemaPicker)
	assertRailSearchBindings(t, types.SCHEMAS, ctrl.GetKeybindings(types.KeybindingsOpts{}))
}

// AC: the rail-search bindings do not displace the pre-existing TABLES
// bindings (j/k/gg/G via baseBindings, <CR>, r, i).
func TestTablesControllerRailSearchKeepsExistingBindings(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewTablesController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, cur, b.TablePicker)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	wantEnter, wantR, wantI := false, false, false
	for _, kb := range kbs {
		if isSpecial(kb, types.KeyEnter) {
			wantEnter = true
		}
		if isRune(kb, 'r') {
			wantR = true
		}
		if isRune(kb, 'i') && kb.ActionID == commands.TableInspectOpen {
			wantI = true
		}
	}
	if !wantEnter || !wantR || !wantI {
		t.Fatalf("existing TABLES bindings missing: <CR>=%v r=%v i=%v", wantEnter, wantR, wantI)
	}
}

// AC: the rail-search bindings do not displace the pre-existing SCHEMAS
// bindings (H, U, r, <leader>H, <CR>).
func TestSchemasControllerRailSearchKeepsExistingBindings(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewSchemasController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, cur, b.SchemaPicker)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	wantH, wantU, wantR, wantLeaderH := false, false, false, false
	for _, kb := range kbs {
		if isRune(kb, 'H') && kb.ActionID == commands.SchemaHide {
			wantH = true
		}
		if isRune(kb, 'U') && kb.ActionID == commands.SchemaUnhide {
			wantU = true
		}
		if isRune(kb, 'r') {
			wantR = true
		}
		if len(kb.Sequence) == 2 &&
			kb.Sequence[0].Special == types.KeyLeader &&
			kb.Sequence[1].Code == 'H' {
			wantLeaderH = true
		}
	}
	if !wantH || !wantU || !wantR || !wantLeaderH {
		t.Fatalf("existing SCHEMAS bindings missing: H=%v U=%v r=%v <leader>H=%v", wantH, wantU, wantR, wantLeaderH)
	}
}

// AC: the four rail-search action IDs appear in commands.AllActionIDs().
func TestRailSearchActionIDsInAllActionIDs(t *testing.T) {
	all := commands.AllActionIDs()
	have := make(map[string]bool, len(all))
	for _, id := range all {
		have[id] = true
	}
	for _, id := range []string{
		commands.RailSearchPrompt,
		commands.RailSearchNext,
		commands.RailSearchPrev,
		commands.RailSearchClear,
	} {
		if !have[id] {
			t.Errorf("AllActionIDs() missing %q", id)
		}
	}
}
