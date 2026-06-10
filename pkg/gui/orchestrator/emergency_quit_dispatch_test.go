// Un-rebindable emergency Ctrl-C exit proofs (task dbsavvy-ivck.5 / R5).
//
// Under tcell raw mode ISIG is cleared, so keyboard Ctrl-C is delivered as
// a KeyCtrlC KEY event (not SIGINT) and flows through the keybinding
// dispatch path. The default app.quit binding (<c-c>) works only because
// gocui dispatches it as a trie key into the user-replaceable keybinding
// trie. A user config that moves app.quit off <c-c> — or binds <c-c> to
// some other action — could otherwise leave keyboard escape broken or
// shadowed at runtime.
//
// R5 adds an in-code interception that ALWAYS routes Ctrl-C to the existing
// clean-quit path (QuitController.Quit -> gocui.ErrQuit), independent of
// the user's keybindings: trie. The interception lives in two seams that
// together cover every context:
//
//   - non-editable views (+ global): installShimsForScope RESERVES Ctrl-C
//     (never installs a trie shim for it), so installEmergencyQuitShim is the
//     SOLE Ctrl-C handler per view — it wins independent of gocui's
//     first-registered-wins scan order;
//   - editable views (CELL_EDITOR, SEARCH_LINE, QUERY_EDITOR, …): the
//     master / vim Editor intercepts Ctrl-C before matcher.Dispatch.
//
// Because the interception is in code, the user's keybinding config can
// neither remove nor shadow it — pressing Ctrl-C always quits. These tests
// prove that at the dispatch layer.
package orchestrator_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ctrlCFeed is the (gocui Key, Mod) pair the recorder FeedKey uses to drive
// the Ctrl-C shim — the same encoding installEmergencyQuitShim registers
// via keys.ChordKeyToGocui for {Code:'c', Mod:ChordModCtrl}.
func ctrlCFeed(t *testing.T) (types.Key, types.Modifier) {
	t.Helper()
	gk, gmod, err := keys.ChordKeyToGocui(types.ChordKey{Code: 'c', Mod: types.ChordModCtrl})
	if err != nil {
		t.Fatalf("ChordKeyToGocui(<c-c>): %v", err)
	}
	return gk, gmod
}

// appQuitMovedOffCtrlC returns a copy of the default config with the
// shipped global `<c-c> -> app.quit` binding REPLACED by `q -> app.quit`,
// and with NOTHING bound to <c-c>. This models a user who rebound quit to a
// non-ctrl-c key. Without the R5 emergency shim, Ctrl-C would have no
// handler installed on a non-editable view and gocui would drop it.
func appQuitMovedOffCtrlC() *config.UserConfig {
	cfg := config.GetDefaultConfig()
	out := cfg.Keybindings[:0:0]
	for _, kb := range cfg.Keybindings {
		// Drop the default global <c-c> -> app.quit binding.
		if kb.Scope == string(types.GLOBAL) && kb.Key == "<c-c>" && kb.Action == commands.AppQuit {
			continue
		}
		out = append(out, kb)
	}
	// Rebind app.quit to a plain `q` in the GLOBAL scope.
	out = append(out, config.KeybindingConfig{
		Mode:        "n",
		Scope:       string(types.GLOBAL),
		Key:         "q",
		Action:      commands.AppQuit,
		Description: "quit moved off ctrl-c",
	})
	cfg.Keybindings = out
	return cfg
}

// ctrlCBoundToOtherAction returns a copy of the default config with <c-c>
// REBOUND (in the SELECTION scope) to a non-quit action. The emergency
// exit must still win: <c-c> is no longer user-remappable.
func ctrlCBoundToOtherAction() *config.UserConfig {
	return withOverride(types.SELECTION, "<c-c>", commands.SelectionDown)
}

// TestEmergencyQuit_NonEditable_QuitMovedOffCtrlC proves that when the user
// moves app.quit off <c-c> and binds nothing to <c-c>, pressing Ctrl-C in a
// NON-editable context still quits via the in-code emergency shim.
func TestEmergencyQuit_NonEditable_QuitMovedOffCtrlC(t *testing.T) {
	s := setupKbSmokeWithCfg(t, appQuitMovedOffCtrlC())

	view := liveViewName(t, s, types.SELECTION)
	gk, gmod := ctrlCFeed(t)

	// The emergency shim MUST be installed on the non-editable view even
	// though the user bound no <c-c> action.
	if !s.rec.HasKeybinding(view, gk, gmod) {
		t.Fatalf("no emergency Ctrl-C shim installed on non-editable view %q", view)
	}

	// Driving Ctrl-C through the shim routes into the clean-quit path and
	// returns gocui.ErrQuit.
	if err := s.rec.FeedKey(view, gk, gmod); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("FeedKey(%q, <c-c>) = %v; want gocui.ErrQuit (emergency exit did not fire)", view, err)
	}
}

