// Untagged shim-level dispatch proofs.
//
// Each of the 10 interactive AD3 contexts gets a test that:
//
//  1. wires a fresh Gui against a UserConfig carrying ONE keybinding
//     override in that context's scope (so installKeyDispatch installs the
//     per-view SetKeybinding shim — or the master Editor for editable
//     contexts — for the override key as the app does at startup);
//  2. drives the override key through the recorder's FeedKey (shim route)
//     or FeedChord (master-editor route) BY VIEW NAME, without Push-ing the
//     popup and without building any DB/buffer/conflict/FK fixtures;
//  3. asserts the bound handler actually fired.
//
// The override binds the key to commands.AppQuit, whose handler returns
// gocui.ErrQuit on a clean session. ErrQuit is the one handler result the
// Matcher propagates rather than swallowing (matcher.go), so the recorder
// hands it straight back from FeedKey/FeedChord — a precise, side-effect
// free dispatch signal.
//
// Why the shim route and not matcher.Dispatch directly: matcher.Dispatch
// bypasses the gocui SetKeybinding shim, which is the actual silent-no-op
// site this epic guards. Invoking cmd.Handler directly would BE the no-op
// the epic exists to catch, so neither is used here.
package orchestrator_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// withOverride returns a copy of the default config with one extra
// keybinding: (mode n, scope, key) -> action.
func withOverride(scope types.ContextKey, key, action string) *config.UserConfig {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = append(cfg.Keybindings, config.KeybindingConfig{
		Mode:        "n",
		Scope:       string(scope),
		Key:         key,
		Action:      action,
		Description: "dispatch-proof override",
	})
	return cfg
}

// liveViewName returns the GetViewName() of the live context registered
// under scope. Fails the test when the context is absent. This is the R6
// view-name oracle: FeedKey-by-name alone does not model view existence,
// so we assert the bound name equals the live context's view name.
func liveViewName(t *testing.T, s *kbSmoke, scope types.ContextKey) string {
	t.Helper()
	for _, ctx := range s.g.Registry().Flatten() {
		if ctx != nil && ctx.GetKey() == scope {
			return ctx.GetViewName()
		}
	}
	t.Fatalf("no live context registered for scope %q", scope)
	return ""
}

// runeKey is the (types.Key, types.Modifier) pair the shim registers for a
// bare-rune override key — the same encoding installShimsForScope uses via
// keys.ChordKeyToGocui, so FeedKey matches the recorded binding exactly.
func runeKey(t *testing.T, r rune) (types.Key, types.Modifier) {
	t.Helper()
	gk, gmod, err := keys.ChordKeyToGocui(types.ChordKey{Code: r})
	if err != nil {
		t.Fatalf("ChordKeyToGocui(%q): %v", r, err)
	}
	return gk, gmod
}

// the 8 non-editable AD3 contexts driven through the gocui shim route.
var nonEditableDispatchCases = []struct {
	name  string
	scope types.ContextKey
}{
	{"SELECTION", types.SELECTION},
	{"CONNECTION_MANAGER", types.CONNECTION_MANAGER},
	{"COMMIT_DIALOG", types.COMMIT_DIALOG},
	{"CONFLICT_DIALOG", types.CONFLICT_DIALOG},
	{"EXPORT_MENU", types.EXPORT_MENU},
	{"TABLE_INSPECT", types.TABLE_INSPECT},
	{"FK_REVERSE_PICKER", types.FK_REVERSE_PICKER},
	{"CHEATSHEET", types.CHEATSHEET},
}

// the 2 editable AD3 contexts driven through the master-editor route.
var editableDispatchCases = []struct {
	name  string
	scope types.ContextKey
}{
	{"CELL_EDITOR", types.CELL_EDITOR},
	{"SEARCH_LINE", types.SEARCH_LINE},
}

// TestKeybindingOverrideDispatch_NonEditable proves that for each of the 8
// non-editable AD3 contexts a user-config override installs a gocui shim
// on the context's static view, and that FeedKey-by-view-name routes the
// key into the Matcher and fires the bound handler.
func TestKeybindingOverrideDispatch_NonEditable(t *testing.T) {
	const overrideRune = 'X'
	for _, tc := range nonEditableDispatchCases {
		t.Run(tc.name, func(t *testing.T) {
			s := setupKbSmokeWithCfg(t, withOverride(tc.scope, string(overrideRune), commands.AppQuit))

			// R6: the bound view name MUST equal the live context's view
			// name (== string(ContextKey)). A TEMPORARY_POPUP whose pushed
			// view drifts from the bound name would otherwise pass FeedKey
			// vacuously.
			view := liveViewName(t, s, tc.scope)
			if view != string(tc.scope) {
				t.Fatalf("live view name = %q; want %q (== string(ContextKey))", view, tc.scope)
			}

			// The shim MUST exist on that view for the override key. This is
			// the silent-no-op site: a context that validates+builds but
			// installs no shim would fail here.
			gk, gmod := runeKey(t, overrideRune)
			if !s.rec.HasKeybinding(view, gk, gmod) {
				t.Fatalf("no gocui shim installed on view %q for override key %q", view, overrideRune)
			}

			// Drive the key through the shim. The handler routes into the
			// Matcher under tc.scope, fires AppQuit, which returns ErrQuit.
			err := s.rec.FeedKey(view, gk, gmod)
			if !errors.Is(err, gocui.ErrQuit) {
				t.Fatalf("FeedKey(%q, %q) = %v; want gocui.ErrQuit (override did not dispatch via shim)", view, overrideRune, err)
			}
		})
	}
}

