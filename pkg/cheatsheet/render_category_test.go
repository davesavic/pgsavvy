package cheatsheet

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/mattn/go-runewidth"
)

// row is a small helper to build a Row with the glyph derived from source,
// mirroring generator.glyphFor so tests exercise the real glyph slot logic.
func row(key, desc string, src types.Source) Row {
	return Row{Key: key, Description: desc, Source: src, Glyph: glyphFor(src)}
}

func catView(c Category, current, global []ModeView) CategoryView {
	return CategoryView{Category: c, CurrentScope: current, Global: global}
}

func modeView(m types.Mode, rows ...Row) ModeView {
	return ModeView{Mode: m, Sections: []Section{{Tag: "T", Rows: rows}}}
}

// descColumn returns the display column at which the description begins in a
// rendered row (i.e. the StringWidth of everything before the description).
func descColumn(t *testing.T, line, desc string) int {
	t.Helper()
	idx := strings.Index(line, desc)
	if idx < 0 {
		// Alignment tests use untruncated descriptions; a miss means a bad
		// fixture — fail loudly.
		t.Fatalf("description %q not found in line %q", desc, line)
	}
	return runewidth.StringWidth(line[:idx])
}

func bodyLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestRenderCategory_TwoColumnAligned(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal,
			row("a", "alpha", types.ShippedDefault),
			row("ggg", "gamma", types.ShippedDefault),
		)},
		[]ModeView{modeView(types.ModeNormal,
			row("zz", "zeta", types.ShippedDefault),
		)},
	)

	got := RenderCategory(cv, tr)

	if !strings.Contains(got, tr.CheatsheetCurrentScopeTab) {
		t.Fatalf("missing current-scope sub-header: %q", got)
	}
	if !strings.Contains(got, tr.CheatsheetGlobalTab) {
		t.Fatalf("missing global sub-header: %q", got)
	}

	cols := map[int]struct{}{}
	for desc, line := range map[string]string{
		"alpha": findLine(t, got, "alpha"),
		"gamma": findLine(t, got, "gamma"),
		"zeta":  findLine(t, got, "zeta"),
	} {
		cols[descColumn(t, line, desc)] = struct{}{}
	}
	if len(cols) != 1 {
		t.Fatalf("description columns not aligned: %v\nbody:\n%s", cols, got)
	}
}

func TestRenderCategory_OnlyOneScopeSubHeader(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal, row("a", "alpha", types.ShippedDefault))},
		nil,
	)
	got := RenderCategory(cv, tr)
	if !strings.Contains(got, tr.CheatsheetCurrentScopeTab) {
		t.Fatalf("missing current-scope sub-header: %q", got)
	}
	if strings.Contains(got, tr.CheatsheetGlobalTab) {
		t.Fatalf("global sub-header present with no global rows: %q", got)
	}
}

func TestRenderCategory_GlyphOnlyWhenNonDefault(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal,
			row("a", "alpha", types.ShippedDefault),
			row("b", "bravo", types.UserOverride),
		)},
		nil,
	)
	got := RenderCategory(cv, tr)

	defLine := findLine(t, got, "alpha")
	ovrLine := findLine(t, got, "bravo")

	if strings.ContainsRune(defLine, GlyphDefault) {
		t.Fatalf("default row prints '·': %q", defLine)
	}
	if strings.ContainsRune(defLine, GlyphOverride) || strings.ContainsRune(defLine, GlyphCustom) {
		t.Fatalf("default row prints a glyph: %q", defLine)
	}
	if !strings.ContainsRune(ovrLine, GlyphOverride) {
		t.Fatalf("override row missing '✱': %q", ovrLine)
	}

	if c1, c2 := descColumn(t, defLine, "alpha"), descColumn(t, ovrLine, "bravo"); c1 != c2 {
		t.Fatalf("mixed glyph/no-glyph descriptions misaligned: %d vs %d\n%q\n%q", c1, c2, defLine, ovrLine)
	}
}

func TestRenderCategory_FooterLegendOnce(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal,
			row("a", "alpha", types.ShippedDefault),
			row("b", "bravo", types.UserOverride),
		)},
		[]ModeView{modeView(types.ModeNormal, row("g", "global", types.ShippedDefault))},
	)
	got := RenderCategory(cv, tr)

	if c := strings.Count(got, tr.CheatsheetLegend); c != 1 {
		t.Fatalf("legend appears %d times, want 1", c)
	}
	lines := bodyLines(got)
	if last := lines[len(lines)-1]; last != tr.CheatsheetLegend {
		t.Fatalf("last non-empty line = %q, want legend %q", last, tr.CheatsheetLegend)
	}
}

