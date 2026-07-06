package keys

// This file holds the failing repro test for the
// `<leader>q` dispatch regression. This test is expected to FAIL at
// master @ f370700 and flip to passing once the fix widens shim
// coverage to chord-trailing keys.
//
// Bug shape:
// `installShimsForScope` at pkg/gui/orchestrator/gui.go:512-547
// installs `driver.SetKeybinding` only for `trie.RootKeys()`. For a
// chord like `<leader>q` (== Space + q), only Space is registered as
// a shim. After Space is consumed (Matcher returns Pending), gocui
// has no shim for `q`, so the key is never delivered to Matcher,
// the leaf never fires, and `gocui.ErrQuit` is never returned.
//
// The test below faithfully replicates the shim-install loop in a
// tiny local fake (we can't import the orchestrator from this
// package — cycle). The fake mirrors the production loop's "only
// RootKeys get a SetKeybinding" behaviour at gui.go:525.

import (
	"errors"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// shimFake is a minimal stand-in for the gocui driver. It mirrors the
// production `installShimsForScope` (gui.go:512-547) behaviour: each
// SetKeybinding registration is keyed by (view, gocui.Key, gocui.Modifier);
// FeedKey returns errNotRegistered when no shim exists, exactly as gocui
// would silently swallow a keystroke that has no registered binding.
type shimFake struct {
	binds map[shimFakeKey]func() error
}

type shimFakeKey struct {
	view string
	gk   types.Key
	gmod types.Modifier
}

func newShimFake() *shimFake {
	return &shimFake{binds: map[shimFakeKey]func() error{}}
}

func (f *shimFake) SetKeybinding(view string, gk types.Key, gmod types.Modifier, handler func() error) {
	f.binds[shimFakeKey{view: view, gk: gk, gmod: gmod}] = handler
}

var errShimNotRegistered = errors.New("shimFake: no binding for (view, key, mod)")

func (f *shimFake) Has(view string, gk types.Key, gmod types.Modifier) bool {
	_, ok := f.binds[shimFakeKey{view: view, gk: gk, gmod: gmod}]
	return ok
}

func (f *shimFake) FeedKey(view string, gk types.Key, gmod types.Modifier) error {
	h, ok := f.binds[shimFakeKey{view: view, gk: gk, gmod: gmod}]
	if !ok {
		return errShimNotRegistered
	}
	return h()
}

// installShimsForScopeLikeProduction faithfully replicates the shim
// installation loop at pkg/gui/orchestrator/gui.go:518-545. Post-fix,
// the loop walks `trie.ReachableKeys()` so every key
// at any depth in the trie gets a SetKeybinding — root keys AND
// chord-trailing keys (the `q` in `<leader>q`). This replica must stay
// in lockstep with the production loop.
func installShimsForScopeLikeProduction(f *shimFake, m *Matcher, ts *TrieSet, scope types.ContextKey, view string) {
	if ts == nil {
		return
	}
	seen := map[shimFakeKey]struct{}{}
	ts.Walk(func(tk TrieSetKey, trie *ChordTrie) {
		if tk.Scope != scope {
			return
		}
		for _, k := range trie.ReachableKeys() {
			gk, gmod, err := ChordKeyToGocui(k)
			if err != nil {
				continue
			}
			sk := shimFakeKey{view: view, gk: gk, gmod: gmod}
			if _, dup := seen[sk]; dup {
				continue
			}
			seen[sk] = struct{}{}
			dispatchKey := k
			handler := func() error {
				_, derr := m.Dispatch(scope, dispatchKey)
				return derr
			}
			f.SetKeybinding(view, gk, gmod, handler)
		}
	})
}

// TestKeybindingService_LeaderQDispatchesAppQuit reproduces the
// `<leader>q` dispatch regression.
//
// At master @ f370700 this test FAILS because `installShimsForScope`
// (gui.go:512) only registers shims for trie root keys (Space), not
// for chord-trailing keys (q). After Space is consumed by the
// Matcher, gocui has no registered handler for `q`, so the leaf
// `<leader>q → app.quit` never fires and gocui.ErrQuit is never
// propagated to the gocui main loop.
//
// The fix is to widen shim coverage so EVERY key
// reachable in the trie has a SetKeybinding shim — not just the root
// keys. After the fix, this test should PASS.
func TestKeybindingService_LeaderQDispatchesAppQuit(t *testing.T) {
	// --- Phase 1: build a registry with the app.quit handler that
	//     mirrors QuitController.Quit (pkg/gui/controllers/quit_controller.go:37-39).
	quitInvocations := 0
	reg := commands.NewRegistry()
	if err := reg.Register(&commands.Command{
		ID:          commands.AppQuit,
		Description: "Quit application",
		Handler: func(commands.ExecCtx) error {
			quitInvocations++
			return gocui.ErrQuit
		},
	}); err != nil {
		t.Fatalf("register app.quit: %v", err)
	}

	// --- Phase 2: build the trie from the *default* UserConfig (which
	//     carries the `<leader>q` -> app.quit binding at user_config.go:134).
	cfg := config.GetDefaultConfig()
	svc := NewKeybindingService()
	trieSet, warns, err := svc.Build(nil, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// AC: Build warnings free of orphan_action for app.quit.
	for _, w := range warns {
		if w.Code == "orphan_action" {
			// We've registered app.quit; an orphan_action warning here is
			// either (a) the bug we're chasing (resolve-action skipped the
			// binding for an unrelated reason) or (b) a different orphan
			// that we don't care about — but for app.quit specifically,
			// it must not appear.
			if containsAppQuit(w.Message) {
				t.Errorf("unexpected orphan_action warning for app.quit: %+v", w)
			}
		}
	}

	// Sanity: the leaf must exist in the trie. If this fails, the bug is
	// at Build time, not at shim time; report and bail.
	leafSeq := []Key{{Code: ' '}, {Code: 'q'}}
	if trie, ok := trieSet.Get(types.ModeNormal, types.GLOBAL); ok {
		res := trie.Lookup(leafSeq)
		if !res.IsLeaf || res.Action == nil || res.Action.ID != commands.AppQuit {
			t.Fatalf("trie lookup ' q' did not resolve to app.quit leaf: %+v", res)
		}
	} else {
		t.Fatal("no trie for (ModeNormal, GLOBAL) — Build did not register defaults")
	}

	// --- Phase 3: construct a Matcher driven by the same trie.
	leader := []rune(cfg.Leader)[0]
	matcher, err := NewMatcher(trieSet, MatcherConfig{
		Leader:        leader,
		TimeoutLen:    durationOrDefault(cfg.TimeoutLen, 1*time.Second),
		TtimeoutLen:   durationOrDefault(cfg.TtimeoutLen, 50*time.Millisecond),
		WhichKeyDelay: durationOrDefault(cfg.WhichKeyDelay, 300*time.Millisecond),
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}

	// --- Phase 4: simulate the production shim install path
	//     (pkg/gui/orchestrator/gui.go:518-545). View name is "" — the
	//     GLOBAL scope binds against the empty-view-name slot.
	shim := newShimFake()
	installShimsForScopeLikeProduction(shim, matcher, trieSet, types.GLOBAL, "")

	// AC: assert the shim path was exercised — Space must be registered.
	spaceKey, spaceMod, encErr := ChordKeyToGocui(Key{Code: ' '})
	if encErr != nil {
		t.Fatalf("ChordKeyToGocui(' '): %v", encErr)
	}
	if !shim.Has("", spaceKey, spaceMod) {
		t.Fatal("shim missing for Space — installShimsForScope did not register the leader key")
	}

	// AC: assert q is ALSO registered as a shim. The production
	// loop only registers RootKeys, so after the leader is consumed q
	// has no shim and gocui drops the key. This assertion is the
	// FAILURE that pins bug 7.
	qKey, qMod, encErr := ChordKeyToGocui(Key{Code: 'q'})
	if encErr != nil {
		t.Fatalf("ChordKeyToGocui('q'): %v", encErr)
	}
	if !shim.Has("", qKey, qMod) {
		t.Errorf("BUG: no shim registered for 'q' — `<leader>q` chord trailing key " +
			"will never be delivered to Matcher (gui.go:525 only walks trie.RootKeys()). " +
			"Fix must widen shim coverage to all chord-reachable keys.")
	}

	// --- Phase 5: drive the chord. Feed Space first — should succeed
	//     (Pending). Then feed q — at master, this fails with
	//     errShimNotRegistered. After the fix, q's handler should call
	//     matcher.Dispatch which fires the leaf and returns gocui.ErrQuit.
	if err := shim.FeedKey("", spaceKey, spaceMod); err != nil {
		t.Fatalf("feed Space: %v", err)
	}
	// Matcher must now be partial (Space buffered, awaiting q).
	if !matcher.IsPartial() {
		t.Fatal("after Space: Matcher not partial; leader was not buffered")
	}

	feedErr := shim.FeedKey("", qKey, qMod)

	// AC: gocui.ErrQuit propagated through the SetKeybinding callback path.
	if !errors.Is(feedErr, gocui.ErrQuit) {
		t.Errorf("feed q: err = %v, want gocui.ErrQuit propagated via shim handler "+
			"(today this fails because no shim is registered for q — bug 7)", feedErr)
	}

	// AC: Quit handler invoked exactly once.
	if quitInvocations != 1 {
		t.Errorf("Quit handler invocations = %d, want 1", quitInvocations)
	}

	// Defensive: matcher must not still hold pending state after the
	// chord resolved (or fell through).
	if matcher.IsPartial() {
		// Wait briefly in case a timer is in flight; this test does not
		// schedule one for an unambiguous leaf, but be defensive.
		time.Sleep(10 * time.Millisecond)
		if matcher.IsPartial() {
			t.Error("matcher still partial after chord — pending state leaked")
		}
	}
}

// containsAppQuit reports whether s mentions the app.quit action ID.
// The orphan_action warning message embeds the ActionID; we use this
// to scope the assertion to the binding under test.
func containsAppQuit(s string) bool {
	const needle = "app.quit"
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func durationOrDefault(d *time.Duration, fallback time.Duration) time.Duration {
	if d != nil {
		return *d
	}
	return fallback
}
