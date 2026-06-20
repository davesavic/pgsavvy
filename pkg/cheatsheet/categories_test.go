package cheatsheet

import (
	"slices"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

// expectedCategory pins the intended Category for every production tag.
// It is the fixture the taxonomy tests assert against; it must stay in
// lockstep with categoryByTag in categories.go.
var expectedCategory = map[string]Category{
	"Edit":         CategoryEditing,
	"Insert":       CategoryEditing,
	"Visual":       CategoryEditing,
	"Operator":     CategoryEditing,
	"Motion":       CategoryEditing,
	"Text object":  CategoryEditing,
	"Edit history": CategoryEditing,
	"Query":        CategoryQuery,
	"Plan":         CategoryQuery,
	"Result":       CategoryResults,
	"Row":          CategoryResults,
	"Cell Edit":    CategoryCells,
	"Commit":       CategoryCells,
	"Conflict":     CategoryCells,
	"Transaction":  CategorySession,
	"Session":      CategorySession,
	"Connection":   CategorySession,
	"Help":         CategorySession,
}

// TestCategoryFor_Total checks the mapping is total: every known tag,
// plus the empty string and an arbitrary unknown, resolves to a Category
// in Categories. "" and unknown tags fall back to General.
func TestCategoryFor_Total(t *testing.T) {
	inputs := append([]string{}, commands.KnownTags...)
	inputs = append(inputs, "", "totally-unknown-tag-xyz")

	for _, tag := range inputs {
		got := CategoryFor(tag)
		if !slices.Contains(Categories, got) {
			t.Errorf("CategoryFor(%q) = %q, not in Categories", tag, got)
		}
	}

	if got := CategoryFor(""); got != CategoryGeneral {
		t.Errorf(`CategoryFor("") = %q, want General`, got)
	}
	if got := CategoryFor("totally-unknown-tag-xyz"); got != CategoryGeneral {
		t.Errorf("CategoryFor(unknown) = %q, want General", got)
	}
}

// TestCategoryFor_ExpectedMapping asserts every production tag maps to the
// Category the taxonomy intends (per the expectedCategory fixture), and
// that none silently falls through to General.
func TestCategoryFor_ExpectedMapping(t *testing.T) {
	for _, tag := range commands.KnownTags {
		want, ok := expectedCategory[tag]
		if !ok {
			t.Fatalf("KnownTag %q missing from expectedCategory fixture", tag)
		}
		if got := CategoryFor(tag); got != want {
			t.Errorf("CategoryFor(%q) = %q, want %q", tag, got, want)
		}
		if CategoryFor(tag) == CategoryGeneral {
			t.Errorf("KnownTag %q fell through to General; categorize it explicitly", tag)
		}
	}
}

// TestExplicitMapCoversKnownTags fails loudly if a production tag is added
// to commands.KnownTags without an explicit categoryByTag entry, or vice
// versa: the domain of categoryByTag must equal commands.KnownTags exactly.
func TestExplicitMapCoversKnownTags(t *testing.T) {
	if len(categoryByTag) != len(commands.KnownTags) {
		t.Errorf("categoryByTag has %d entries, commands.KnownTags has %d; sets must match",
			len(categoryByTag), len(commands.KnownTags))
	}

	for _, tag := range commands.KnownTags {
		if _, ok := categoryByTag[tag]; !ok {
			t.Errorf("KnownTag %q has no explicit categoryByTag entry", tag)
		}
	}

	for tag := range categoryByTag {
		if !slices.Contains(commands.KnownTags, tag) {
			t.Errorf("categoryByTag has tag %q not in commands.KnownTags", tag)
		}
	}
}

// TestCategoryLabels_NonEmpty ensures every Category has a non-empty i18n
// label, so no empty/typo'd field ships.
func TestCategoryLabels_NonEmpty(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	for _, c := range Categories {
		if LabelFor(c, tr) == "" {
			t.Errorf("Category %q has empty i18n label", c)
		}
	}
}