// TestEmergencyQuit_Editable_QuitMovedOffCtrlC proves the same for an
// EDITABLE context (CELL_EDITOR), driven through the master-editor route.
// The editor intercepts Ctrl-C before matcher dispatch, so the user's
// keybinding config is irrelevant.
func TestEmergencyQuit_Editable_QuitMovedOffCtrlC(t *testing.T) {
	s := setupKbSmokeWithCfg(t, appQuitMovedOffCtrlC())

	view := liveViewName(t, s, types.CELL_EDITOR)
	ed := s.g.MasterEditorForTest(types.CELL_EDITOR)
	if ed == nil {
		t.Fatalf("no master Editor built for editable scope CELL_EDITOR")
	}
	if err := s.rec.SetMasterEditor(view, ed); err != nil {
		t.Fatalf("SetMasterEditor(%q): %v", view, err)
	}

	_, err := s.rec.FeedChord(view, []keys.Key{{Code: 'c', Mod: keys.ModCtrl}})
	if !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("FeedChord(CELL_EDITOR, <c-c>) = %v; want gocui.ErrQuit (emergency exit did not fire in editable context)", err)
	}
}

// TestEmergencyQuit_DefaultConfig_StillQuits is the regression guard: with
// the shipped default config (app.quit = <c-c>) Ctrl-C still quits cleanly.
// installShimsForScope reserves Ctrl-C, so the only Ctrl-C handler is the
// emergency shim, which routes to the same QuitController.Quit — no
// double-handling or panic.
func TestEmergencyQuit_DefaultConfig_StillQuits(t *testing.T) {
	s := setupKbSmokeWithCfg(t, config.GetDefaultConfig())

	view := liveViewName(t, s, types.SELECTION)
	gk, gmod := ctrlCFeed(t)
	if !s.rec.HasKeybinding(view, gk, gmod) {
		t.Fatalf("no Ctrl-C shim on non-editable view %q under default config", view)
	}
	if err := s.rec.FeedKey(view, gk, gmod); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("FeedKey(SELECTION, <c-c>) = %v; want gocui.ErrQuit under default config", err)
	}

	// Editable route under default config too.
	cellView := liveViewName(t, s, types.CELL_EDITOR)
	ed := s.g.MasterEditorForTest(types.CELL_EDITOR)
	if ed == nil {
		t.Fatalf("no master Editor for CELL_EDITOR under default config")
	}
	if err := s.rec.SetMasterEditor(cellView, ed); err != nil {
		t.Fatalf("SetMasterEditor(%q): %v", cellView, err)
	}
	if _, err := s.rec.FeedChord(cellView, []keys.Key{{Code: 'c', Mod: keys.ModCtrl}}); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("FeedChord(CELL_EDITOR, <c-c>) = %v; want gocui.ErrQuit under default config", err)
	}
}

// TestEmergencyQuit_UnRebindable_CtrlCBoundElsewhere proves that a user
// binding <c-c> to a DIFFERENT action does NOT defeat the emergency exit.
// installShimsForScope RESERVES Ctrl-C (skips the trie shim for it), so the
// user's <c-c> binding — though present in the trie — never installs a
// competing gocui handler. The emergency shim is the SOLE Ctrl-C handler on
// the view, so Ctrl-C still quits regardless of registration order. <c-c> is
// therefore no longer user-remappable.
func TestEmergencyQuit_UnRebindable_CtrlCBoundElsewhere(t *testing.T) {
	s := setupKbSmokeWithCfg(t, ctrlCBoundToOtherAction())

	view := liveViewName(t, s, types.SELECTION)
	gk, gmod := ctrlCFeed(t)

	// Sanity: the user's <c-c> -> selection.down binding really is present
	// in the trie (so a non-emergency dispatch would NOT quit).
	ts := s.g.Matcher().TrieSet()
	trie, ok := ts.Get(types.ModeNormal, types.SELECTION)
	if !ok || trie == nil {
		t.Fatalf("no (Normal, SELECTION) trie")
	}
	res := trie.Lookup([]keys.Key{{Code: 'c', Mod: keys.ModCtrl}})
	if !res.Found || !res.IsLeaf || res.Action == nil || res.Action.ID != commands.SelectionDown {
		t.Fatalf("<c-c> trie leaf = %+v; want user override -> %q", res, commands.SelectionDown)
	}

	// Despite the trie binding, the emergency shim (registered last) wins:
	// Ctrl-C still quits.
	if err := s.rec.FeedKey(view, gk, gmod); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("FeedKey(SELECTION, <c-c>) = %v; want gocui.ErrQuit (emergency must win over user <c-c> rebind)", err)
	}
}
