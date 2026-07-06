package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

func durPtr(d time.Duration) *time.Duration { return &d }

func TestSaveUserConfig_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := &UserConfig{
		ConfigVersion: 1,
		Leader:        ",",
		LocalLeader:   "\\",
		Timeout:       durPtr(2 * time.Second),
		TimeoutLen:    durPtr(500 * time.Millisecond),
		TtimeoutLen:   durPtr(100 * time.Millisecond),
		WhichKeyDelay: durPtr(200 * time.Millisecond),
		Theme: ThemeConfig{
			ActiveBorder: "red",
		},
		Keybindings: []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
			{Mode: "n", Scope: "global", Key: "<leader>q", Action: "app.quit"},
		},
		Editor: EditorConfig{Autocomplete: false, FKForwardLimit: 500},
	}
	if err := SaveUserConfig(fs, "/cfg.yml", cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/cfg.yml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got UserConfig
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.Leader != cfg.Leader {
		t.Errorf("Leader = %q, want %q", got.Leader, cfg.Leader)
	}
	if got.Timeout == nil || *got.Timeout != *cfg.Timeout {
		t.Errorf("Timeout = %v, want %v", got.Timeout, cfg.Timeout)
	}
	if got.Editor.FKForwardLimit != cfg.Editor.FKForwardLimit {
		t.Errorf("Editor.FKForwardLimit = %d, want %d", got.Editor.FKForwardLimit, cfg.Editor.FKForwardLimit)
	}
	if len(got.Keybindings) != len(cfg.Keybindings) {
		t.Fatalf("Keybindings len = %d, want %d", len(got.Keybindings), len(cfg.Keybindings))
	}
	if got.Keybindings[0].Action != cfg.Keybindings[0].Action {
		t.Errorf("Keybindings[0].Action = %q, want %q", got.Keybindings[0].Action, cfg.Keybindings[0].Action)
	}
}

func TestSaveUserConfig_EmptyConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := GetDefaultConfig()
	if err := SaveUserConfig(fs, "/cfg.yml", cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/cfg.yml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got UserConfig
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.Leader != cfg.Leader {
		t.Errorf("Leader = %q, want %q", got.Leader, cfg.Leader)
	}
	if got.Timeout == nil || *got.Timeout != *cfg.Timeout {
		t.Errorf("Timeout = %v, want %v", got.Timeout, cfg.Timeout)
	}
}

func TestSaveUserConfig_AtomicFailure(t *testing.T) {
	base := afero.NewMemMapFs()
	fs := &renameFailFs{Fs: base}
	cfg := GetDefaultConfig()
	err := SaveUserConfig(fs, "/cfg.yml", cfg)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("err = %v; want rename failure", err)
	}
	if exists, _ := afero.Exists(base, "/cfg.yml.tmp"); exists {
		t.Errorf("tmp file remains after rename failure")
	}
	if exists, _ := afero.Exists(base, "/cfg.yml"); exists {
		t.Errorf("final file unexpectedly created after rename failure")
	}
}

func TestClone_NilSafe(t *testing.T) {
	var cfg *UserConfig
	got := cfg.Clone()
	if got != nil {
		t.Errorf("Clone() of nil = %v; want nil", got)
	}
}

func TestClone_EmptyConfig(t *testing.T) {
	cfg := GetDefaultConfig()
	clone := cfg.Clone()
	if clone == cfg {
		t.Fatal("Clone returned same pointer")
	}
	if !reflect.DeepEqual(cfg, clone) {
		t.Error("Clone is not DeepEqual to original")
	}
	clone.Leader = "x"
	if cfg.Leader == "x" {
		t.Error("mutating clone.Leader affected original")
	}
}

