package cheatsheet

import (
	"sort"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// catRowKey is the full-fidelity equality key for the no-loss proof. It
// carries Tag and Source in addition to (Mode, Scope, Key) so a bucketing bug
// that zeroes those fields fails the set-equality assertion loudly.
type catRowKey struct {
	Mode   types.Mode
	Scope  types.ContextKey
	Key    string
	Tag    string
	Source types.Source
}

// flattenOutput unions every Row in an Output into a full-fidelity set,
// attributing CurrentScope rows to sc and Global rows to types.GLOBAL —
// mirroring collectGenerateRows' attribution.
func flattenOutput(out Output, sc types.ContextKey) map[catRowKey]struct{} {
	set := map[catRowKey]struct{}{}
	addRows(set, out.CurrentScope, sc)
	addRows(set, out.Global, types.GLOBAL)
	return set
}

// flattenCategoryViews unions every Row across all CategoryViews into a
// full-fidelity set, using the same scope attribution as flattenOutput.
func flattenCategoryViews(views []CategoryView, sc types.ContextKey) map[catRowKey]struct{} {
	set := map[catRowKey]struct{}{}
	for _, v := range views {
		addRows(set, v.CurrentScope, sc)
		addRows(set, v.Global, types.GLOBAL)
	}
	return set
}

func addRows(set map[catRowKey]struct{}, views []ModeView, scope types.ContextKey) {
	for _, mv := range views {
		for _, sect := range mv.Sections {
			for _, row := range sect.Rows {
				set[catRowKey{
					Mode:   mv.Mode,
					Scope:  scope,
					Key:    row.Key,
					Tag:    row.Tag,
					Source: row.Source,
				}] = struct{}{}
			}
		}
	}
}

// TestGenerate_CategoryBucketing_NoLoss proves Categorize neither loses,
// duplicates, nor corrupts rows: over the production trie's probe scopes,
// the flattened CategoryViews equal the flattened Output by the full
// (Mode, Scope, Key, Tag, Source) tuple, and every (Mode, Scope, Key)
// lands under exactly one Category.
func TestGenerate_CategoryBucketing_NoLoss(t *testing.T) {
	t.Parallel()
	trieSet, _, _ := buildProductionTrieSet(t)
	probeScopes := scopesFromTrieSet(trieSet)

	// rowKey → set of categories it appears in (must be size 1 for all).
	rowCategories := map[rowKey]map[Category]struct{}{}

	for _, sc := range probeScopes {
		out := Generate(GenerateInput{Trie: trieSet, Scope: sc})
		views := Categorize(out)

		want := flattenOutput(out, sc)
		got := flattenCategoryViews(views, sc)
		assertSetEqual(t, sc, want, got)

		recordCategories(rowCategories, views, sc)
	}

	for rk, cats := range rowCategories {
		if len(cats) != 1 {
			t.Errorf("%s appears under %d categories (want exactly 1): %v", rk, len(cats), categorySet(cats))
		}
	}
}

func recordCategories(into map[rowKey]map[Category]struct{}, views []CategoryView, sc types.ContextKey) {
	mark := func(modeViews []ModeView, scope types.ContextKey, cat Category) {
		for _, mv := range modeViews {
			for _, sect := range mv.Sections {
				for _, row := range sect.Rows {
					rk := rowKey{Mode: mv.Mode, Scope: scope, Key: row.Key}
					if into[rk] == nil {
						into[rk] = map[Category]struct{}{}
					}
					into[rk][cat] = struct{}{}
				}
			}
		}
	}
	for _, v := range views {
		mark(v.CurrentScope, sc, v.Category)
		mark(v.Global, types.GLOBAL, v.Category)
	}
}

func categorySet(cats map[Category]struct{}) []Category {
	out := make([]Category, 0, len(cats))
	for c := range cats {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func assertSetEqual(t *testing.T, sc types.ContextKey, want, got map[catRowKey]struct{}) {
	t.Helper()
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("scope=%s: row in Output missing from Categorize: %+v", sc, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("scope=%s: row in Categorize missing from Output: %+v", sc, k)
		}
	}
}

// TestCategorize_AlwaysGeneral asserts that an empty Output still yields
// exactly one CategoryView (CategoryGeneral) carrying zero rows.
func TestCategorize_AlwaysGeneral(t *testing.T) {
	t.Parallel()
	views := Categorize(Output{})
	if len(views) != 1 {
		t.Fatalf("Categorize(Output{}) = %d views, want 1", len(views))
	}
	if views[0].Category != CategoryGeneral {
		t.Fatalf("sole view category = %q, want %q", views[0].Category, CategoryGeneral)
	}
	if n := countRows(views[0]); n != 0 {
		t.Fatalf("CategoryGeneral carries %d rows, want 0", n)
	}
}

func countRows(v CategoryView) int {
	n := 0
	for _, modeViews := range [][]ModeView{v.CurrentScope, v.Global} {
		for _, mv := range modeViews {
			for _, sect := range mv.Sections {
				n += len(sect.Rows)
			}
		}
	}
	return n
}

// TestCategorize_OrderAndNonEmpty asserts: result order follows Categories;
// non-General categories appear only when they have rows; General is always
// present and last.
func TestCategorize_OrderAndNonEmpty(t *testing.T) {
	t.Parallel()
	trieSet, _, _ := buildProductionTrieSet(t)
	probeScopes := scopesFromTrieSet(trieSet)

	for _, sc := range probeScopes {
		views := Categorize(Generate(GenerateInput{Trie: trieSet, Scope: sc}))

		if len(views) == 0 {
			t.Fatalf("scope=%s: Categorize returned no views", sc)
		}

		// Order follows Categories (a subsequence of it).
		assertOrderedSubsequence(t, sc, views)

		// General always present and last.
		last := views[len(views)-1]
		if last.Category != CategoryGeneral {
			t.Errorf("scope=%s: last category = %q, want %q", sc, last.Category, CategoryGeneral)
		}

		// Non-General categories appear only when non-empty.
		for _, v := range views {
			if v.Category == CategoryGeneral {
				continue
			}
			if len(v.CurrentScope) == 0 && len(v.Global) == 0 {
				t.Errorf("scope=%s: non-General category %q present with zero rows", sc, v.Category)
			}
		}
	}
}

func assertOrderedSubsequence(t *testing.T, sc types.ContextKey, views []CategoryView) {
	t.Helper()
	rank := map[Category]int{}
	for i, c := range Categories {
		rank[c] = i
	}
	prev := -1
	for _, v := range views {
		r, ok := rank[v.Category]
		if !ok {
			t.Errorf("scope=%s: category %q not in Categories", sc, v.Category)
			continue
		}
		if r <= prev {
			t.Errorf("scope=%s: category %q (rank %d) out of order after rank %d", sc, v.Category, r, prev)
		}
		prev = r
	}
}
