package config

import (
	"strings"
	"testing"
)

// alwaysTrue is a predicate that accepts any string. Used in tests where
// the binding's action/scope should be considered "known".
func alwaysTrue(string) bool { return true }

// allowSet returns a predicate that returns true only for strings in s.
func allowSet(s ...string) func(string) bool {
	m := make(map[string]struct{}, len(s))
	for _, k := range s {
		m[k] = struct{}{}
	}
	return func(x string) bool { _, ok := m[x]; return ok }
}

// fullDeps is a permissive ValidationDeps that treats every action and
// scope as valid. Useful for tests focused on syntactic checks.
func fullDeps() ValidationDeps {
	return ValidationDeps{ActionExists: alwaysTrue, ScopeExists: alwaysTrue}
}

func TestValidateUserConfig_DefaultsValid(t *testing.T) {
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit"),
		ScopeExists:  allowSet("global"),
	}
	warns, errs := ValidateUserConfig(GetDefaultConfig(), deps)
	if len(errs) != 0 {
		t.Errorf("defaults should validate; got errors %v", errs)
	}
	// Default config has app.quit bound but not help.cheatsheet → one warning.
	if !containsSubstr(warns, "help.cheatsheet") {
		t.Errorf("expected help.cheatsheet warning, got %v", warns)
	}
}

func TestValidateUserConfig_InvalidLabel(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<bogus", Action: "x"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected at least one error for malformed key")
	}
}

func TestValidateUserConfig_NilConfig(t *testing.T) {
	_, errs := ValidateUserConfig(nil, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected error for nil config")
	}
}

func TestValidateUserConfig_MultipleErrors(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<bogus", Action: "x"},
		{Mode: "n", Scope: "global", Key: "j", Action: "y"},
		{Mode: "n", Scope: "global", Key: "<f13>", Action: "z"},
		// app.quit must be bound or its absence adds a (now-hard) error and
		// throws off the count; this test targets key-parse errors only.
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) != 2 {
		t.Errorf("expected exactly 2 errors (<bogus + <f13>), got %d: %v", len(errs), errs)
	}
}

func TestValidateUserConfig_ValidOverride(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
		{Mode: "n", Scope: "global", Key: "?", Action: "help.cheatsheet"},
	}
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit", "help.cheatsheet"),
		ScopeExists:  allowSet("global"),
	}
	warns, errs := ValidateUserConfig(cfg, deps)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
	if len(warns) != 0 {
		t.Errorf("expected no warnings, got %v", warns)
	}
}

func TestValidateUserConfig_OrphanAction(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{
			Mode: "n", Scope: "global", Key: "q", Action: "no.such.action",
			OriginFile: "/etc/pgsavvy.yml", OriginLine: 42,
		},
		{Mode: "n", Scope: "global", Key: "?", Action: "help.cheatsheet"},
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit", "help.cheatsheet"),
		ScopeExists:  allowSet("global"),
	}
	_, errs := ValidateUserConfig(cfg, deps)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error, got %d: %v", len(errs), errs)
	}
	msg := errs[0].Error()
	if !strings.Contains(msg, "no.such.action") {
		t.Errorf("error should mention action id, got %q", msg)
	}
	if !strings.Contains(msg, "/etc/pgsavvy.yml:42") {
		t.Errorf("error should mention OriginFile:OriginLine, got %q", msg)
	}
}

func TestValidateUserConfig_EmptyKey(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "", Action: "app.quit"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected error for empty key")
	}
}

func TestValidateUserConfig_ActionAndCommandBothSet(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "app.quit", Command: ":quit"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected error when both action and command are set")
	}
}

func TestValidateUserConfig_NeitherActionNorCommand(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected error when neither action nor command set")
	}
}

func TestValidateUserConfig_UnknownModeLetter(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "z", Scope: "global", Key: "q", Action: "app.quit"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected error for unknown mode letter")
	}
	if !strings.Contains(errs[0].Error(), "z") {
		t.Errorf("error should mention the offending letter, got %q", errs[0].Error())
	}
}

func TestValidateUserConfig_StrayAngleBracket(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "a<b", Action: "app.quit"},
	}
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if len(errs) == 0 {
		t.Fatal("expected error for stray angle bracket")
	}
}

func TestValidateUserConfig_ScopeAll(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "all", Key: "q", Action: "app.quit"},
	}
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit"),
		ScopeExists:  allowSet("all"),
	}
	_, errs := ValidateUserConfig(cfg, deps)
	if len(errs) != 0 {
		t.Errorf("scope=all should be accepted when ScopeExists allows it, got %v", errs)
	}
}

