package config

import (
	"testing"

	"github.com/spf13/afero"
)

func TestLoadConnections_MissingFileReturnsEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	got, err := LoadConnections(fs, "/missing.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestLoadConnections_TwoConnections(t *testing.T) {
	fs := afero.NewMemMapFs()
	yaml := "- name: dev\n  driver: postgres\n  dsn: postgres://localhost/dev\n- name: prod\n  driver: postgres\n  dsn: postgres://localhost/prod\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "dev" || got[1].Name != "prod" {
		t.Errorf("names = %q,%q; want dev,prod", got[0].Name, got[1].Name)
	}
}

func TestLoadConnections_PasswordIndirectionFields(t *testing.T) {
	fs := afero.NewMemMapFs()
	yaml := "- name: dev\n  driver: postgres\n  dsn: postgres://localhost/dev\n  password_command: \"vault read /pg\"\n" +
		"- name: prod\n  driver: postgres\n  dsn: postgres://localhost/prod\n  keyring: \"dbsavvy/prod\"\n" +
		"- name: legacy\n  driver: postgres\n  dsn: postgres://localhost/legacy\n  pgpass: \"/home/u/.pgpass\"\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].PasswordCommand != "vault read /pg" {
		t.Errorf("PasswordCommand = %q", got[0].PasswordCommand)
	}
	if got[0].Password != "" {
		t.Errorf("Password = %q, want empty", got[0].Password)
	}
	if got[1].KeyringRef != "dbsavvy/prod" {
		t.Errorf("KeyringRef = %q", got[1].KeyringRef)
	}
	if got[1].Password != "" {
		t.Errorf("Password = %q, want empty", got[1].Password)
	}
	if got[2].PgpassPath != "/home/u/.pgpass" {
		t.Errorf("PgpassPath = %q", got[2].PgpassPath)
	}
}

func TestLoadConnections_MalformedYAMLReturnsError(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/c.yml", []byte("this: is: not: valid: yaml\n  - bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
	if got != nil {
		t.Errorf("expected nil slice on error, got %#v", got)
	}
}
