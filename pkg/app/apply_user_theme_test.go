package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/davesavic/pgsavvy/pkg/theme/builtin"
)

// restoreDefaultThemeAfter resets the process-global theme to DefaultDark when
// the test ends, so applying a user theme here does not leak into other tests.
func restoreDefaultThemeAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = theme.Apply(builtin.DefaultDark()) })
}

func TestApplyUserTheme_ConfiguredColorRendersLive(t *testing.T) {
	restoreDefaultThemeAfter(t)

	cfg := config.GetDefaultConfig() // full baseline, like the real loader
	cfg.Theme.Keyword = "color82"

	var buf bytes.Buffer
	applyUserTheme(cfg, &buf)

	if got := theme.Current().Keyword.Fg; got != "color82" {
		t.Errorf("Keyword.Fg = %q, want %q", got, "color82")
	}
	// An unset field keeps its DefaultDark default.
	if got := theme.Current().NullValue.Fg; got != "red" {
		t.Errorf("NullValue.Fg = %q, want default %q", got, "red")
	}
	if buf.Len() != 0 {
		t.Errorf("valid theme wrote to stderr: %q", buf.String())
	}
}

func TestApplyUserTheme_AllDefaultWritesNothing(t *testing.T) {
	restoreDefaultThemeAfter(t)

	cfg := config.GetDefaultConfig() // Theme == DefaultDark

	var buf bytes.Buffer
	applyUserTheme(cfg, &buf)

	if buf.Len() != 0 {
		t.Errorf("all-default theme wrote to writer: %q", buf.String())
	}
	// Current() equals the DefaultDark snapshot for a representative field.
	if got := theme.Current().Keyword.Fg; got != "blue" {
		t.Errorf("Keyword.Fg = %q, want DefaultDark %q", got, "blue")
	}
}

func TestApplyUserTheme_UnknownTokenWarnsAndStillStarts(t *testing.T) {
	restoreDefaultThemeAfter(t)

	cfg := config.GetDefaultConfig()
	cfg.Theme.Keyword = "notacolor"

	var buf bytes.Buffer
	applyUserTheme(cfg, &buf)

	out := buf.String()
	if !strings.HasPrefix(out, "config: warning: ") {
		t.Errorf("expected a `config: warning:` line, got %q", out)
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("expected exactly one warning line, got %d: %q", n, out)
	}
	if !strings.Contains(out, "keyword") || !strings.Contains(out, "notacolor") {
		t.Errorf("warning %q must name the field and the token", out)
	}
	// The app still "starts": the token applies (renders untinted downstream).
	if got := theme.Current().Keyword.Fg; got != "notacolor" {
		t.Errorf("Keyword.Fg = %q, want %q", got, "notacolor")
	}
}

// TestApplyUserTheme_NoColorStillSuppressesInline guards that wiring a user
// theme does not disturb the NO_COLOR inline-suppression gate: applying a
// colourful theme leaves theme.IsMonochrome() reporting true.
func TestApplyUserTheme_NoColorStillSuppressesInline(t *testing.T) {
	restoreDefaultThemeAfter(t)
	restore := theme.SetMonochromeForTest(true)
	defer restore()

	cfg := config.GetDefaultConfig()
	cfg.Theme.Keyword = "color82"

	var buf bytes.Buffer
	applyUserTheme(cfg, &buf)

	if !theme.IsMonochrome() {
		t.Error("applying a user theme cleared the NO_COLOR monochrome gate")
	}
}
