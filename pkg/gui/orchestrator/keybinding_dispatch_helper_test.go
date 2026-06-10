// Package orchestrator_test (untagged) hosts the shared keybinding-smoke
// harness plus the per-context shim-dispatch proof tests. The harness
// (kbSmoke / setupKbSmoke / runBuildWithCfg / findLeaf / hasWarning) was
// extracted out of keybinding_smoke_integration_test.go so it compiles
// under the default `task test` suite as well as the `integration` tag —
// the integration walkthrough and these untagged tests share one harness.
package orchestrator_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// kbSmoke bundles the live components built during setupKbSmoke. Step
// subtests read from these; nothing here is shared across walkthroughs.
type kbSmoke struct {
	g   *orchestrator.Gui
	rec *testfake.RecorderGuiDriver
	cfg *config.UserConfig
	tr  *i18n.TranslationSet
	log *slog.Logger
}

// setupKbSmoke spins up a minimal *orchestrator.Gui backed by the
// recorder GuiDriver, with synthetic key-delay overrides so the
// whichkey popup fires within a few milliseconds of wall-clock. The
// shipped default config is used; per-context dispatch tests that need
// a user-config override at wire time use setupKbSmokeWithCfg instead.
func setupKbSmoke(t *testing.T) *kbSmoke {
	t.Helper()
	return setupKbSmokeWithCfg(t, config.GetDefaultConfig())
}

// setupKbSmokeWithCfg is setupKbSmoke parametrised by the UserConfig the
// Gui is wired against. The cfg is read by wireWithDriver via
// Common.Cfg(), so any keybinding override present here is baked into the
// trie AND the per-view SetKeybinding shims installed by
// installKeyDispatch — exactly the path a user's on-disk override travels
// at startup. That is what makes the recorder's FeedKey/FeedChord prove
// dispatch at the shim layer rather than at matcher.Dispatch alone.
func setupKbSmokeWithCfg(t *testing.T, cfg *config.UserConfig) *kbSmoke {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	tr := i18n.EnglishTranslationSet()
	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock())

	g := orchestrator.NewGui(
		orchestrator.Deps{
			Common:              c,
			Store:               store,
			ConnectionsPath:     "/cfg/connections.yml",
			ConnectionsProvider: func() []models.Connection { return nil },
			DriverNamesFn:       func() []string { return []string{"postgres"} },
		},
		// Synthetic delays: tlen=200ms (long enough for the whichkey
		// popup to fire before the matcher's inactivity timer drops the
		// partial), ttimeout=5ms, whichkey=20ms. Keeps the walkthrough
		// well under 2s wall-clock while still exercising real timers.
		orchestrator.WithKeyDelays(200*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond),
	)
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	rec.SetManager(g)

	t.Cleanup(func() {
		_ = g.Close()
	})

	return &kbSmoke{g: g, rec: rec, cfg: cfg, tr: tr, log: log}
}

// runBuildWithCfg constructs a fresh KeybindingService.Build invocation
// against a synthetic config. Useful for steps that need to inspect
// warnings without disturbing the wired Gui's live state.
//
// Returns (trie, warnings, err). The Defaults are AllDefaultBindings on
// the live controllers, and the Registry is the wired Gui's registry.
func (s *kbSmoke) runBuildWithCfg(synthetic *config.UserConfig) (*keys.TrieSet, []keys.Warning, error) {
	svc := keys.NewKeybindingService()
	defaults := controllers.AllDefaultBindings(s.g.Controllers())
	kindOf := func(k types.ContextKey) types.ContextKind {
		for _, ctx := range s.g.Registry().Flatten() {
			if ctx != nil && ctx.GetKey() == k {
				return ctx.GetKind()
			}
		}
		return types.GLOBAL_CONTEXT
	}
	return svc.Build(defaults, synthetic, s.g.CommandRegistry(), kindOf)
}

// hasWarning reports whether ws contains a warning with the given Code.
func hasWarning(ws []keys.Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// findLeaf walks the trie at (mode, scope) and returns the first leaf
// whose Action.ID matches actionID. Returns (zero, nil, false) when
// missing.
func findLeaf(trieSet *keys.TrieSet, mode types.Mode, scope types.ContextKey, actionID string) ([]keys.Key, keys.LookupResult, bool) {
	if trieSet == nil {
		return nil, keys.LookupResult{}, false
	}
	trie, ok := trieSet.Get(mode, scope)
	if !ok || trie == nil {
		return nil, keys.LookupResult{}, false
	}
	var (
		foundSeq  []keys.Key
		foundLeaf keys.LookupResult
		hit       bool
	)
	trie.Walk(func(seq []keys.Key, leaf keys.LookupResult) {
		if hit {
			return
		}
		if leaf.Action != nil && leaf.Action.ID == actionID {
			foundSeq = append([]keys.Key(nil), seq...)
			foundLeaf = leaf
			hit = true
		}
	})
	return foundSeq, foundLeaf, hit
}