func TestRenderCategory_HeadersBoldAndColoured(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal, row("a", "alpha", types.ShippedDefault))},
		nil,
	)
	got := RenderCategory(cv, tr)

	scopeLine := findLineContaining(t, got, tr.CheatsheetCurrentScopeTab)
	modeLine := findLineContaining(t, got, modeLabel(types.ModeNormal))
	rowLine := findLine(t, got, "alpha")

	// Both header lines are bolded and wrapped with a reset; the row is not.
	for name, line := range map[string]string{"scope": scopeLine, "mode": modeLine} {
		if !strings.Contains(line, ansiBold) {
			t.Fatalf("%s header not bolded: %q", name, line)
		}
		if !strings.HasSuffix(line, theme.AnsiReset) {
			t.Fatalf("%s header not reset-terminated: %q", name, line)
		}
	}
	if strings.Contains(rowLine, ansiBold) {
		t.Fatalf("binding row should not be styled as a header: %q", rowLine)
	}
}

func TestRenderCategory_TruncatesOverflow(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	longDesc := strings.Repeat("description-word ", 20)
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal,
			row("k", longDesc, types.ShippedDefault),
		)},
		nil,
	)
	got := RenderCategory(cv, tr)

	line := findLineContaining(t, got, "description-word")
	if w := runewidth.StringWidth(line); w > cheatsheetMaxCols {
		t.Fatalf("row width %d exceeds cap %d: %q", w, cheatsheetMaxCols, line)
	}
	if !strings.HasSuffix(line, "…") {
		t.Fatalf("truncated row missing ellipsis: %q", line)
	}
}

func TestRenderCategory_OverlongKeyGrowsToCapAndDescTruncates(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	longKey := strings.Repeat("K", 80)
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal,
			row(longKey, "some description here that should truncate", types.ShippedDefault),
		)},
		nil,
	)
	got := RenderCategory(cv, tr)

	line := findLineContaining(t, got, "KKK")
	if w := runewidth.StringWidth(line); w > cheatsheetMaxCols {
		t.Fatalf("over-long-key row width %d exceeds cap %d: %q", w, cheatsheetMaxCols, line)
	}
	// Key grew only to the cap, not its full 80 cols.
	if strings.Count(line, "K") > categoryKeyColCap {
		t.Fatalf("key not capped at %d: %q", categoryKeyColCap, line)
	}
}

func TestRenderCategory_SingleBindingNoMisalignment(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cv := catView(CategoryEditing,
		[]ModeView{modeView(types.ModeNormal, row("x", "solo", types.ShippedDefault))},
		nil,
	)
	got := RenderCategory(cv, tr)
	line := findLine(t, got, "solo")
	if w := runewidth.StringWidth(line); w > cheatsheetMaxCols {
		t.Fatalf("single-binding row width %d exceeds cap: %q", w, line)
	}
}

func TestRenderCategory_EmptyGeneralShowsSentinel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := RenderCategory(catView(CategoryGeneral, nil, nil), tr)
	if got == "" {
		t.Fatalf("empty General rendered blank body")
	}
	if !strings.Contains(got, tr.CheatsheetEmpty) {
		t.Fatalf("empty General missing sentinel: %q", got)
	}
}

func TestRenderCategory_NilTranslationSet(t *testing.T) {
	if got := RenderCategory(catView(CategoryGeneral, nil, nil), nil); got != "" {
		t.Fatalf("RenderCategory(nil tr) = %q, want empty", got)
	}
}

func TestRenderCategory_NonEmptyBodyForBucketedCategory(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	for _, c := range Categories {
		cv := catView(c,
			[]ModeView{modeView(types.ModeNormal, row("a", "alpha", types.ShippedDefault))},
			nil,
		)
		if got := RenderCategory(cv, tr); strings.TrimSpace(got) == "" {
			t.Fatalf("category %s with rows rendered blank body", c)
		}
	}
}

func TestCheatsheetTabStrip_WorstCaseSixLabelsFit(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	// gocui tab separator is " - " (3 cols) per gocui/gui.go:1034.
	const sepWidth = 3

	sum := 0
	for _, c := range Categories {
		sum += runewidth.StringWidth(LabelFor(c, tr))
	}
	n := len(Categories)
	// +2 for the active tab's [ ] brackets (tabbed_rail_context.go:322).
	realized := sum + (n-1)*sepWidth + 2

	if realized > cheatsheetMaxCols {
		t.Fatalf("6-label tab strip width %d exceeds cap %d", realized, cheatsheetMaxCols)
	}
}

// findLine returns the single rendered line whose content equals the row
// carrying desc (the first line that contains desc as a substring).
func findLine(t *testing.T, body, desc string) string {
	t.Helper()
	return findLineContaining(t, body, desc)
}

func findLineContaining(t *testing.T, body, sub string) string {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	t.Fatalf("no line contains %q in:\n%s", sub, body)
	return ""
}
