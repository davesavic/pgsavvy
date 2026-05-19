package presentation

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
	"github.com/davesavic/dbsavvy/pkg/theme/builtin"
)

func restoreDefaultTheme(t *testing.T) {
	t.Helper()
	if err := theme.Apply(builtin.DefaultDark()); err != nil {
		t.Fatalf("restore default theme: %v", err)
	}
}

func TestHeaderTextFor_NilReturnsEmpty(t *testing.T) {
	if got := HeaderTextFor(nil); got != "" {
		t.Fatalf("HeaderTextFor(nil) = %q, want %q", got, "")
	}
}

func TestHeaderTextFor_BothPresent(t *testing.T) {
	conn := &models.Connection{Icon: "⚠", Label: "prod"}
	if got := HeaderTextFor(conn); got != "⚠ prod" {
		t.Fatalf("HeaderTextFor = %q, want %q", got, "⚠ prod")
	}
}

func TestHeaderTextFor_OnlyLabel(t *testing.T) {
	conn := &models.Connection{Label: "prod"}
	if got := HeaderTextFor(conn); got != "prod" {
		t.Fatalf("HeaderTextFor = %q, want %q", got, "prod")
	}
}

func TestHeaderTextFor_OnlyIcon(t *testing.T) {
	conn := &models.Connection{Icon: "⚠"}
	if got := HeaderTextFor(conn); got != "⚠" {
		t.Fatalf("HeaderTextFor = %q, want %q", got, "⚠")
	}
}

func TestHeaderTextFor_BothEmpty(t *testing.T) {
	conn := &models.Connection{}
	if got := HeaderTextFor(conn); got != "" {
		t.Fatalf("HeaderTextFor = %q, want %q", got, "")
	}
}

func TestBorderStyleFor_NilFallsBackToPopupBorder(t *testing.T) {
	restoreDefaultTheme(t)
	got := BorderStyleFor(nil)
	want := theme.Current().PopupBorder
	if got.Fg != want.Fg {
		t.Fatalf("BorderStyleFor(nil).Fg = %q, want %q", got.Fg, want.Fg)
	}
}

func TestBorderStyleFor_EmptyColorFallsBackToPopupBorder(t *testing.T) {
	restoreDefaultTheme(t)
	conn := &models.Connection{Name: "x"}
	got := BorderStyleFor(conn)
	want := theme.Current().PopupBorder
	if got.Fg != want.Fg {
		t.Fatalf("BorderStyleFor.Fg = %q, want %q", got.Fg, want.Fg)
	}
}

func TestBorderStyleFor_ConnColorWins(t *testing.T) {
	restoreDefaultTheme(t)
	conn := &models.Connection{Color: "#ff4d4d"}
	got := BorderStyleFor(conn)
	if got.Fg != "#ff4d4d" {
		t.Fatalf("BorderStyleFor.Fg = %q, want %q", got.Fg, "#ff4d4d")
	}
}

func TestBorderStyleFor_ThemeHotReloadFlowsThrough(t *testing.T) {
	// Apply theme A.
	cfgA := builtin.DefaultDark()
	cfgA.PopupBorder = "magenta"
	if err := theme.Apply(cfgA); err != nil {
		t.Fatalf("Apply A: %v", err)
	}
	gotA := BorderStyleFor(nil)
	if gotA.Fg != "magenta" {
		t.Fatalf("after Apply A, BorderStyleFor(nil).Fg = %q, want %q", gotA.Fg, "magenta")
	}

	// Apply theme B — different PopupBorder.
	cfgB := &config.ThemeConfig{PopupBorder: "yellow"}
	if err := theme.Apply(cfgB); err != nil {
		t.Fatalf("Apply B: %v", err)
	}
	gotB := BorderStyleFor(nil)
	if gotB.Fg != "yellow" {
		t.Fatalf("after Apply B, BorderStyleFor(nil).Fg = %q, want %q (helper must read theme.Current() at call time)", gotB.Fg, "yellow")
	}

	// Restore so other tests in the suite are not surprised.
	restoreDefaultTheme(t)
}

func TestResolveColor(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"#abc", "#abc"},
		{"#aabbcc", "#aabbcc"},
		{"red", "red"},
		{"cyan", "cyan"},
		{"danger", "danger"}, // pass-through; token resolution deferred to E12
	}
	for _, c := range cases {
		if got := ResolveColor(c.in); got != c.want {
			t.Fatalf("ResolveColor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewPresentationHook_NilSafe(t *testing.T) {
	restoreDefaultTheme(t)
	h := NewPresentationHook()
	style, header := h(nil)
	if header != "" {
		t.Fatalf("header = %q, want %q for nil conn", header, "")
	}
	if style.Fg == "" {
		t.Fatalf("style.Fg is empty; expected theme PopupBorder fallback")
	}
}

func TestNewPresentationHook_PicksUpConnColor(t *testing.T) {
	h := NewPresentationHook()
	conn := &models.Connection{Color: "#ff4d4d", Icon: "⚠", Label: "PROD"}
	style, header := h(conn)
	if style.Fg != "#ff4d4d" {
		t.Fatalf("style.Fg = %q, want %q", style.Fg, "#ff4d4d")
	}
	if header != "⚠ PROD" {
		t.Fatalf("header = %q, want %q", header, "⚠ PROD")
	}
}

func TestNewPerRowDecorationHook(t *testing.T) {
	h := NewPerRowDecorationHook()

	icon, label, color := h(nil)
	if icon != "" || label != "" || color != "" {
		t.Fatalf("nil conn -> (%q,%q,%q), want all empty", icon, label, color)
	}

	// The rail label is Profile.Name (not Profile.Label): the picker's
	// purpose is to disambiguate profiles by their stable handle. A
	// non-empty Profile.Label is ignored here; status-bar / title-bar
	// rendering owns the Label-via-HeaderTextFor path. Bug dbsavvy-2ox.
	conn := &models.Connection{Icon: "★", Name: "local-pg", Label: "lbl", Color: "#abc"}
	icon, label, color = h(conn)
	if icon != "★" || label != "local-pg" || color != "#abc" {
		t.Fatalf("got (%q,%q,%q), want (★,local-pg,#abc)", icon, label, color)
	}
}

// TestNewPerRowDecorationHook_NameUsedWhenLabelMatchesHost guards the
// dbsavvy-2ox regression: a profile saved with name='local-pg' and a
// DSN-derived label='localhost' (the host portion of the DSN — typical
// of an auto-populated or user-set label) must render as 'local-pg' in
// the CONNECTIONS rail. Two profiles sharing a host but differing in
// name remain visually distinguishable.
func TestNewPerRowDecorationHook_NameUsedWhenLabelMatchesHost(t *testing.T) {
	h := NewPerRowDecorationHook()
	conn := &models.Connection{
		Name:  "local-pg",
		DSN:   "postgres://dbsavvy:dbsavvy@localhost:5432/dbsavvy_test",
		Label: "localhost",
	}
	_, label, _ := h(conn)
	if label != "local-pg" {
		t.Fatalf("rail label = %q, want %q (must use Profile.Name, not Profile.Label)", label, "local-pg")
	}
}

func TestNewLimitText(t *testing.T) {
	if got := NewLimitText(nil)(); got != "" {
		t.Fatalf("NewLimitText(nil)() = %q, want empty", got)
	}
	tr := i18n.EnglishTranslationSet()
	if got := NewLimitText(tr)(); got != tr.TerminalTooSmall {
		t.Fatalf("NewLimitText = %q, want %q", got, tr.TerminalTooSmall)
	}
}
