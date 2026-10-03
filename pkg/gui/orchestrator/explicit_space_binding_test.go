package orchestrator

import (
	"errors"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestReview_space_dispatch(t *testing.T) {
	for _, route := range []struct {
		name     string
		scope    types.ContextKey
		mode     types.Mode
		token    string
		editable bool
	}{
		{"noneditable", types.CONNECTION_MANAGER, types.ModeNormal, "n", false},
		{"global", types.GLOBAL, types.ModeNormal, "n", false},
		{"editable_normal", types.SEARCH_LINE, types.ModeNormal, "n", true},
		{"editable_insert", types.SEARCH_LINE, types.ModeInsert, "i", true},
	} {
		for _, tc := range []struct {
			input string
			mod   gocui.Modifier
			chord bool
		}{
			{"<space>", 0, false},
			{"<space>Q", 0, true},
			{"<a-space>Q", gocui.ModAlt, true},
			{"<leader>Q", 0, true},
		} {
			t.Run(route.name+"/"+tc.input, func(t *testing.T) {
				cfg := config.GetDefaultConfig()
				cfg.Keybindings = []config.KeybindingConfig{{Mode: route.token, Scope: string(route.scope), Key: tc.input, Action: commands.AppQuit}}
				registry := commands.NewRegistry()
				calls := 0
				require.NoError(t, registry.Register(&commands.Command{ID: commands.AppQuit, Handler: func(commands.ExecCtx) error {
					calls++
					return gocui.ErrQuit
				}}))
				trieSet, _, err := keys.NewKeybindingService().Build(nil, cfg, registry, nil)
				require.NoError(t, err)
				modes := keys.NewModeStore()
				modes.Set(route.scope, route.mode)
				matcher, err := keys.NewMatcher(trieSet, keys.MatcherConfig{Modes: modes, Leader: ' ', TimeoutLen: time.Minute, TtimeoutLen: time.Minute})
				require.NoError(t, err)
				t.Cleanup(matcher.Cancel)
				rec := testfake.NewRecorderGuiDriver()
				view := string(route.scope)
				if route.scope == types.GLOBAL {
					view = ""
				}
				var feed func(gocui.Key, gocui.Modifier) error
				if route.editable {
					ed := NewMasterEditor(nil, matcher, route.scope).(Dispatcher)
					feed = func(key gocui.Key, _ gocui.Modifier) error {
						result, dispatchErr := ed.Dispatch(nil, key)
						if dispatchErr == nil && tc.chord && calls == 0 {
							require.Equal(t, keys.Pending, result)
						}
						return dispatchErr
					}
				} else {
					g := &Gui{driver: rec, keybindingSystem: keybindingSystem{matcher: matcher}}
					require.NoError(t, g.installShimsForScope(trieSet, route.scope, view))
					require.True(t, rec.HasKeybinding(view, gocui.NewKeyRune(' '), tc.mod), "runtime Space shim must exist")
					if tc.chord {
						require.True(t, rec.HasKeybinding(view, gocui.NewKeyRune('Q'), 0), "trailing key shim must exist")
					}
					feed = func(key gocui.Key, mod gocui.Modifier) error {
						return rec.FeedKey(view, gocui.NewKeyRune([]rune(key.Str())[0]), mod)
					}
				}
				err = feed(gocui.NewKeyStrMod(" ", tc.mod), tc.mod)
				if tc.chord {
					require.NoError(t, err)
					require.Zero(t, calls, "Space alone must not execute the chord")
					err = feed(gocui.NewKeyRune('Q'), 0)
				}
				require.ErrorIs(t, err, gocui.ErrQuit)
				require.Equal(t, 1, calls)
			})
		}
	}
}

type spaceRegistrationFailure struct {
	*testfake.RecorderGuiDriver
	err error
}

func (f *spaceRegistrationFailure) SetKeybinding(string, types.Key, types.Modifier, func() error) error {
	return f.err
}

func TestReview_space_installation_errors(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  keys.Key
	}{
		{"unknown_special", keys.Key{Special: ^keys.SpecialKey(0)}},
		{"empty_key", keys.Key{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := spaceTestTrie(tc.key)
			g := &Gui{driver: testfake.NewRecorderGuiDriver()}
			require.ErrorContains(t, g.installShimsForScope(ts, types.CONNECTION_MANAGER, "connections"), "encode")
		})
	}
	t.Run("unexpanded_leader_rejected_before_install", func(t *testing.T) {
		builder := keys.NewTrieBuilder()
		builder.InsertDefault(&keys.ChordBinding{Sequence: []keys.Key{{Special: keys.KeyLeader}}}, commands.NopCommand)
		trie, warnings := builder.Build()
		require.Empty(t, trie.ReachableKeys())
		require.Len(t, warnings, 1)
		require.Equal(t, "unexpanded_leader", warnings[0].Code)
	})
	t.Run("registration_failure", func(t *testing.T) {
		failure := errors.New("synthetic SetKeybinding failure")
		g := &Gui{driver: &spaceRegistrationFailure{RecorderGuiDriver: testfake.NewRecorderGuiDriver(), err: failure}}
		require.ErrorIs(t, g.installShimsForScope(spaceTestTrie(keys.Key{Code: ' '}), types.CONNECTION_MANAGER, "connections"), failure)
	})
}

func spaceTestTrie(key keys.Key) *keys.TrieSet {
	builder := keys.NewTrieBuilder()
	builder.InsertDefault(&keys.ChordBinding{Sequence: []keys.Key{key}}, &commands.Command{ID: commands.AppQuit, Handler: func(commands.ExecCtx) error { return gocui.ErrQuit }})
	trie, _ := builder.Build()
	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.CONNECTION_MANAGER, trie)
	return ts
}
