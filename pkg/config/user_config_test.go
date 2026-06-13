package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestGetDefaultConfigUIMouseEnabled pins the default per dbsavvy-zro AC.
// Mouse is opt-out: the bootstrap-time mouse-binding registration block
// in pkg/gui/controllers/* skips entirely when this flag is false, so
// the default MUST stay true to preserve documented UX.
func TestGetDefaultConfigUIMouseEnabled(t *testing.T) {
	if !GetDefaultConfig().UI.Mouse.Enabled {
		t.Fatal("GetDefaultConfig().UI.Mouse.Enabled = false; want true")
	}
}

// TestGetDefaultConfigLeaderDefaults pins the dbsavvy-dlp.3 defaults per
// the review-plan D19 amendment.
func TestGetDefaultConfigLeaderDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.Leader != " " {
		t.Errorf("Leader = %q, want %q (space per D19)", cfg.Leader, " ")
	}
	if cfg.LocalLeader != "," {
		t.Errorf("LocalLeader = %q, want %q", cfg.LocalLeader, ",")
	}
}

func TestGetDefaultConfigTimeoutDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.TimeoutLen != 1*time.Second {
		t.Errorf("TimeoutLen = %v, want 1s", cfg.TimeoutLen)
	}
	if cfg.TtimeoutLen != 50*time.Millisecond {
		t.Errorf("TtimeoutLen = %v, want 50ms", cfg.TtimeoutLen)
	}
	if cfg.WhichKeyDelay != 300*time.Millisecond {
		t.Errorf("WhichKeyDelay = %v, want 300ms", cfg.WhichKeyDelay)
	}
}

// TestGetDefaultConfigUIPaginationDefaults pins the dbsavvy-uv0.3
// pagination knob defaults: 200/50/25/1_000_000.
func TestGetDefaultConfigUIPaginationDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.UI.ResultPageSize != 200 {
		t.Errorf("UI.ResultPageSize = %d, want 200", cfg.UI.ResultPageSize)
	}
	if cfg.UI.ResultPrefetchRows != 50 {
		t.Errorf("UI.ResultPrefetchRows = %d, want 50", cfg.UI.ResultPrefetchRows)
	}
	if cfg.UI.PrefetchThreshold != 25 {
		t.Errorf("UI.PrefetchThreshold = %d, want 25", cfg.UI.PrefetchThreshold)
	}
	if cfg.UI.ReadToEndWarnThreshold != 1_000_000 {
		t.Errorf("UI.ReadToEndWarnThreshold = %d, want 1_000_000", cfg.UI.ReadToEndWarnThreshold)
	}
}

// TestGetDefaultConfigUIExportDefaults pins the dbsavvy-uv0.9 export
// knob defaults: 100_000 buffered-row warn threshold and 16 MiB
// clipboard cap.
func TestGetDefaultConfigUIExportDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.UI.Export.BufferedRowWarnThreshold != 100_000 {
		t.Errorf("UI.Export.BufferedRowWarnThreshold = %d, want 100_000", cfg.UI.Export.BufferedRowWarnThreshold)
	}
	if cfg.UI.Export.ClipboardMaxBytes != 16*1024*1024 {
		t.Errorf("UI.Export.ClipboardMaxBytes = %d, want %d (16 MiB)", cfg.UI.Export.ClipboardMaxBytes, 16*1024*1024)
	}
}

// TestGetDefaultConfigEditorAutocompleteDefault pins the dbsavvy-bwq.22
// (C5) default per ADR-16: auto-trigger completion is on by default on
// fresh install. Users opt out via `editor.autocomplete: false`.
func TestGetDefaultConfigEditorAutocompleteDefault(t *testing.T) {
	cfg := GetDefaultConfig()
	if !cfg.Editor.Autocomplete {
		t.Fatal("GetDefaultConfig().Editor.Autocomplete = false; want true (default per ADR-16)")
	}
}

// TestParseYAML_EditorAutocomplete_Disabled asserts the top-level
// `editor.autocomplete` YAML path decodes onto UserConfig.Editor.Autocomplete.
// Locks ADR-16: the path is `editor.autocomplete`, NOT
// `keys.editor.autocomplete`.
func TestParseYAML_EditorAutocomplete_Disabled(t *testing.T) {
	src := []byte("editor:\n  autocomplete: false\n")
	var cfg UserConfig
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.Editor.Autocomplete {
		t.Errorf("Editor.Autocomplete = true after parsing %q; want false", string(src))
	}
}

// TestParseYAML_EditorAutocomplete_Enabled is the symmetric positive
// case — explicit `true` is preserved on decode.
func TestParseYAML_EditorAutocomplete_Enabled(t *testing.T) {
	src := []byte("editor:\n  autocomplete: true\n")
	var cfg UserConfig
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if !cfg.Editor.Autocomplete {
		t.Errorf("Editor.Autocomplete = false after parsing %q; want true", string(src))
	}
}

// TestGetDefaultConfigEditorAutocompleteAliasDefault pins dbsavvy-ko4m.6.2
// (Finding K): table-accept alias auto-insert is on by default on fresh
// install. Users opt out via `editor.autocomplete_alias: false`.
func TestGetDefaultConfigEditorAutocompleteAliasDefault(t *testing.T) {
	cfg := GetDefaultConfig()
	if !cfg.Editor.AutocompleteAlias {
		t.Fatal("GetDefaultConfig().Editor.AutocompleteAlias = false; want true (default per ko4m.6.2)")
	}
}

// TestParseYAML_EditorAutocompleteAlias_Disabled asserts the top-level
// `editor.autocomplete_alias` YAML path decodes onto
// UserConfig.Editor.AutocompleteAlias (dbsavvy-ko4m.6.2, Finding K).
func TestParseYAML_EditorAutocompleteAlias_Disabled(t *testing.T) {
	src := []byte("editor:\n  autocomplete_alias: false\n")
	var cfg UserConfig
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.Editor.AutocompleteAlias {
		t.Errorf("Editor.AutocompleteAlias = true after parsing %q; want false", string(src))
	}
}
