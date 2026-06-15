package keys

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// fixtureCmd builds a Command with a no-op handler. The Registry is not
// exercised here; tests construct *Command directly so the trie semantics
// are isolated from registry concerns.
func fixtureCmd(id string) *commands.Command {
	return &commands.Command{
		ID:      id,
		Handler: func(commands.ExecCtx) error { return nil },
	}
}

func keyG() Key    { return Key{Code: 'g'} }
func keyGG() []Key { return []Key{keyG(), keyG()} }

func TestChordTrie_EmptyLookup_RootIsFound(t *testing.T) {
	b := NewTrieBuilder()
	trie, warns := b.Build()
	if len(warns) != 0 {
		t.Errorf("empty trie warns = %v, want none", warns)
	}
	res := trie.Lookup(nil)
	if !res.Found {
		t.Error("Lookup([]) on empty trie: Found = false, want true (root always found)")
	}
	if res.IsLeaf {
		t.Error("Lookup([]) on empty trie: IsLeaf = true, want false")
	}
	if res.HasChildren {
		t.Error("Lookup([]) on empty trie: HasChildren = true, want false")
	}
}

func TestChordTrie_LookupUnknown_NotFound(t *testing.T) {
	b := NewTrieBuilder()
	trie, _ := b.Build()
	res := trie.Lookup([]Key{{Code: 'x'}})
	if res.Found {
		t.Errorf("Lookup of unknown prefix: Found = true, want false")
	}
	if res.IsLeaf || res.HasChildren {
		t.Errorf("Lookup of unknown prefix: %+v, want zero", res)
	}
}

func TestChordTrie_InsertDefault_LookupLeaf(t *testing.T) {
	b := NewTrieBuilder()
	cmd := fixtureCmd("app.quit")
	b.InsertDefault(&ChordBinding{
		Sequence: []Key{{Code: 'q'}},
		Mode:     types.ModeNormal,
		Scope:    types.GLOBAL,
		ActionID: "app.quit",
		Origin:   "test:1",
	}, cmd)
	trie, warns := b.Build()
	if len(warns) != 0 {
		t.Errorf("warns = %v, want none", warns)
	}

	res := trie.Lookup([]Key{{Code: 'q'}})
	if !res.Found || !res.IsLeaf {
		t.Fatalf("Lookup(q): %+v", res)
	}
	if res.Action != cmd {
		t.Errorf("Action = %v, want %v", res.Action, cmd)
	}
	if res.Source != ShippedDefault {
		t.Errorf("Source = %v, want ShippedDefault", res.Source)
	}
	if res.Origin != "test:1" {
		t.Errorf("Origin = %q, want test:1", res.Origin)
	}
}

func TestChordTrie_InteriorPrefix_NotLeaf(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{
		Sequence: keyGG(), ActionID: "list.first", Origin: "t",
	}, fixtureCmd("list.first"))
	trie, _ := b.Build()

	// 'g' alone is interior, not a leaf.
	res := trie.Lookup([]Key{keyG()})
	if !res.Found {
		t.Error("Lookup(g) on gg-trie: not found")
	}
	if res.IsLeaf {
		t.Error("Lookup(g) on gg-trie: IsLeaf = true, want false")
	}
	if !res.HasChildren {
		t.Error("Lookup(g) on gg-trie: HasChildren = false, want true")
	}

	// 'gg' is the leaf.
	res = trie.Lookup(keyGG())
	if !res.IsLeaf {
		t.Errorf("Lookup(gg): IsLeaf = false, want true")
	}
}

func TestChordTrie_UserOverlaysDefault(t *testing.T) {
	def := fixtureCmd("app.quit")
	user := fixtureCmd("custom.quit")

	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{
		Sequence: []Key{{Code: 'q'}}, ActionID: "app.quit", Origin: "default",
	}, def)
	b.InsertUser(&ChordBinding{
		Sequence: []Key{{Code: 'q'}}, ActionID: "custom.quit", Origin: "user:42", Source: UserOverride,
	}, user)
	trie, warns := b.Build()

	// User overlay of a default MUST NOT emit a collision warning.
	for _, w := range warns {
		if w.Code == "collision" {
			t.Errorf("unexpected collision warning on default→user overlay: %+v", w)
		}
	}

	res := trie.Lookup([]Key{{Code: 'q'}})
	if res.Action != user {
		t.Errorf("Action = %v, want user", res.Action)
	}
	if res.Source != UserOverride {
		t.Errorf("Source = %v, want UserOverride", res.Source)
	}
	if res.Origin != "user:42" {
		t.Errorf("Origin = %q, want user:42", res.Origin)
	}
}

