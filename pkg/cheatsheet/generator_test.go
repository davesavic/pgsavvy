package cheatsheet

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// buildTrie is a tiny per-(mode,scope) helper for tests: takes a slice
// of (sequenceShorthand, *Command, Source, tag) tuples and returns a
// finalised ChordTrie. The Source field on each ChordBinding is honoured
// — InsertDefault overwrites it to ShippedDefault, InsertUser preserves
// it. To produce a UserOverride leaf in a test trie we go through
// InsertUser; for ShippedDefault, InsertDefault.
type entry struct {
	seq    string
	cmd    *commands.Command
	source types.Source
}

func buildTrie(t *testing.T, entries []entry) *keys.ChordTrie {
	t.Helper()
	b := keys.NewTrieBuilder()
	for _, e := range entries {
		seq, err := keys.SequenceFromShorthand(e.seq)
		if err != nil {
			t.Fatalf("SequenceFromShorthand(%q): %v", e.seq, err)
		}
		cb := &keys.ChordBinding{
			Sequence: seq,
			Source:   e.source,
			Origin:   "test",
		}
		switch e.source {
		case types.ShippedDefault:
			b.InsertDefault(cb, e.cmd)
		default:
			b.InsertUser(cb, e.cmd)
		}
	}
	trie, _ := b.Build()
	return trie
}

func TestGenerate_NilTrieReturnsZeroOutput(t *testing.T) {
	out := Generate(GenerateInput{Trie: nil, Scope: types.TABLES})
	if len(out.CurrentScope) != 0 || len(out.Global) != 0 {
		t.Fatalf("nil trie → non-zero output: %+v", out)
	}
}

func TestGenerate_EmptyTrieSetReturnsZeroOutput(t *testing.T) {
	ts := keys.NewTrieSet()
	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})
	if len(out.CurrentScope) != 0 || len(out.Global) != 0 {
		t.Fatalf("empty TrieSet → non-zero output: %+v", out)
	}
}

func TestGenerate_CurrentScopeAndGlobalPartition(t *testing.T) {
	truncate := &commands.Command{ID: "table.truncate", Description: "Truncate table", Tag: "DDL", Handler: commands.NopSentinel}
	drop := &commands.Command{ID: "table.drop", Description: "Drop table", Tag: "DDL", Handler: commands.NopSentinel}
	quit := &commands.Command{ID: "app.quit", Description: "Quit", Tag: "", Handler: commands.NopSentinel}

	tablesTrie := buildTrie(t, []entry{
		{seq: "tr", cmd: truncate, source: types.ShippedDefault},
		{seq: "td", cmd: drop, source: types.UserOverride},
	})
	globalTrie := buildTrie(t, []entry{
		{seq: "qq", cmd: quit, source: types.ShippedDefault},
	})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, tablesTrie)
	ts.Set(types.ModeNormal, types.GLOBAL, globalTrie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	if len(out.CurrentScope) != 1 {
		t.Fatalf("CurrentScope = %+v, want 1 ModeView", out.CurrentScope)
	}
	if out.CurrentScope[0].Mode != types.ModeNormal {
		t.Fatalf("CurrentScope[0].Mode = %s, want Normal", out.CurrentScope[0].Mode)
	}
	if len(out.CurrentScope[0].Sections) != 1 || out.CurrentScope[0].Sections[0].Tag != "DDL" {
		t.Fatalf("CurrentScope[0].Sections = %+v, want one [DDL] section", out.CurrentScope[0].Sections)
	}
	rows := out.CurrentScope[0].Sections[0].Rows
	if len(rows) != 2 {
		t.Fatalf("DDL rows = %d, want 2", len(rows))
	}
	// Rows sorted by Key: "<leader>td" < "<leader>tr"
	if rows[0].Key != "td" || rows[1].Key != "tr" {
		t.Fatalf("rows not sorted by Key: got [%q, %q]", rows[0].Key, rows[1].Key)
	}
	if rows[0].Glyph != GlyphOverride {
		t.Fatalf("td glyph = %q, want %q (UserOverride)", rows[0].Glyph, GlyphOverride)
	}
	if rows[1].Glyph != GlyphDefault {
		t.Fatalf("tr glyph = %q, want %q (ShippedDefault)", rows[1].Glyph, GlyphDefault)
	}

	if len(out.Global) != 1 || out.Global[0].Mode != types.ModeNormal {
		t.Fatalf("Global ModeView wrong: %+v", out.Global)
	}
	if rows := out.Global[0].Sections[0].Rows; len(rows) != 1 || rows[0].Key != "qq" {
		t.Fatalf("Global rows wrong: %+v", rows)
	}
}