func TestCloneIsolation(t *testing.T) {
	original := &UserConfig{
		ConfigVersion: 1,
		Leader:        " ",
		LocalLeader:   ",",
		Timeout:       durPtr(1 * time.Second),
		TimeoutLen:    durPtr(1 * time.Second),
		TtimeoutLen:   durPtr(50 * time.Millisecond),
		WhichKeyDelay: durPtr(300 * time.Millisecond),
		Theme: ThemeConfig{
			ActiveBorder: "yellow",
		},
		Keybindings: []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "a", Action: "app.quit"},
			{Mode: "n", Scope: "global", Key: "b", Command: "echo hi"},
		},
		UI: UIConfig{
			Mouse:                  MouseConfig{Enabled: true, DoubleClickMs: 400},
			ResultPageSize:         200,
			ResultPrefetchRows:     50,
			PrefetchThreshold:      25,
			ReadToEndWarnThreshold: 1_000_000,
			Export: ExportConfig{
				BufferedRowWarnThreshold: 100_000,
				ClipboardMaxBytes:        16 * 1024 * 1024,
			},
			ResultGrid: ResultGridConfig{YankFormat: "csv"},
		},
		Editor: EditorConfig{Autocomplete: true, AutocompleteAlias: true, FKForwardLimit: 1000},
		Query:  QueryConfig{DefaultStatementTimeout: durPtr(30 * time.Second)},
	}

	clone := original.Clone()

	original.ConfigVersion = 99
	original.Leader = "["
	original.LocalLeader = "]"
	*original.Timeout = 99 * time.Second
	*original.TimeoutLen = 99 * time.Second
	*original.TtimeoutLen = 99 * time.Millisecond
	*original.WhichKeyDelay = 99 * time.Millisecond
	original.Theme = ThemeConfig{ActiveBorder: "green"}
	original.Keybindings = []KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "z", Action: "app.quit"},
	}
	original.Keybindings[0].Mode = "i"
	original.UI = UIConfig{
		Mouse:                  MouseConfig{Enabled: false, DoubleClickMs: 999},
		ResultPageSize:         999,
		ResultPrefetchRows:     999,
		PrefetchThreshold:      999,
		ReadToEndWarnThreshold: 999,
		Export: ExportConfig{
			BufferedRowWarnThreshold: 999,
			ClipboardMaxBytes:        999,
		},
		ResultGrid: ResultGridConfig{YankFormat: "ndjson"},
	}
	original.Editor = EditorConfig{Autocomplete: false, AutocompleteAlias: false, FKForwardLimit: 999}
	*original.Query.DefaultStatementTimeout = 99 * time.Second

	if clone.ConfigVersion != 1 {
		t.Errorf("ConfigVersion = %d, want 1", clone.ConfigVersion)
	}
	if clone.Leader != " " {
		t.Errorf("Leader = %q, want space", clone.Leader)
	}
	if clone.LocalLeader != "," {
		t.Errorf("LocalLeader = %q, want comma", clone.LocalLeader)
	}
	if clone.Timeout == nil || *clone.Timeout != 1*time.Second {
		t.Errorf("Timeout = %v, want 1s", clone.Timeout)
	}
	if clone.TimeoutLen == nil || *clone.TimeoutLen != 1*time.Second {
		t.Errorf("TimeoutLen = %v, want 1s", clone.TimeoutLen)
	}
	if clone.TtimeoutLen == nil || *clone.TtimeoutLen != 50*time.Millisecond {
		t.Errorf("TtimeoutLen = %v, want 50ms", clone.TtimeoutLen)
	}
	if clone.WhichKeyDelay == nil || *clone.WhichKeyDelay != 300*time.Millisecond {
		t.Errorf("WhichKeyDelay = %v, want 300ms", clone.WhichKeyDelay)
	}
	if clone.Theme.ActiveBorder != "yellow" {
		t.Errorf("Theme.ActiveBorder = %q, want yellow", clone.Theme.ActiveBorder)
	}
	if len(clone.Keybindings) != 2 {
		t.Fatalf("Keybindings len = %d, want 2", len(clone.Keybindings))
	}
	if clone.Keybindings[0].Key != "a" {
		t.Errorf("Keybindings[0].Key = %q, want a", clone.Keybindings[0].Key)
	}
	if clone.Keybindings[0].Mode != "n" {
		t.Errorf("Keybindings[0].Mode = %q, want n", clone.Keybindings[0].Mode)
	}
	if clone.UI.Mouse.Enabled != true {
		t.Errorf("UI.Mouse.Enabled = %v, want true", clone.UI.Mouse.Enabled)
	}
	if clone.UI.ResultPageSize != 200 {
		t.Errorf("UI.ResultPageSize = %d, want 200", clone.UI.ResultPageSize)
	}
	if clone.UI.Export.BufferedRowWarnThreshold != 100_000 {
		t.Errorf("UI.Export.BufferedRowWarnThreshold = %d, want 100_000", clone.UI.Export.BufferedRowWarnThreshold)
	}
	if clone.Editor.Autocomplete != true {
		t.Errorf("Editor.Autocomplete = %v, want true", clone.Editor.Autocomplete)
	}
	if clone.Editor.FKForwardLimit != 1000 {
		t.Errorf("Editor.FKForwardLimit = %d, want 1000", clone.Editor.FKForwardLimit)
	}
	if clone.Query.DefaultStatementTimeout == nil || *clone.Query.DefaultStatementTimeout != 30*time.Second {
		t.Errorf("Query.DefaultStatementTimeout = %v, want 30s", clone.Query.DefaultStatementTimeout)
	}

	clone.Keybindings[0].Key = "mutated"
	if original.Keybindings[0].Key != "z" {
		t.Errorf("mutating clone Keybinding.Key affected original Key: got %q, want %q", original.Keybindings[0].Key, "z")
	}
}