func TestChordTrie_NopOverlay_CarriesNopCommand(t *testing.T) {
	def := fixtureCmd("app.quit")
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{
		Sequence: []Key{{Code: 'q'}}, ActionID: "app.quit", Origin: "default",
	}, def)
	b.InsertUser(&ChordBinding{
		Sequence: []Key{{Code: 'q'}}, ActionID: "<nop>", Origin: "user", Source: UserOverride,
	}, commands.NopCommand)
	trie, _ := b.Build()

	res := trie.Lookup([]Key{{Code: 'q'}})
	if res.Action != commands.NopCommand {
		t.Errorf("Action = %v, want NopCommand", res.Action)
	}
	if !commands.IsNop(res.Action.Handler) {
		t.Errorf("NopCommand handler is not nop")
	}
	if res.Source != UserOverride {
		t.Errorf("Source = %v, want UserOverride", res.Source)
	}
}

func TestChordTrie_DefaultCollision_LastWinsAndWarns(t *testing.T) {
	first := fixtureCmd("a")
	second := fixtureCmd("b")
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{
		Sequence: SequenceFromShorthandT(t, "<leader>tr"), ActionID: "a", Origin: "first.go:1",
	}, first)
	b.InsertDefault(&ChordBinding{
		Sequence: SequenceFromShorthandT(t, "<leader>tr"), ActionID: "b", Origin: "second.go:1",
	}, second)
	trie, warns := b.Build()

	// Expansion-free: SequenceFromShorthand returns KeyLeader; we never
	// expanded, so the binding will trigger unexpanded_leader on insert.
	// For pure-trie unit test, use bare runes.
	_ = trie
	_ = warns
}

func TestChordTrie_DefaultCollision_BareSequence(t *testing.T) {
	first := fixtureCmd("a")
	second := fixtureCmd("b")
	seq := []Key{{Code: 't'}, {Code: 'r'}}

	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: seq, ActionID: "a", Origin: "first"}, first)
	b.InsertDefault(&ChordBinding{Sequence: seq, ActionID: "b", Origin: "second"}, second)
	trie, warns := b.Build()

	foundCollision := false
	for _, w := range warns {
		if w.Code == "collision" {
			foundCollision = true
		}
	}
	if !foundCollision {
		t.Errorf("expected collision warning, got %v", warns)
	}

	res := trie.Lookup(seq)
	if res.Action != second {
		t.Errorf("last-wins: Action = %v, want second", res.Action)
	}
}

func TestChordTrie_AmbiguousPrefix_GAndGG(t *testing.T) {
	b := NewTrieBuilder()
	cmdG := fixtureCmd("g.alone")
	cmdGG := fixtureCmd("g.g")
	b.InsertDefault(&ChordBinding{Sequence: []Key{keyG()}, ActionID: "g.alone", Origin: "g_alone"}, cmdG)
	b.InsertDefault(&ChordBinding{Sequence: keyGG(), ActionID: "g.g", Origin: "g_g"}, cmdGG)
	_, warns := b.Build()

	foundAmb := false
	for _, w := range warns {
		if w.Code == "ambiguous_prefix" {
			foundAmb = true
		}
	}
	if !foundAmb {
		t.Errorf("expected ambiguous_prefix warning, got %v", warns)
	}
}

func TestChordTrie_NopLeafNotAmbiguousPrefix(t *testing.T) {
	// A <nop> on 'g' plus 'gg' should NOT report ambiguous_prefix: the
	// <nop> is an explicit unbind, not an active binding.
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{keyG()}, ActionID: "<nop>", Origin: "nop"}, commands.NopCommand)
	b.InsertDefault(&ChordBinding{Sequence: keyGG(), ActionID: "gg.go", Origin: "gg"}, fixtureCmd("gg.go"))
	_, warns := b.Build()
	for _, w := range warns {
		if w.Code == "ambiguous_prefix" {
			t.Errorf("unexpected ambiguous_prefix warning on <nop>: %+v", w)
		}
	}
}

func TestChordTrie_Walk_VisitsEveryLeafOnce(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'q'}}, ActionID: "q", Origin: "q"}, fixtureCmd("q"))
	b.InsertDefault(&ChordBinding{Sequence: keyGG(), ActionID: "gg", Origin: "gg"}, fixtureCmd("gg"))
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'd'}, {Code: 'd'}}, ActionID: "dd", Origin: "dd"}, fixtureCmd("dd"))
	trie, _ := b.Build()

	seen := map[string]int{}
	trie.Walk(func(seq []Key, leaf LookupResult) {
		seen[SequenceString(seq)]++
		if !leaf.IsLeaf {
			t.Errorf("Walk yielded non-leaf for seq %q", SequenceString(seq))
		}
	})
	want := []string{"q", "gg", "dd"}
	for _, s := range want {
		if seen[s] != 1 {
			t.Errorf("seq %q visited %d times, want 1", s, seen[s])
		}
	}
	if len(seen) != len(want) {
		t.Errorf("seen %d leaves, want %d: %v", len(seen), len(want), seen)
	}
}

