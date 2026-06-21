package config

import (
	"reflect"
	"testing"

	"github.com/spf13/afero"
)

func TestLoadUserConfig_NilFiles_ReturnsDefaults(t *testing.T) {
	fs := afero.NewMemMapFs()
	got, err := LoadUserConfig(fs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := GetDefaultConfig()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nil files: got %#v, want %#v", got, want)
	}
}

func TestLoadUserConfig_EmptyFiles_ReturnsDefaults(t *testing.T) {
	fs := afero.NewMemMapFs()
	got, err := LoadUserConfig(fs, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, GetDefaultConfig()) {
		t.Fatalf("empty files: did not equal defaults")
	}
}

func TestLoadUserConfig_ScalarOverlayMergesOverDefaults(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/cfg.yml"
	if err := afero.WriteFile(fs, path, []byte("theme:\n  active_border: blue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig(fs, []string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Theme.ActiveBorder != "blue" {
		t.Errorf("ActiveBorder = %q, want %q", cfg.Theme.ActiveBorder, "blue")
	}
	if cfg.Leader != " " {
		t.Errorf("Leader = %q, want default %q", cfg.Leader, " ")
	}
	if cfg.Theme.NullValueFg != "red" {
		t.Errorf("NullValueFg = %q, want default %q", cfg.Theme.NullValueFg, "red")
	}
}

func TestLoadUserConfig_LaterFileWins(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/a.yml", []byte("leader: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(fs, "/b.yml", []byte("leader: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig(fs, []string{"/a.yml", "/b.yml"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Leader != "y" {
		t.Errorf("Leader = %q, want %q (later file wins)", cfg.Leader, "y")
	}
}

func TestLoadUserConfig_MissingFileReturnsError(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg, err := LoadUserConfig(fs, []string{"/nope.yml"})
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if cfg != nil {
		t.Errorf("expected nil cfg on error, got %#v", cfg)
	}
}

func TestLoadUserConfig_KeybindingsOverlayReplacesSlice(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/cfg.yml"
	yaml := "keybindings:\n  - mode: n\n    scope: global\n    key: q\n    action: app.quit\n"
	if err := afero.WriteFile(fs, path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig(fs, []string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Keybindings) != 1 {
		t.Fatalf("len(Keybindings) = %d, want 1 (overlay replaces, does not merge)", len(cfg.Keybindings))
	}
	if cfg.Keybindings[0].Action != "app.quit" {
		t.Errorf("Action = %q, want %q", cfg.Keybindings[0].Action, "app.quit")
	}
}

func TestLoadUserConfig_EmptyFileYieldsDefaults(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/cfg.yml"
	if err := afero.WriteFile(fs, path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig(fs, []string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg, GetDefaultConfig()) {
		t.Errorf("empty YAML overlay should not change defaults")
	}
}

func TestLoadUserConfig_UnknownTopLevelKeyTolerated(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/cfg.yml"
	if err := afero.WriteFile(fs, path, []byte("unknown_thing: 42\nleader: z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadUserConfig(fs, []string{path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Leader != "z" {
		t.Errorf("Leader = %q, want %q", cfg.Leader, "z")
	}
}
