package orchestrator

import (
	"reflect"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// optionsBarBinding is a compact ChordBinding constructor for tests.
// Every entry resolves to a fixture *commands.Command whose Tag and
// Description are propagated into the trie leaf via the trieNode
// extension.
type optionsBarBinding struct {
	seq         string
	mode        types.Mode
	scope       types.ContextKey
	tag         string
	description string
	showInBar   bool
}

// buildOptionsBarTrieSet routes each binding through the per-(mode,
// scope) TrieBuilder and returns the resulting TrieSet. Mirrors the
// per-bit / per-scope routing keys.KeybindingService.Build performs
// in production, minus the cfg-overlay and warning machinery.
func buildOptionsBarTrieSet(t *testing.T, bindings []optionsBarBinding) *keys.TrieSet {
	t.Helper()
	type bucketKey struct {
		mode  types.Mode
		scope types.ContextKey
	}
	builders := map[bucketKey]*keys.TrieBuilder{}
	for _, b := range bindings {
		seq, err := keys.SequenceFromShorthand(b.seq)
		if err != nil {
			t.Fatalf("SequenceFromShorthand(%q): %v", b.seq, err)
		}
		cmd := &commands.Command{
			ID:          b.tag + "." + b.seq,
			Description: b.description,
			Tag:         b.tag,
			Handler:     func(commands.ExecCtx) error { return nil },
		}
		bk := bucketKey{mode: b.mode, scope: b.scope}
		bld, ok := builders[bk]
		if !ok {
			bld = keys.NewTrieBuilder()
			builders[bk] = bld
		}
		bld.InsertDefault(&keys.ChordBinding{
			Sequence:    seq,
			Mode:        b.mode,
			Scope:       b.scope,
			ActionID:    cmd.ID,
			Description: b.description,
			Tag:         b.tag,
			ShowInBar:   b.showInBar,
			Origin:      "options_bar_test",
		}, cmd)
	}
	out := keys.NewTrieSet()
	for bk, bld := range builders {
		trie, _ := bld.Build()
		out.Set(bk.mode, bk.scope, trie)
	}
	return out
}

func TestCollectOptionsForScope_NilTrieSetReturnsEmpty(t *testing.T) {
	got := CollectOptionsForScope(nil, types.ModeNormal, types.TABLES, nil, nil)
	if got == nil {
		t.Fatalf("got nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestCollectOptionsForScope_NoShowInBarReturnsEmpty(t *testing.T) {
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "q", mode: types.ModeNormal, scope: types.TABLES, tag: "App", description: "Quit"},
		{seq: "r", mode: types.ModeNormal, scope: types.TABLES, tag: "Table", description: "Refresh"},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	if len(got) != 0 {
		t.Errorf("got %v, want empty (no ShowInBar leaves)", got)
	}
	if got == nil {
		t.Errorf("got nil, want non-nil empty slice")
	}
}

func TestCollectOptionsForScope_ScopeAndGlobalLeavesIncluded(t *testing.T) {
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "r", mode: types.ModeNormal, scope: types.TABLES, tag: "Table", description: "Refresh", showInBar: true},
		{seq: "d", mode: types.ModeNormal, scope: types.TABLES, tag: "Table", description: "Describe", showInBar: true},
		{seq: "x", mode: types.ModeNormal, scope: types.TABLES, tag: "Table", description: "DELETE", showInBar: false},
		{seq: "q", mode: types.ModeNormal, scope: types.GLOBAL, tag: "App", description: "Quit", showInBar: true},
		{seq: "?", mode: types.ModeNormal, scope: types.GLOBAL, tag: "Help", description: "Cheatsheet", showInBar: true},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	want := []string{
		"[q] Quit",       // tag "App"
		"[?] Cheatsheet", // tag "Help"
		"[d] Describe",   // tag "Table", key "d"
		"[r] Refresh",    // tag "Table", key "r"
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCollectOptionsForScope_DeterministicSortByTagThenKey(t *testing.T) {
	// Two bindings share Tag — secondary sort key is the sequence
	// string, so the lexicographically smaller key wins.
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "b", mode: types.ModeNormal, scope: types.TABLES, tag: "Same", description: "Beta", showInBar: true},
		{seq: "a", mode: types.ModeNormal, scope: types.TABLES, tag: "Same", description: "Alpha", showInBar: true},
		{seq: "c", mode: types.ModeNormal, scope: types.TABLES, tag: "Aardvark", description: "Cee", showInBar: true},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	want := []string{
		"[c] Cee",   // tag "Aardvark" wins on tag sort
		"[a] Alpha", // tag "Same", key "a"
		"[b] Beta",  // tag "Same", key "b"
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCollectOptionsForScope_TruncatedToMaxEight(t *testing.T) {
	// Ten ShowInBar leaves, all distinct keys — only the first eight
	// (after tag-then-key sort) make it into the output.
	var bs []optionsBarBinding
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		bs = append(bs, optionsBarBinding{
			seq: k, mode: types.ModeNormal, scope: types.TABLES,
			tag: "Same", description: "Desc" + k, showInBar: true,
		})
	}
	ts := buildOptionsBarTrieSet(t, bs)
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	if len(got) != optionsBarMax {
		t.Fatalf("len(got) = %d, want %d", len(got), optionsBarMax)
	}
	want := []string{
		"[a] Desca", "[b] Descb", "[c] Descc", "[d] Descd",
		"[e] Desce", "[f] Descf", "[g] Descg", "[h] Desch",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCollectOptionsForScope_ModeMismatchExcluded(t *testing.T) {
	// Visual-mode binding must NOT appear when the focused mode is
	// Normal. tries are keyed by single-bit Mode so a Get(Normal,
	// scope) miss is the implicit filter.
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "y", mode: types.ModeVisual, scope: types.TABLES, tag: "Edit", description: "Yank", showInBar: true},
		{seq: "r", mode: types.ModeNormal, scope: types.TABLES, tag: "Table", description: "Refresh", showInBar: true},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	want := []string{"[r] Refresh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCollectOptionsForScope_NoGlobalTrieForModeStillReturnsScope(t *testing.T) {
	// Only scope-tier bindings — no GLOBAL trie at all for this mode.
	// Scope contributions must still appear.
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "r", mode: types.ModeNormal, scope: types.TABLES, tag: "Table", description: "Refresh", showInBar: true},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	want := []string{"[r] Refresh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCollectOptionsForScope_FocusedScopeIsGlobalNoDoubleCount(t *testing.T) {
	// When the focused scope IS GLOBAL, we must not collect from the
	// same trie twice.
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "q", mode: types.ModeNormal, scope: types.GLOBAL, tag: "App", description: "Quit", showInBar: true},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.GLOBAL, nil, nil)
	want := []string{"[q] Quit"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCollectOptionsForScope_ConnectionsHasAtLeastThreeHints(t *testing.T) {
	// the CONNECTIONS rail flags its top bindings
	// (connect/add/refresh) ShowInBar:true so the status options bar
	// lights up. Mirror those three production leaves here and assert
	// CollectOptionsForScope returns a non-empty slice with >=3 entries.
	ts := buildOptionsBarTrieSet(t, []optionsBarBinding{
		{seq: "<cr>", mode: types.ModeNormal, scope: types.SCHEMAS, tag: "Conn", description: "Select", showInBar: true},
		{seq: "a", mode: types.ModeNormal, scope: types.SCHEMAS, tag: "Conn", description: "Add connection", showInBar: true},
		{seq: "r", mode: types.ModeNormal, scope: types.SCHEMAS, tag: "Conn", description: "Refresh rail", showInBar: true},
	})
	got := CollectOptionsForScope(ts, types.ModeNormal, types.SCHEMAS, nil, nil)
	if len(got) < 3 {
		t.Fatalf("CollectOptionsForScope(CONNECTIONS) = %v (len %d), want >=3", got, len(got))
	}
}

func TestCollectOptionsForScope_EmptyTrieSetReturnsEmpty(t *testing.T) {
	ts := keys.NewTrieSet()
	got := CollectOptionsForScope(ts, types.ModeNormal, types.TABLES, nil, nil)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	if got == nil {
		t.Errorf("got nil, want non-nil empty slice")
	}
}