func TestValidateUserConfig_UnknownScopeRejected(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "WeirdScope", Key: "q", Action: "app.quit"},
	}
	deps := ValidationDeps{
		ActionExists: alwaysTrue,
		ScopeExists:  allowSet("global"),
	}
	_, errs := ValidateUserConfig(cfg, deps)
	if len(errs) == 0 {
		t.Fatal("expected error for unknown scope")
	}
}

func TestValidateUserConfig_DuplicateBinding(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "app.quit"},
		{Mode: "n", Scope: "global", Key: "q", Action: "help.cheatsheet"},
	}
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit", "help.cheatsheet"),
		ScopeExists:  allowSet("global"),
	}
	_, errs := ValidateUserConfig(cfg, deps)
	if !containsErrSubstr(errs, "duplicate binding") {
		t.Fatalf("expected duplicate binding error, got %v", errs)
	}
	// Exactly one duplicate error.
	dupCount := 0
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate binding") {
			dupCount++
		}
	}
	if dupCount != 1 {
		t.Errorf("expected exactly 1 duplicate error, got %d: %v", dupCount, errs)
	}
}

func TestValidateUserConfig_NopAllowsOverlap(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "<nop>"},
		{Mode: "n", Scope: "global", Key: "q", Action: "app.quit"},
	}
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit"),
		ScopeExists:  allowSet("global"),
	}
	_, errs := ValidateUserConfig(cfg, deps)
	if containsErrSubstr(errs, "duplicate binding") {
		t.Errorf("nop + real on same key should NOT be a duplicate, got %v", errs)
	}
}

func TestValidateUserConfig_MissingCheatsheetWarning(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit"),
		ScopeExists:  allowSet("global"),
	}
	warns, errs := ValidateUserConfig(cfg, deps)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !containsSubstr(warns, "help.cheatsheet") {
		t.Errorf("expected help.cheatsheet warning, got %v", warns)
	}
}

// TestQuitBindingRequired covers that a merged config with
// no app.quit binding is a hard ERROR (not a warning), while a config that
// does bind app.quit validates cleanly. help.cheatsheet must remain a
// warning either way (the asymmetry is intentional).
func TestQuitBindingRequired(t *testing.T) {
	deps := ValidationDeps{
		ActionExists: allowSet("app.quit", "help.cheatsheet"),
		ScopeExists:  allowSet("global"),
	}

	t.Run("missing app.quit is an error, not a warning", func(t *testing.T) {
		cfg := GetDefaultConfig()
		cfg.Keybindings = []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "?", Action: "help.cheatsheet"},
		}
		warns, errs := ValidateUserConfig(cfg, deps)
		if !containsErrSubstr(errs, "app.quit") {
			t.Errorf("expected an error naming app.quit, got errors %v", errs)
		}
		if containsSubstr(warns, "app.quit") {
			t.Errorf("app.quit must be an error, not a warning; warns=%v", warns)
		}
		// help.cheatsheet stays a warning here only when it is itself
		// missing; it is bound in this config, so assert it is NOT promoted
		// to an error.
		if containsErrSubstr(errs, "help.cheatsheet") {
			t.Errorf("help.cheatsheet must never be promoted to an error; errs=%v", errs)
		}
	})

	t.Run("help.cheatsheet remains a warning, never an error", func(t *testing.T) {
		// Bind app.quit but not help.cheatsheet: the only advisory should be
		// the help.cheatsheet warning, and there must be no errors.
		cfg := GetDefaultConfig()
		cfg.Keybindings = []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
		}
		warns, errs := ValidateUserConfig(cfg, deps)
		if len(errs) != 0 {
			t.Errorf("expected no errors when app.quit is bound, got %v", errs)
		}
		if !containsSubstr(warns, "help.cheatsheet") {
			t.Errorf("expected help.cheatsheet warning, got %v", warns)
		}
	})

	t.Run("config that binds app.quit validates cleanly", func(t *testing.T) {
		cfg := GetDefaultConfig()
		cfg.Keybindings = []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
			{Mode: "n", Scope: "global", Key: "?", Action: "help.cheatsheet"},
		}
		_, errs := ValidateUserConfig(cfg, deps)
		if len(errs) != 0 {
			t.Errorf("expected clean validation, got errors %v", errs)
		}
	})
}

func TestValidateUserConfig_LeaderDigitRejected(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Leader = "0"
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "leader") {
		t.Fatalf("expected leader-digit error, got %v", errs)
	}
}

