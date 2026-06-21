package theme

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/theme/builtin"
)

// restoreDefaultThemeAfter restores the process-global theme to DefaultDark when
// the test finishes, so a test that calls ApplyUserConfig/Apply does not leak
// its theme into later tests in the package.
func restoreDefaultThemeAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = Apply(builtin.DefaultDark()) })
}

func TestApplyUserConfig_AppliesAndClassifiesVocabulary(t *testing.T) {
	restoreDefaultThemeAfter(t)

	cfg := builtin.DefaultDark() // full baseline, mirrors the real overlay
	cfg.KeywordFg = "color82"
	cfg.StringFg = "#ff8800"
	cfg.NumericFg = "brightmagenta"
	cfg.OperatorFg = "color82"

	warnings := ApplyUserConfig(cfg)

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for valid vocabulary, got %v", warnings)
	}
	if got := Current().KeywordFg.Fg; got != "color82" {
		t.Errorf("KeywordFg.Fg = %q, want %q", got, "color82")
	}
	if got := Current().StringFg.Fg; got != "#ff8800" {
		t.Errorf("StringFg.Fg = %q, want %q", got, "#ff8800")
	}
	if got := Current().NumericFg.Fg; got != "brightmagenta" {
		t.Errorf("NumericFg.Fg = %q, want %q", got, "brightmagenta")
	}
	// An unset field keeps its DefaultDark default.
	if got := Current().NullValueFg.Fg; got != "red" {
		t.Errorf("NullValueFg.Fg = %q, want default %q", got, "red")
	}
}

func TestApplyUserConfig_UnknownFgWarnsButStillApplies(t *testing.T) {
	restoreDefaultThemeAfter(t)

	cfg := builtin.DefaultDark()
	cfg.KeywordFg = "notacolor"

	warnings := ApplyUserConfig(cfg)

	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", len(warnings), warnings)
	}
	if w := warnings[0]; !strings.Contains(w, "keyword_fg") || !strings.Contains(w, "notacolor") {
		t.Errorf("warning %q must name the field (keyword_fg) and the token (notacolor)", w)
	}
	// The token still applies — stored verbatim, renders untinted downstream.
	if got := Current().KeywordFg.Fg; got != "notacolor" {
		t.Errorf("KeywordFg.Fg = %q, want %q (token applies despite warning)", got, "notacolor")
	}
}

// TestApplyUserConfig_NoFalsePositiveOnStrayBareword is the shared-tokenizer
// drift guard: the validator must reuse parseStyle's tokenization. "blue
// notacolor" renders Fg=blue and ignores the stray 2nd bareword, so it must
// produce zero warnings — and parseStyle must agree.
func TestApplyUserConfig_NoFalsePositiveOnStrayBareword(t *testing.T) {
	restoreDefaultThemeAfter(t)

	// Renderer's view of the same input.
	if got := parseStyle("blue notacolor").Fg; got != "blue" {
		t.Fatalf("parseStyle(%q).Fg = %q, want %q (test premise)", "blue notacolor", got, "blue")
	}

	cfg := builtin.DefaultDark()
	cfg.KeywordFg = "blue notacolor"

	if warnings := ApplyUserConfig(cfg); len(warnings) != 0 {
		t.Errorf("stray bareword must not warn (validator must match parseStyle), got %v", warnings)
	}
}

func TestApplyUserConfig_CompoundBackground(t *testing.T) {
	restoreDefaultThemeAfter(t)

	t.Run("valid compound and bg-only warn nothing", func(t *testing.T) {
		cfg := builtin.DefaultDark()
		cfg.CurSearch = "black on yellow"
		cfg.DirtyCellBg = "on #5a4410"
		if warnings := ApplyUserConfig(cfg); len(warnings) != 0 {
			t.Errorf("valid compound/bg-only values must not warn, got %v", warnings)
		}
	})

	t.Run("unknown bg warns exactly once naming the field", func(t *testing.T) {
		cfg := builtin.DefaultDark()
		cfg.CurSearch = "white on notacolor"
		warnings := ApplyUserConfig(cfg)
		if len(warnings) != 1 {
			t.Fatalf("expected exactly 1 warning for unknown bg, got %d: %v", len(warnings), warnings)
		}
		if w := warnings[0]; !strings.Contains(w, "cur_search") || !strings.Contains(w, "notacolor") {
			t.Errorf("warning %q must name the field (cur_search) and the bg token (notacolor)", w)
		}
	})
}

func TestApplyUserConfig_AttributesAndCaseAreNotClassified(t *testing.T) {
	restoreDefaultThemeAfter(t)

	cfg := builtin.DefaultDark()
	cfg.StringFg = "bold #ff8800"
	cfg.KeywordFg = "BLUE BOLD" // mixed case; bold is an attribute, BLUE a valid named color

	if warnings := ApplyUserConfig(cfg); len(warnings) != 0 {
		t.Errorf("attributes and mixed-case named colors must not warn, got %v", warnings)
	}
}

func TestApplyUserConfig_AllEmptyAppliesCleanly(t *testing.T) {
	restoreDefaultThemeAfter(t)

	var cfg config.ThemeConfig // every field ""

	if warnings := ApplyUserConfig(&cfg); len(warnings) != 0 {
		t.Errorf("an all-empty theme config must produce no warnings, got %v", warnings)
	}
}
