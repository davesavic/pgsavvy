package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestGetDefaultConfigUIMouseEnabled pins the default.
// Mouse is opt-out: the bootstrap-time mouse-binding registration block
// in pkg/gui/controllers/* skips entirely when this flag is false, so
// the default MUST stay true to preserve documented UX.
func TestGetDefaultConfigUIMouseEnabled(t *testing.T) {
	if !GetDefaultConfig().UI.Mouse.Enabled {
		t.Fatal("GetDefaultConfig().UI.Mouse.Enabled = false; want true")
	}
}

// TestGetDefaultConfigLeaderDefaults pins the leader defaults.
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

// TestGetDefaultConfigUIPaginationDefaults pins the pagination knob
// defaults: 200/50/25/1_000_000.
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

// TestGetDefaultConfigUIExportDefaults pins the export knob defaults:
// 100_000 buffered-row warn threshold and 16 MiB clipboard cap.
func TestGetDefaultConfigUIExportDefaults(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.UI.Export.BufferedRowWarnThreshold != 100_000 {
		t.Errorf("UI.Export.BufferedRowWarnThreshold = %d, want 100_000", cfg.UI.Export.BufferedRowWarnThreshold)
	}
	if cfg.UI.Export.ClipboardMaxBytes != 16*1024*1024 {
		t.Errorf("UI.Export.ClipboardMaxBytes = %d, want %d (16 MiB)", cfg.UI.Export.ClipboardMaxBytes, 16*1024*1024)
	}
}

// TestGetDefaultConfigEditorAutocompleteDefault pins the default per
// ADR-16: auto-trigger completion is on by default on fresh install.
// Users opt out via `editor.autocomplete: false`.
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

// TestGetDefaultConfigEditorAutocompleteAliasDefault pins the default:
// table-accept alias auto-insert is on by default on fresh install.
// Users opt out via `editor.autocomplete_alias: false`.
func TestGetDefaultConfigEditorAutocompleteAliasDefault(t *testing.T) {
	cfg := GetDefaultConfig()
	if !cfg.Editor.AutocompleteAlias {
		t.Fatal("GetDefaultConfig().Editor.AutocompleteAlias = false; want true (default)")
	}
}

// TestParseYAML_EditorAutocompleteAlias_Disabled asserts the top-level
// `editor.autocomplete_alias` YAML path decodes onto
// UserConfig.Editor.AutocompleteAlias.
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

// TestThemeConfig_DroppedDeadFieldsAreGone guards the ialt epic trim: the 25
// fields that no renderer reads must not reappear on the config surface.
// Reflection over yaml tags means an accidental reintroduction (or an
// off-by-one trim that left one behind) fails here rather than silently
// re-advertising a no-op color knob.
func TestThemeConfig_DroppedDeadFieldsAreGone(t *testing.T) {
	dropped := []string{
		"selected_row_bg", "selected_row_fg", "background_bg", "foreground_fg",
		"status_bar_bg", "status_bar_fg", "command_line_bg", "command_line_fg",
		"hint_fg", "popup_bg", "popup_fg", "menu_bg", "menu_fg",
		"menu_selected_bg", "menu_selected_fg", "table_header_bg", "table_row_alt_bg",
		"gutter_fg", "line_number_fg", "cursor_bg", "cursor_fg", "match_highlight",
		"diff_added_fg", "diff_removed_fg", "diff_changed_fg",
	}
	present := map[string]bool{}
	tp := reflect.TypeOf(ThemeConfig{})
	for i := 0; i < tp.NumField(); i++ {
		tag := tp.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		present[strings.Split(tag, ",")[0]] = true
	}
	for _, k := range dropped {
		if present[k] {
			t.Errorf("ThemeConfig still carries trimmed yaml key %q (must be removed)", k)
		}
	}
}
