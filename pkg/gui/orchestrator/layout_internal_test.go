package orchestrator

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/davesavic/pgsavvy/pkg/theme/builtin"
)

// newFramedView returns a real *gocui.View with Frame=true and FrameColor
// initialised to ColorDefault — mirroring what gocui's SetView hands back
// in production. Tests use it to verify the FrameColor swap without
// instantiating a real *gocui.Gui / tcell screen.
func newFramedView(name string) *gocui.View {
	return gocui.NewView(name, 0, 0, 1, 1, gocui.OutputNormal)
}

// resetThemeToDefaultDark restores the default-dark theme so tests don't
// inherit overrides leaked by earlier test cases. Returns a teardown.
func resetThemeToDefaultDark(t *testing.T) {
	t.Helper()
	if err := theme.Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("theme.Apply default-dark: %v", err)
	}
}

// TestApplyFocusFrameColorsFocusedAndUnfocused:
// the focused rail's FrameColor matches theme.ActiveBorder; every other
// Frame=true non-popup rail gets theme.InactiveBorder.
func TestApplyFocusFrameColorsFocusedAndUnfocused(t *testing.T) {
	resetThemeToDefaultDark(t)
	active := frameAttr(theme.Current().ActiveBorder)
	inactive := frameAttr(theme.Current().InactiveBorder)

	rails := map[string]*gocui.View{
		string(types.SCHEMAS): newFramedView(string(types.SCHEMAS)),
		string(types.TABLES):  newFramedView(string(types.TABLES)),
	}
	focused := string(types.SCHEMAS)

	applyFocusFrameColors(rails, focused, active, inactive)

	if got := rails[focused].FrameColor; got != active {
		t.Errorf("focused (%s) FrameColor = %v, want active %v", focused, got, active)
	}
	for name, v := range rails {
		if name == focused {
			continue
		}
		if v.FrameColor != inactive {
			t.Errorf("unfocused (%s) FrameColor = %v, want inactive %v", name, v.FrameColor, inactive)
		}
	}
}

// TestApplyFocusFrameColorsSwapWithinOneCall:
// swapping focus and re-invoking the helper updates BOTH the previously
// focused and newly focused views in a single pass. Mirrors the
// once-per-Layout integration in RunLayout.
func TestApplyFocusFrameColorsSwapWithinOneCall(t *testing.T) {
	resetThemeToDefaultDark(t)
	active := frameAttr(theme.Current().ActiveBorder)
	inactive := frameAttr(theme.Current().InactiveBorder)

	a := newFramedView("a")
	b := newFramedView("b")
	rails := map[string]*gocui.View{"a": a, "b": b}

	applyFocusFrameColors(rails, "a", active, inactive)
	if a.FrameColor != active || b.FrameColor != inactive {
		t.Fatalf("initial: a=%v b=%v want a=%v b=%v", a.FrameColor, b.FrameColor, active, inactive)
	}

	// Swap focus from a to b: one helper invocation must flip BOTH.
	applyFocusFrameColors(rails, "b", active, inactive)
	if a.FrameColor != inactive {
		t.Errorf("post-swap a FrameColor = %v, want inactive %v", a.FrameColor, inactive)
	}
	if b.FrameColor != active {
		t.Errorf("post-swap b FrameColor = %v, want active %v", b.FrameColor, active)
	}
}

// TestApplyFocusFrameColorsSkipsFrameFalse:
// views with Frame=false (e.g. COMMAND_LINE — borderless 1-row strip)
// must NOT have their FrameColor written by the helper.
func TestApplyFocusFrameColorsSkipsFrameFalse(t *testing.T) {
	resetThemeToDefaultDark(t)
	active := frameAttr(theme.Current().ActiveBorder)
	inactive := frameAttr(theme.Current().InactiveBorder)

	borderless := newFramedView(string(types.COMMAND_LINE))
	borderless.Frame = false
	const sentinel = gocui.ColorMagenta
	borderless.FrameColor = sentinel

	rails := map[string]*gocui.View{string(types.COMMAND_LINE): borderless}
	applyFocusFrameColors(rails, string(types.COMMAND_LINE), active, inactive)

	if borderless.FrameColor != sentinel {
		t.Errorf("Frame=false view FrameColor = %v, want sentinel %v (helper must skip)", borderless.FrameColor, sentinel)
	}
}