func TestGenerate_EmptyModeOmitted(t *testing.T) {
	// TABLES has no Visual mode bindings; Visual must NOT appear.
	cmd := &commands.Command{ID: "table.x", Description: "x", Tag: "DDL", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	for _, mv := range out.CurrentScope {
		if mv.Mode == types.ModeVisual {
			t.Fatalf("expected Visual mode to be omitted, got %+v", mv)
		}
	}
}

func TestGenerate_OtherScopesIgnored(t *testing.T) {
	cmd := &commands.Command{ID: "x", Description: "x", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.SCHEMAS, trie) // not the focused scope

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	if len(out.CurrentScope) != 0 {
		t.Fatalf("CurrentScope = %+v, want empty (SCHEMAS is not the focused scope)", out.CurrentScope)
	}
	if len(out.Global) != 0 {
		t.Fatalf("Global = %+v, want empty (SCHEMAS is not GLOBAL)", out.Global)
	}
}

func TestGenerate_TagOrderingEmptyLast(t *testing.T) {
	// Three rows across tags "Z", "A", "" — sections must be A, Z, "" in that order.
	a := &commands.Command{ID: "a", Description: "a", Tag: "A", Handler: commands.NopSentinel}
	z := &commands.Command{ID: "z", Description: "z", Tag: "Z", Handler: commands.NopSentinel}
	none := &commands.Command{ID: "n", Description: "n", Tag: "", Handler: commands.NopSentinel}

	trie := buildTrie(t, []entry{
		{seq: "a", cmd: a, source: types.ShippedDefault},
		{seq: "z", cmd: z, source: types.ShippedDefault},
		{seq: "n", cmd: none, source: types.ShippedDefault},
	})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	tags := []string{}
	for _, s := range out.CurrentScope[0].Sections {
		tags = append(tags, s.Tag)
	}
	want := []string{"A", "Z", ""}
	if len(tags) != len(want) {
		t.Fatalf("tag order = %v, want %v", tags, want)
	}
	for i := range tags {
		if tags[i] != want[i] {
			t.Fatalf("tag[%d] = %q, want %q", i, tags[i], want[i])
		}
	}
}

func TestGenerate_GlyphPerSource(t *testing.T) {
	cases := []struct {
		source types.Source
		want   rune
	}{
		{types.ShippedDefault, GlyphDefault},
		{types.UserOverride, GlyphOverride},
		{types.CustomCmd, GlyphCustom},
	}
	for _, c := range cases {
		cmd := &commands.Command{ID: "x", Description: "x", Handler: commands.NopSentinel}
		trie := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: c.source}})

		ts := keys.NewTrieSet()
		ts.Set(types.ModeNormal, types.TABLES, trie)
		out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

		if g := out.CurrentScope[0].Sections[0].Rows[0].Glyph; g != c.want {
			t.Fatalf("source=%v glyph = %q, want %q", c.source, g, c.want)
		}
	}
}

func TestGenerate_BlankDescriptionFallsBackToActionID(t *testing.T) {
	// Defensive AC: binding with empty Description renders <actionID> so
	// the row is never blank.
	cmd := &commands.Command{ID: "table.x", Description: "", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	if d := out.CurrentScope[0].Sections[0].Rows[0].Description; d != "table.x" {
		t.Fatalf("Description = %q, want %q (fallback to ActionID)", d, "table.x")
	}
}

func TestGenerate_SameKeyDifferentModes(t *testing.T) {
	// Two bindings on the same Key in different modes render once per mode.
	cmd := &commands.Command{ID: "x", Description: "x", Handler: commands.NopSentinel}
	t1 := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})
	t2 := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, t1)
	ts.Set(types.ModeInsert, types.TABLES, t2)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	if len(out.CurrentScope) != 2 {
		t.Fatalf("ModeViews = %d, want 2 (Normal + Insert)", len(out.CurrentScope))
	}
}