func TestValidateUserConfig_NilDepsTreatedAsFalse(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "app.quit"},
	}
	// Zero-value deps → all predicates effectively return false → action
	// + scope both rejected.
	_, errs := ValidateUserConfig(cfg, ValidationDeps{})
	if len(errs) < 2 {
		t.Errorf("expected at least 2 errors (unknown action + unknown scope), got %d: %v", len(errs), errs)
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func containsErrSubstr(errs []error, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), sub) {
			return true
		}
	}
	return false
}

// TestValidateUserConfig_UIPaginationDefaults pins that the shipped
// defaults (200/50/25/1_000_000) all pass validation.
func TestValidateUserConfig_UIPaginationDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	_, errs := ValidateUserConfig(cfg, fullDeps())
	for _, e := range errs {
		if strings.Contains(e.Error(), "result_page_size") ||
			strings.Contains(e.Error(), "result_prefetch_rows") ||
			strings.Contains(e.Error(), "prefetch_threshold") ||
			strings.Contains(e.Error(), "read_to_end_warn_threshold") {
			t.Errorf("default UI pagination knobs should validate; got %v", e)
		}
	}
}

func TestValidateUserConfig_UIPagination_ResultPageSizeZero(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.ResultPageSize = 0
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "result_page_size") {
		t.Errorf("expected result_page_size error, got %v", errs)
	}
}

func TestValidateUserConfig_UIPagination_ResultPrefetchRowsNegative(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.ResultPrefetchRows = -1
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "result_prefetch_rows") {
		t.Errorf("expected result_prefetch_rows error, got %v", errs)
	}
}

func TestValidateUserConfig_UIPagination_PrefetchThresholdNegative(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.PrefetchThreshold = -1
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "prefetch_threshold") {
		t.Errorf("expected prefetch_threshold error, got %v", errs)
	}
}

func TestValidateUserConfig_UIPagination_PrefetchThresholdZeroOK(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.PrefetchThreshold = 0
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if containsErrSubstr(errs, "prefetch_threshold") {
		t.Errorf("prefetch_threshold=0 should be valid, got %v", errs)
	}
}

func TestValidateUserConfig_UIPagination_ReadToEndWarnThresholdZero(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.ReadToEndWarnThreshold = 0
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "read_to_end_warn_threshold") {
		t.Errorf("expected read_to_end_warn_threshold error, got %v", errs)
	}
}

func TestValidateUserConfig_MouseDoubleClickMs_OutOfRange(t *testing.T) {
	for _, v := range []int{0, 99, 2001, -1} {
		cfg := GetDefaultConfig()
		cfg.UI.Mouse.DoubleClickMs = v
		_, errs := ValidateUserConfig(cfg, fullDeps())
		if !containsErrSubstr(errs, "double_click_ms") {
			t.Errorf("DoubleClickMs=%d: expected double_click_ms error, got %v", v, errs)
		}
	}
}

func TestValidateUserConfig_MouseDoubleClickMs_DefaultClean(t *testing.T) {
	cfg := GetDefaultConfig()
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if containsErrSubstr(errs, "double_click_ms") {
		t.Errorf("default DoubleClickMs should validate clean; got %v", errs)
	}
}

// TestValidateUserConfig_ExportDefaults pins that the shipped export
// defaults validate clean.
func TestValidateUserConfig_ExportDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if containsErrSubstr(errs, "ui.export") {
		t.Errorf("default ui.export knobs should validate; got %v", errs)
	}
}

// TestValidateUserConfig_Export_BufferedRowWarnThresholdZero rejects
// non-positive warn thresholds.
func TestValidateUserConfig_Export_BufferedRowWarnThresholdZero(t *testing.T) {
	for _, v := range []int64{0, -1} {
		cfg := GetDefaultConfig()
		cfg.UI.Export.BufferedRowWarnThreshold = v
		_, errs := ValidateUserConfig(cfg, fullDeps())
		if !containsErrSubstr(errs, "buffered_row_warn_threshold") {
			t.Errorf("BufferedRowWarnThreshold=%d: expected error, got %v", v, errs)
		}
	}
}

// TestValidateUserConfig_Export_ClipboardMaxBytesZero rejects
// non-positive clipboard caps.
func TestValidateUserConfig_Export_ClipboardMaxBytesZero(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.Export.ClipboardMaxBytes = 0
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "clipboard_max_bytes") {
		t.Errorf("expected clipboard_max_bytes error, got %v", errs)
	}
}

// TestValidateUserConfig_Export_ClipboardMaxBytesAboveGiB rejects caps
// larger than 1 GiB.
func TestValidateUserConfig_Export_ClipboardMaxBytesAboveGiB(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.UI.Export.ClipboardMaxBytes = (1 << 30) + 1
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "clipboard_max_bytes") {
		t.Errorf("expected clipboard_max_bytes error, got %v", errs)
	}
}