// TestApplyFocusFrameColorsLeavesPopupsUntouched:
// callers are responsible for excluding popup-Kind views from the input
// map. The RunLayout integration only collects SIDE/EXTRAS contexts, so
// a popup-style view passed implicitly stays untouched — assert by
// constructing a popup view OUTSIDE the rails map and verifying its
// FrameColor is unchanged after the helper runs.
func TestApplyFocusFrameColorsLeavesPopupsUntouched(t *testing.T) {
	resetThemeToDefaultDark(t)
	active := frameAttr(theme.Current().ActiveBorder)
	inactive := frameAttr(theme.Current().InactiveBorder)

	popup := newFramedView(string(types.WHICH_KEY))
	const popupSentinel = gocui.ColorYellow
	popup.FrameColor = popupSentinel

	rails := map[string]*gocui.View{
		string(types.SCHEMAS): newFramedView(string(types.SCHEMAS)),
	}
	applyFocusFrameColors(rails, string(types.SCHEMAS), active, inactive)

	if popup.FrameColor != popupSentinel {
		t.Errorf("popup FrameColor = %v, want sentinel %v (helper must not touch views absent from rails map)", popup.FrameColor, popupSentinel)
	}
}

// TestApplyFocusFrameColorsNoFocusedMatch (negative
// case): when focusedName is empty or matches no rail (e.g. before any
// SetCurrentView has fired and Current() returns nil), every rail gets
// inactive — no panic, no stale active border lingering from a previous
// frame.
func TestApplyFocusFrameColorsNoFocusedMatch(t *testing.T) {
	resetThemeToDefaultDark(t)
	active := frameAttr(theme.Current().ActiveBorder)
	inactive := frameAttr(theme.Current().InactiveBorder)

	a := newFramedView("a")
	a.FrameColor = active // simulate a stale "previously focused" colour
	rails := map[string]*gocui.View{"a": a}

	applyFocusFrameColors(rails, "", active, inactive)

	if a.FrameColor != inactive {
		t.Errorf("with no focus match: FrameColor = %v, want inactive %v", a.FrameColor, inactive)
	}
}

// TestPromptBorderStyleExprPromptUsesWarnBorder:
// the free-form `<c-e>` expression prompt, keyed off its label, paints
// the WarnBorder colour.
func TestPromptBorderStyleExprPromptUsesWarnBorder(t *testing.T) {
	resetThemeToDefaultDark(t)
	if got := promptBorderStyle(helpers.WarnExprPromptLabel); got != theme.Current().WarnBorder {
		t.Errorf("expr-prompt border = %v, want WarnBorder %v", got, theme.Current().WarnBorder)
	}
}

// TestPromptBorderStyleOtherPromptUsesActiveBorder: every other prompt
// label keeps ActiveBorder.
func TestPromptBorderStyleOtherPromptUsesActiveBorder(t *testing.T) {
	resetThemeToDefaultDark(t)
	if got := promptBorderStyle("Enter database name: "); got != theme.Current().ActiveBorder {
		t.Errorf("non-expr prompt border = %v, want ActiveBorder %v", got, theme.Current().ActiveBorder)
	}
}

// TestPromptBorderStyleWarnUnsetFallsBackToDefault (invariant / edge
// path): a theme that leaves WarnBorder unset must not
// crash — promptBorderStyle returns the nil style and frameAttr maps it
// to ColorDefault.
func TestPromptBorderStyleWarnUnsetFallsBackToDefault(t *testing.T) {
	cfg := builtin.DefaultDark()
	cfg.WarnBorder = ""
	if err := theme.Apply(cfg); err != nil {
		t.Fatalf("theme.Apply WarnBorder-unset: %v", err)
	}
	t.Cleanup(func() { resetThemeToDefaultDark(t) })

	if got := frameAttr(promptBorderStyle(helpers.WarnExprPromptLabel)); got != gocui.ColorDefault {
		t.Errorf("expr-prompt border with WarnBorder unset = %v, want ColorDefault %v", got, gocui.ColorDefault)
	}
}