// TestKeybindingOverrideDispatch_Editable proves the same for the 2
// editable AD3 contexts, driven through the master-editor route
// (FeedChord) since editable views receive input through a master
// gocui.Editor rather than per-key SetKeybinding shims.
func TestKeybindingOverrideDispatch_Editable(t *testing.T) {
	const overrideRune = 'X'
	for _, tc := range editableDispatchCases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.scope.IsEditable() {
				t.Fatalf("%s is not editable; wrong dispatch route", tc.scope)
			}
			s := setupKbSmokeWithCfg(t, withOverride(tc.scope, string(overrideRune), commands.AppQuit))

			// R6: bound view name == live context view name.
			view := liveViewName(t, s, tc.scope)
			if view != string(tc.scope) {
				t.Fatalf("live view name = %q; want %q (== string(ContextKey))", view, tc.scope)
			}

			// installKeyDispatch builds the master Editor for the editable
			// scope at wire time; the per-frame Tier-3 layout pass attaches it
			// to the live view when the context is on the focus stack. We do
			// NOT push the popup, so we attach the production-built editor to
			// the recorder by its static view name — the same editor the
			// layout would attach — then drive it. A missing editor here is
			// the editable-context silent-no-op site.
			ed := s.g.MasterEditorForTest(tc.scope)
			if ed == nil {
				t.Fatalf("installKeyDispatch built no master Editor for editable scope %q", tc.scope)
			}
			if err := s.rec.SetMasterEditor(view, ed); err != nil {
				t.Fatalf("SetMasterEditor(%q): %v", view, err)
			}

			// Drive the key through the master Editor. The editor dispatches
			// under tc.scope at the scope's current mode (ModeNormal while
			// un-pushed), fires AppQuit, returning ErrQuit.
			res, err := s.rec.FeedChord(view, []keys.Key{{Code: overrideRune}})
			if !errors.Is(err, gocui.ErrQuit) {
				t.Fatalf("FeedChord(%q, %q) = (%v, %v); want gocui.ErrQuit (override did not dispatch via master editor)", view, overrideRune, res, err)
			}
		})
	}
}

// TestKeybindingOverrideShadowsDefault proves a user binding shadows the
// shipped default for the same (mode, scope, key). SELECTION ships
// `j -> selection.down`; the override rebinds `j -> app.quit`. Driving `j`
// through the shim must now fire AppQuit (ErrQuit), not the motion — the
// user layer wins.
func TestKeybindingOverrideShadowsDefault(t *testing.T) {
	s := setupKbSmokeWithCfg(t, withOverride(types.SELECTION, "j", commands.AppQuit))

	view := liveViewName(t, s, types.SELECTION)
	gk, gmod := runeKey(t, 'j')
	if !s.rec.HasKeybinding(view, gk, gmod) {
		t.Fatalf("no shim for shadowed default key 'j' on view %q", view)
	}

	// Sanity: the `j` leaf at (Normal, SELECTION) now resolves to AppQuit,
	// not selection.down — the override displaced the default ON THIS KEY.
	// (selection.down legitimately survives on its other default binding,
	// the Down arrow, so we look up the `j` key specifically rather than
	// the action.)
	ts := s.g.Matcher().TrieSet()
	if _, _, ok := findLeaf(ts, types.ModeNormal, types.SELECTION, commands.AppQuit); !ok {
		t.Fatalf("override binding ->app.quit absent from (Normal, SELECTION) trie")
	}
	trie, ok := ts.Get(types.ModeNormal, types.SELECTION)
	if !ok || trie == nil {
		t.Fatalf("no (Normal, SELECTION) trie")
	}
	res := trie.Lookup([]keys.Key{{Code: 'j'}})
	if !res.Found || !res.IsLeaf || res.Action == nil || res.Action.ID != commands.AppQuit {
		t.Fatalf("j leaf = %+v; want app.quit (override should shadow selection.down on 'j')", res)
	}

	err := s.rec.FeedKey(view, gk, gmod)
	if !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("FeedKey(SELECTION, 'j') = %v; want gocui.ErrQuit (override should fire AppQuit, not the motion)", err)
	}
}

// TestKeybindingOverrideOrphanActionWarns is the negative AC: a binding
// citing an unknown action emits the existing orphan_action warning and
// does not crash the Build. (Wire-time validation rejects unknown actions
// hard, so this exercises Build directly via the shared harness — the same
// path used by step19 of the integration walkthrough.)
func TestKeybindingOverrideOrphanActionWarns(t *testing.T) {
	s := setupKbSmoke(t)

	synthetic := *s.cfg
	synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
	synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
		Mode:        "n",
		Scope:       string(types.SELECTION),
		Key:         "X",
		Action:      "this.action.does.not.exist",
		Description: "orphan",
	})
	trie, warnings, err := s.runBuildWithCfg(&synthetic)
	if err != nil {
		t.Fatalf("Build with orphan action returned hard error: %v", err)
	}
	if trie == nil {
		t.Fatalf("Build returned nil TrieSet despite orphan being non-fatal")
	}
	if !hasWarning(warnings, "orphan_action") {
		t.Fatalf("expected orphan_action warning; got %v", warnings)
	}
}
