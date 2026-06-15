package cheatsheet

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

func TestRender_NilTranslationSet(t *testing.T) {
	if got := Render(Output{}, nil, "tables"); got != "" {
		t.Fatalf("Render(nil tr) = %q, want empty", got)
	}
}

func TestRender_EmptyOutputShowsTitleLegendAndSentinel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := Render(Output{}, tr, "tables")

	if !strings.Contains(got, tr.CheatsheetTitle) {
		t.Fatalf("missing title in: %q", got)
	}
	if !strings.Contains(got, tr.CheatsheetLegend) {
		t.Fatalf("missing legend in: %q", got)
	}
	if !strings.Contains(got, tr.CheatsheetEmpty) {
		t.Fatalf("missing empty sentinel in: %q", got)
	}
}

func TestRender_LegendOnlyAppearsOnce(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cmd := &commands.Command{ID: "x", Description: "x", Tag: "T", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})
	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)
	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	got := Render(out, tr, "tables")

	if c := strings.Count(got, tr.CheatsheetLegend); c != 1 {
		t.Fatalf("legend appears %d times, want 1", c)
	}
}

func TestRender_Deterministic(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cmd := &commands.Command{ID: "x", Description: "x", Tag: "T", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{
		{seq: "a", cmd: cmd, source: types.ShippedDefault},
		{seq: "b", cmd: cmd, source: types.ShippedDefault},
	})
	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)
	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	first := Render(out, tr, "tables")
	for i := range 10 {
		if next := Render(out, tr, "tables"); next != first {
			t.Fatalf("Render not deterministic at iter %d", i)
		}
	}
}

func TestRender_BannerForCurrentScopeAndGlobal(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	a := &commands.Command{ID: "a", Description: "A", Tag: "T", Handler: commands.NopSentinel}
	g := &commands.Command{ID: "g", Description: "G", Tag: "T", Handler: commands.NopSentinel}

	scopeTrie := buildTrie(t, []entry{{seq: "a", cmd: a, source: types.ShippedDefault}})
	globalTrie := buildTrie(t, []entry{{seq: "g", cmd: g, source: types.ShippedDefault}})

	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, scopeTrie)
	ts.Set(types.ModeNormal, types.GLOBAL, globalTrie)

	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})
	got := Render(out, tr, "tables")

	if !strings.Contains(got, tr.CheatsheetCurrentScopeTab) {
		t.Fatalf("missing current-scope tab: %q", got)
	}
	if !strings.Contains(got, "tables") {
		t.Fatalf("missing scope label %q in: %q", "tables", got)
	}
	if !strings.Contains(got, tr.CheatsheetGlobalTab) {
		t.Fatalf("missing global tab: %q", got)
	}
	if !strings.Contains(got, "A") {
		t.Fatalf("missing scoped description %q in: %q", "A", got)
	}
	if !strings.Contains(got, "G") {
		t.Fatalf("missing global description %q in: %q", "G", got)
	}
}

func TestScopeLabel_AllReplacedWithFriendlyLabel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	if got := ScopeLabel("all", tr); got != tr.CheatsheetScopeAllLabel {
		t.Fatalf("ScopeLabel(all) = %q, want %q", got, tr.CheatsheetScopeAllLabel)
	}
	if got := ScopeLabel(types.TABLES, tr); got != "Tables" {
		t.Fatalf("ScopeLabel(TABLES) = %q, want %q", got, "Tables")
	}
	if got := ScopeLabel("all", nil); got != "All" {
		t.Fatalf("ScopeLabel(all, nil tr) = %q, want %q", got, "All")
	}
}

func TestRender_TagWrappedInBrackets(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cmd := &commands.Command{ID: "x", Description: "x", Tag: "DDL", Handler: commands.NopSentinel}
	trie := buildTrie(t, []entry{{seq: "x", cmd: cmd, source: types.ShippedDefault}})
	ts := keys.NewTrieSet()
	ts.Set(types.ModeNormal, types.TABLES, trie)
	out := Generate(GenerateInput{Trie: ts, Scope: types.TABLES})

	got := Render(out, tr, "tables")

	if !strings.Contains(got, "[DDL]") {
		t.Fatalf("expected '[DDL]' section header in: %q", got)
	}
}