// TestResolveFocusedRailName: the focus-frame swap follows
// the live active result tab instead of the stale focus-stack context.
// When focus is pushed to the result pane the stack captures one
// result_tab_<slot> context; gt/gT change the active tab without updating
// the stack. resolveFocusedRailName must redirect the highlight onto the
// active tab view whenever the stack points at any result tab, so the
// yellow border lands on the visible tab — not only the last one whose
// context was pushed (the original bug).
func TestResolveFocusedRailName(t *testing.T) {
	cases := []struct {
		name          string
		stackViewName string
		activeTabView string
		want          string
	}{
		{
			name:          "non-result rail returns stack name unchanged",
			stackViewName: string(types.SCHEMAS),
			activeTabView: string(types.ResultTabKey(2)),
			want:          string(types.SCHEMAS),
		},
		{
			name:          "stale result tab on stack follows live active tab",
			stackViewName: string(types.ResultTabKey(0)),
			activeTabView: string(types.ResultTabKey(2)),
			want:          string(types.ResultTabKey(2)),
		},
		{
			name:          "stack already matches active tab is a no-op",
			stackViewName: string(types.ResultTabKey(1)),
			activeTabView: string(types.ResultTabKey(1)),
			want:          string(types.ResultTabKey(1)),
		},
		{
			name:          "result tab on stack but no live active tab keeps stack name",
			stackViewName: string(types.ResultTabKey(0)),
			activeTabView: "",
			want:          string(types.ResultTabKey(0)),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveFocusedRailName(c.stackViewName, c.activeTabView); got != c.want {
				t.Errorf("resolveFocusedRailName(%q, %q) = %q, want %q",
					c.stackViewName, c.activeTabView, got, c.want)
			}
		})
	}
}

// TestFrameAttrFallbacks: nil *theme.Style and empty Fg collapse to
// gocui.ColorDefault so the helper never injects a garbage Attribute
// when the theme has not yet been Apply'd (unlikely — theme has an
// init() — but cheap to lock in).
func TestFrameAttrFallbacks(t *testing.T) {
	if got := frameAttr(nil); got != gocui.ColorDefault {
		t.Errorf("frameAttr(nil) = %v, want ColorDefault", got)
	}
	if got := frameAttr(&theme.Style{Fg: ""}); got != gocui.ColorDefault {
		t.Errorf("frameAttr(empty Fg) = %v, want ColorDefault", got)
	}
	// cyan now resolves through ColorAttr's palette index (Get256Color(6)),
	// NOT the old gocui.GetColor("cyan") which collapsed to ColorDefault
	// because tcell v3 names that colour "aqua". This locks in the fix.
	if got := frameAttr(&theme.Style{Fg: "cyan"}); got != gocui.Get256Color(6) {
		t.Errorf("frameAttr(cyan) = %v, want Get256Color(6) %v", got, gocui.Get256Color(6))
	}
}

// TestColorAttr pins the border colour vocabulary to the inline path's: the 16
// named colours, "colorN" 256 indices, and #rgb/#rrggbb hex. Arbitrary W3C
// names and malformed/out-of-range tokens collapse to gocui.ColorDefault.
func TestColorAttr(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  gocui.Attribute
	}{
		{"empty", "", gocui.ColorDefault},
		{"hex", "#ff8800", gocui.NewRGBColor(255, 136, 0)},
		{"index256", "color42", gocui.Get256Color(42)},
		// bright names widen the border vocabulary: these were previously
		// ColorDefault via gocui.GetColor — intentional widening.
		{"brightblack", "brightblack", gocui.Get256Color(8)},
		{"brightwhite", "brightwhite", gocui.Get256Color(15)},
		{"brightred", "brightred", gocui.Get256Color(9)},
		{"gray", "gray", gocui.Get256Color(8)},
		{"grey", "grey", gocui.Get256Color(8)},
		{"red", "red", gocui.Get256Color(1)},
		// cyan FIX: previously ColorDefault via GetColor (tcell calls it "aqua").
		{"cyan", "cyan", gocui.Get256Color(6)},
		// arbitrary W3C name dropped — no longer a gocui.GetColor passthrough.
		{"navy", "navy", gocui.ColorDefault},
		{"notacolor", "notacolor", gocui.ColorDefault},
		// out of range, inherited from ClassifyColor.
		{"color256", "color256", gocui.ColorDefault},
		{"malformed hex", "#gggggg", gocui.ColorDefault},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ColorAttr(c.token); got != c.want {
				t.Errorf("ColorAttr(%q) = %v, want %v", c.token, got, c.want)
			}
		})
	}

	// gray byte-identity: gocui.GetColor("gray") historically resolved to the
	// same Attribute as Get256Color(8), so routing gray through ColorAttr
	// leaves default_dark's InactiveBorder byte-for-byte unchanged.
	if got, want := ColorAttr("gray"), gocui.GetColor("gray"); got != want {
		t.Errorf("ColorAttr(gray) = %v, want GetColor(gray) %v (border colour must be unchanged)", got, want)
	}
}