func TestChordTrie_UnexpandedLeaderTriggersWarning(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{
		Sequence: []Key{{Special: KeyLeader}, {Code: 'x'}},
		ActionID: "test",
		Origin:   "tester",
	}, fixtureCmd("test"))
	_, warns := b.Build()
	found := false
	for _, w := range warns {
		if w.Code == "unexpanded_leader" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unexpanded_leader warning, got %v", warns)
	}
}

func TestChordTrie_ChildrenAt_EmptyPrefixReturnsRootChildrenSorted(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'q'}}, ActionID: "q", Origin: "q"}, fixtureCmd("q"))
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'a'}}, ActionID: "a", Origin: "a"}, fixtureCmd("a"))
	b.InsertDefault(&ChordBinding{Sequence: keyGG(), ActionID: "gg", Origin: "gg"}, fixtureCmd("gg"))
	trie, _ := b.Build()

	rows, ok := trie.ChildrenAt(nil)
	if !ok {
		t.Fatalf("ChildrenAt(nil) ok=false, want true")
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	want := []string{"a", "g", "q"}
	for i, r := range rows {
		if r.Key.String() != want[i] {
			t.Errorf("rows[%d].Key.String() = %q, want %q", i, r.Key.String(), want[i])
		}
	}
}

func TestChordTrie_ChildrenAt_LeafChildHasDescription(t *testing.T) {
	cmd := &commands.Command{ID: "app.quit", Description: "Quit", Handler: func(commands.ExecCtx) error { return nil }}
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'q'}}, ActionID: "app.quit", Origin: "q"}, cmd)
	trie, _ := b.Build()

	rows, ok := trie.ChildrenAt(nil)
	if !ok || len(rows) != 1 {
		t.Fatalf("ChildrenAt(nil): ok=%v rows=%+v", ok, rows)
	}
	r := rows[0]
	if !r.IsLeaf {
		t.Errorf("IsLeaf = false, want true")
	}
	if r.Label != "Quit" {
		t.Errorf("Label = %q, want %q", r.Label, "Quit")
	}
	if r.Source != ShippedDefault {
		t.Errorf("Source = %v, want ShippedDefault", r.Source)
	}
}

func TestChordTrie_ChildrenAt_InteriorChildHasEmptyLabel(t *testing.T) {
	// gg leaf creates an interior 'g' node. ChildrenAt(nil) yields one
	// row for 'g', which is interior → IsLeaf=false, Label="".
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: keyGG(), ActionID: "gg", Origin: "gg"}, fixtureCmd("gg"))
	trie, _ := b.Build()

	rows, ok := trie.ChildrenAt(nil)
	if !ok || len(rows) != 1 {
		t.Fatalf("ChildrenAt(nil): ok=%v rows=%+v", ok, rows)
	}
	r := rows[0]
	if r.IsLeaf {
		t.Errorf("IsLeaf = true, want false (interior)")
	}
	if r.Label != "" {
		t.Errorf("Label = %q, want empty for interior", r.Label)
	}
}

func TestChordTrie_ChildrenAt_NopLeafLabel(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'q'}}, ActionID: "<nop>", Origin: "nop"}, commands.NopCommand)
	trie, _ := b.Build()

	rows, ok := trie.ChildrenAt(nil)
	if !ok || len(rows) != 1 {
		t.Fatalf("ChildrenAt(nil): ok=%v rows=%+v", ok, rows)
	}
	if rows[0].Label != "(unbound)" {
		t.Errorf("Label = %q, want %q for <nop>", rows[0].Label, "(unbound)")
	}
}

func TestChordTrie_ChildrenAt_UnknownPrefixReturnsNotFound(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'q'}}, ActionID: "q", Origin: "q"}, fixtureCmd("q"))
	trie, _ := b.Build()

	rows, ok := trie.ChildrenAt([]Key{{Code: 'x'}})
	if ok {
		t.Errorf("ok = true for unknown prefix, want false")
	}
	if rows != nil {
		t.Errorf("rows = %+v, want nil for unknown prefix", rows)
	}
}

func TestChordTrie_ChildrenAt_LeafNoChildrenReturnsEmpty(t *testing.T) {
	b := NewTrieBuilder()
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'q'}}, ActionID: "q", Origin: "q"}, fixtureCmd("q"))
	trie, _ := b.Build()

	rows, ok := trie.ChildrenAt([]Key{{Code: 'q'}})
	if !ok {
		t.Errorf("ok = false, want true (leaf node exists)")
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty for terminal leaf", rows)
	}
	if rows == nil {
		t.Errorf("rows = nil, want non-nil empty slice")
	}
}

// SequenceFromShorthandT is a test helper that fails the test on parse
// error instead of returning it.
func SequenceFromShorthandT(t *testing.T, s string) []Key {
	t.Helper()
	seq, err := SequenceFromShorthand(s)
	if err != nil {
		t.Fatalf("SequenceFromShorthand(%q): %v", s, err)
	}
	return seq
}