func TestSaveUserConfig_ReadOnlyFilesystem(t *testing.T) {
	base := afero.NewMemMapFs()
	ro := afero.NewReadOnlyFs(base)
	err := SaveUserConfig(ro, "/cfg.yml", GetDefaultConfig())
	if err == nil {
		t.Fatal("expected error on read-only filesystem, got nil")
	}
}

func TestSaveUserConfig_CreatesParentDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := GetDefaultConfig()
	if err := SaveUserConfig(fs, "/a/b/c/cfg.yml", cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	if exists, _ := afero.Exists(fs, "/a/b/c/cfg.yml"); !exists {
		t.Fatal("file not created at nested path")
	}
}

func TestSaveUserConfig_NilConfig(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := SaveUserConfig(fs, "/cfg.yml", nil); err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

func TestCloneIsolation_Reflect(t *testing.T) {
	original := &UserConfig{
		ConfigVersion: 1,
		Leader:        " ",
		LocalLeader:   ",",
		Timeout:       durPtr(1 * time.Second),
		TimeoutLen:    durPtr(1 * time.Second),
		TtimeoutLen:   durPtr(50 * time.Millisecond),
		WhichKeyDelay: durPtr(300 * time.Millisecond),
		Keybindings: []KeybindingConfig{
			{Mode: "n", Scope: "global", Key: "a", Action: "app.quit"},
		},
		Theme:  ThemeConfig{ActiveBorder: "red"},
		UI:     UIConfig{ResultPageSize: 100},
		Editor: EditorConfig{Autocomplete: true},
		Query:  QueryConfig{DefaultStatementTimeout: durPtr(5 * time.Second)},
	}

	clone := original.Clone()

	origVal := reflect.ValueOf(original).Elem()
	origType := origVal.Type()

	for i := 0; i < origType.NumField(); i++ {
		field := origType.Field(i)
		if !field.IsExported() {
			continue
		}
		fieldVal := origVal.Field(i)
		if !fieldVal.CanSet() {
			continue
		}

		switch fieldVal.Kind() {
		case reflect.String:
			if fieldVal.String() != "" {
				fieldVal.SetString("MUTATED_" + field.Name)
			} else {
				fieldVal.SetString("MUTATED")
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fieldVal.SetInt(999999)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fieldVal.SetUint(999999)
		case reflect.Float32, reflect.Float64:
			fieldVal.SetFloat(99.99)
		case reflect.Bool:
			fieldVal.SetBool(!fieldVal.Bool())
		case reflect.Ptr:
			if fieldVal.Type() == reflect.TypeOf((*time.Duration)(nil)) && !fieldVal.IsNil() {
				newVal := 999 * time.Hour
				fieldVal.Set(reflect.ValueOf(&newVal))
			}
		case reflect.Slice:
			if fieldVal.Type() == reflect.TypeOf([]KeybindingConfig{}) && fieldVal.Len() > 0 {
				fieldVal.Set(reflect.MakeSlice(fieldVal.Type(), 0, 0))
			}
		case reflect.Struct:
			mutateStructFields(fieldVal)
		}
	}

	if reflect.DeepEqual(original, clone) {
		t.Error("mutating original did not diverge from clone — Clone() shares memory")
	}
	if !reflect.DeepEqual(original, original.Clone()) {
		t.Error("Clone of mutated original should DeepEqual the mutated original")
	}
}

func mutateStructFields(v reflect.Value) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		if !fv.CanSet() {
			continue
		}
		switch fv.Kind() {
		case reflect.String:
			fv.SetString("MUTATED")
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fv.SetInt(999999)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fv.SetUint(999999)
		case reflect.Float32, reflect.Float64:
			fv.SetFloat(99.99)
		case reflect.Bool:
			fv.SetBool(!fv.Bool())
		case reflect.Ptr:
			if fv.Type() == reflect.TypeOf((*time.Duration)(nil)) && !fv.IsNil() {
				newVal := 999 * time.Hour
				fv.Set(reflect.ValueOf(&newVal))
			}
		}
	}
}