// TestGenerate_PreservesLeaderTokenInKey is the regression for
// chord bindings written as `<leader>q` must render with
// the raw `<leader>` token in the cheatsheet's Key column — NOT the
// post-expansion rune (`Space q`) and NOT a bare `q` (the old bug
// dropped the prefix entirely).
//
// Single-key bindings must be unaffected.
func TestGenerate_PreservesLeaderTokenInKey(t *testing.T) {
	qcmd := &commands.Command{ID: "app.quit", Description: "Quit", Tag: "App", Handler: commands.NopSentinel}
	bare := &commands.Command{ID: "table.bare", Description: "bare q", Tag: "App", Handler: commands.NopSentinel}

	// Build a trie with the POST-expanded sequences (matching production:
	// keys.expandLeaderTokens runs before the trie insert).
	b := keys.NewTrieBuilder()
	b.InsertDefault(&keys.ChordBinding{
		Sequence: []keys.Key{{Code: ' '}, {Code: 'q'}}, // <leader>q expanded
		Source:   types.ShippedDefault,
		Origin:   "test",
	}, qcmd)
	b.InsertDefault(&keys.ChordBinding{
		Sequence: []keys.Key{{Code: 'p'}}, // single key, no leader
		Source:   types.ShippedDefault,
		Origin:   "test",
	}, bare)
	trie, _ := b.Build()

	ts := keys.NewTrieSet()
	ts.Leader = ' '
	ts.LocalLeader = ','
	ts.Set(types.ModeNormal, types.GLOBAL, trie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	if len(out.Global) != 1 {
		t.Fatalf("Global ModeViews = %d, want 1", len(out.Global))
	}
	if len(out.Global[0].Sections) != 1 {
		t.Fatalf("Global Sections = %d, want 1", len(out.Global[0].Sections))
	}
	rows := out.Global[0].Sections[0].Rows
	// Two rows, sorted by Key string: "<leader>q" sorts before "p"
	// because '<' (0x3C) < 'p' (0x70).
	keysSeen := map[string]bool{}
	for _, r := range rows {
		keysSeen[r.Key] = true
	}
	if !keysSeen["<leader>q"] {
		t.Errorf("expected a row with Key=%q; got rows=%+v", "<leader>q", rows)
	}
	if !keysSeen["p"] {
		t.Errorf("expected a row with Key=%q (single-key unchanged); got rows=%+v", "p", rows)
	}
	// Negative assertion: the post-expanded form must NOT leak.
	if keysSeen["<space>q"] || keysSeen[" q"] {
		t.Errorf("expanded leader rune leaked into Key column: %+v", rows)
	}
	// Negative assertion for the original bug: the bare `q` form must
	// not appear — that would mean the `<leader>` prefix was dropped.
	if keysSeen["q"] {
		t.Errorf("chord binding rendered as bare %q; <leader> prefix was dropped: %+v", "q", rows)
	}
}

// TestGenerate_PreservesLocalLeaderTokenInKey verifies the same
// reverse-mapping for `<localleader>` with a non-default rune (`;`),
// proving the cheatsheet stays stable when the user reconfigures
// localleader away from the default `,`.
func TestGenerate_PreservesLocalLeaderTokenInKey(t *testing.T) {
	cmd := &commands.Command{ID: "x", Description: "x", Tag: "T", Handler: commands.NopSentinel}

	b := keys.NewTrieBuilder()
	b.InsertDefault(&keys.ChordBinding{
		Sequence: []keys.Key{{Code: ';'}, {Code: 'x'}}, // <localleader>x expanded with non-default rune
		Source:   types.ShippedDefault,
		Origin:   "test",
	}, cmd)
	trie, _ := b.Build()

	ts := keys.NewTrieSet()
	ts.Leader = ' '
	ts.LocalLeader = ';' // user reconfigured localleader to ;
	ts.Set(types.ModeNormal, types.TABLES, trie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	if len(out.CurrentScope) != 1 || len(out.CurrentScope[0].Sections) != 1 {
		t.Fatalf("CurrentScope shape unexpected: %+v", out.CurrentScope)
	}
	rows := out.CurrentScope[0].Sections[0].Rows
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Key != "<localleader>x" {
		t.Errorf("Key = %q, want %q (no rune leak)", rows[0].Key, "<localleader>x")
	}
	if rows[0].Key == ";x" {
		t.Errorf("runtime localleader rune leaked into cheatsheet: %q", rows[0].Key)
	}
}

func TestGenerate_DeterministicForSameInput(t *testing.T) {
	cmd := &commands.Command{ID: "x", Description: "x", Tag: "T", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{
		{seq: "a", cmd: cmd, source: types.ShippedDefault},
		{seq: "b", cmd: cmd, source: types.ShippedDefault},
		{seq: "c", cmd: cmd, source: types.ShippedDefault},
	})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)

	first := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})
	for range 20 {
		next := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})
		if len(first.CurrentScope) != len(next.CurrentScope) {
			t.Fatalf("non-determinism in Generate")
		}
		for j, mv := range first.CurrentScope {
			if len(mv.Sections) != len(next.CurrentScope[j].Sections) {
				t.Fatalf("non-determinism in section count")
			}
			for k, s := range mv.Sections {
				for r, row := range s.Rows {
					if row.Key != next.CurrentScope[j].Sections[k].Rows[r].Key {
						t.Fatalf("non-determinism in row order at [%d][%d][%d]", j, k, r)
					}
				}
			}
		}
	}
}
