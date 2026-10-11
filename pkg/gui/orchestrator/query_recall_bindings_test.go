package orchestrator_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestQueryRecallBindingsDispatchWithoutTimeout(t *testing.T) {
	for _, tc := range []struct {
		key  rune
		leaf types.ContextKey
	}{
		{key: 'h', leaf: types.HISTORY},
		{key: 'H', leaf: types.SAVED_QUERY},
	} {
		t.Run(string(tc.key), func(t *testing.T) {
			g := buildTestGuiNoHistory(t)
			if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
				t.Fatal(err)
			}
			m := g.Matcher()
			if res, err := m.Dispatch(types.QUERY_EDITOR, keys.Key{Code: ' '}); err != nil || res != keys.Pending {
				t.Fatalf("leader: result=%v, err=%v", res, err)
			}
			if res, err := m.Dispatch(types.QUERY_EDITOR, keys.Key{Code: tc.key}); err != nil || res != keys.Dispatched {
				t.Fatalf("recall shortcut must dispatch immediately: result=%v, err=%v", res, err)
			}
			if got := g.Registry().QueryRail.ActiveLeafKey(); got != tc.leaf {
				t.Fatalf("active tab = %s, want %s", got, tc.leaf)
			}
		})
	}
}

func TestQueryOpenPrefixHasNoSavedQueryAction(t *testing.T) {
	g := buildTestGuiNoHistory(t)
	ts := g.Matcher().TrieSet()
	trie, ok := ts.Get(types.ModeNormal, types.QUERY_EDITOR)
	if !ok {
		t.Fatal("query editor trie missing")
	}
	prefix := []keys.Key{{Code: ' '}, {Code: 'o'}}
	if got := trie.Lookup(prefix); !got.Found || got.IsLeaf || !got.HasChildren {
		t.Fatalf("Space o must only be a prefix: %+v", got)
	}
	if got := trie.Lookup(append(prefix, keys.Key{Code: 'f'})); got.Action == nil || got.Action.ID != commands.QueryOpenFile {
		t.Fatalf("Space o f must open SQL files: %+v", got)
	}
	global, ok := ts.Get(types.ModeNormal, types.GLOBAL)
	if !ok {
		t.Fatal("global trie missing")
	}
	if got := global.Lookup(append(prefix, keys.Key{Code: 's'})); got.Action == nil || got.Action.ID != commands.SettingsOpen {
		t.Fatalf("Space o s must open settings: %+v", got)
	}
}

func TestSettingsOpenChordDispatchesFromQueryEditor(t *testing.T) {
	g := buildTestGuiNoHistory(t)
	if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
		t.Fatal(err)
	}
	for _, r := range []rune{' ', 'o'} {
		if res, err := g.Matcher().Dispatch(types.QUERY_EDITOR, keys.Key{Code: r}); err != nil || res != keys.Pending {
			t.Fatalf("prefix %q: result=%v, err=%v", r, res, err)
		}
	}
	if res, err := g.Matcher().Dispatch(types.QUERY_EDITOR, keys.Key{Code: 's'}); err != nil || res != keys.Dispatched {
		t.Fatalf("settings shortcut: result=%v, err=%v", res, err)
	}
	if current := g.ContextTree().Current(); current == nil || current.GetKey() != types.SETTINGS {
		t.Fatalf("settings shortcut did not open settings: %v", current)
	}
}
