package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineAccess_Validation(t *testing.T) {
	for _, suffix := range []string{"", "    cli_access: null\n", "    cli_access: \"\"\n", "    cli_access: disabled\n"} {
		fs := afero.NewMemMapFs()
		body := "connections:\n  - name: dev\n" + suffix
		if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConnections(fs, "/c.yml")
		if err != nil || len(got) != 1 || got[0].EffectiveCLIAccess() != models.CLIAccessDisabled {
			t.Fatalf("default %q: %v, %v", suffix, got, err)
		}
	}
	for _, suffix := range []string{"    cli_access: true\n", "    cli_access: 1\n", "    cli_access: []\n", "    cli_access: {}\n", "    cli_access: invalid\n", "    cli_access: metadata\n    cli_access: row-read\n"} {
		fs := afero.NewMemMapFs()
		body := []byte("connections:\n  - name: dev\n" + suffix)
		if err := afero.WriteFile(fs, "/c.yml", body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConnections(fs, "/c.yml"); err == nil {
			t.Errorf("invalid policy accepted: %s", suffix)
		}
		after, _ := afero.ReadFile(fs, "/c.yml")
		if !bytes.Equal(body, after) {
			t.Fatal("load rewrote file")
		}
	}
	fs := afero.NewMemMapFs()
	body := []byte("connections:\n  - name: dev\n    cli_access: full\n")
	if err := afero.WriteFile(fs, "/c.yml", body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil || len(got) != 1 || got[0].CLIAccess != models.CLIAccessFull || got[0].EffectiveCLIAccess() != models.CLIAccessFull {
		t.Fatalf("explicit full policy: %v, %v", got, err)
	}
	after, err := afero.ReadFile(fs, "/c.yml")
	if err != nil || !bytes.Equal(body, after) {
		t.Fatal("loading full policy rewrote file")
	}
}

func TestCLIHistoryConsent_StrictYAML(t *testing.T) {
	for _, value := range []string{"null", "~", "", "yes", "on", "\"yes\"", "\"on\"", "\"true\"", "!!str true", "!!bool SECRET_HISTORY_VALUE", "1", "[]", "{}", "SECRET_HISTORY_VALUE"} {
		t.Run(value, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			body := []byte("connections:\n  - name: dev\n    cli_history: " + value + "\n")
			if err := afero.WriteFile(fs, "/c.yml", body, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConnections(fs, "/c.yml")
			if err == nil || !strings.Contains(err.Error(), "cli_history must be a YAML boolean") {
				t.Fatalf("non-boolean consent accepted or unsafe parse path: %v", err)
			}
			if strings.Contains(err.Error(), "SECRET_HISTORY_VALUE") {
				t.Fatal("consent error exposed raw value")
			}
			after, _ := afero.ReadFile(fs, "/c.yml")
			if !bytes.Equal(body, after) {
				t.Fatal("invalid load rewrote configuration")
			}
		})
	}
	for _, body := range []string{
		"connections:\n  - name: dev\n    cli_access: null\n",
		"connections:\n  - name: dev\n    cli_history: false\n",
		"connections:\n  - name: dev\n    cli_history: true\n",
		"connections:\n  - name: dev\n    cli_history: !!bool 'true'\n",
		"connections:\n  - &base\n    name: dev\n    cli_history: &enabled true\n  - name: second\n    cli_history: *enabled\n  - <<: *base\n    name: third\n",
	} {
		fs := afero.NewMemMapFs()
		if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConnections(fs, "/c.yml"); err != nil {
			t.Fatalf("valid consent/legacy null rejected: %v", err)
		}
	}
	for _, body := range []string{
		"connections:\n  - name: first\n    label: &flag yes\n  - name: second\n    cli_history: *flag\n",
		"connections:\n  - name: first\n    &key cli_history: true\n  - name: second\n    *key : yes\n",
		"connections:\n  - name: dev\n    !!binary Y2xpX2hpc3Rvcnk=: yes\n",
		"connections:\n  - &profile\n    name: dev\n    cli_history: *profile\n",
		"connections:\n  - &base\n    name: base\n    cli_history: null\n  - <<: *base\n    name: merged\n",
	} {
		fs := afero.NewMemMapFs()
		if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConnections(fs, "/c.yml"); err == nil || !strings.Contains(err.Error(), "YAML boolean") {
			t.Fatalf("alias bypassed consent type validation: %v", err)
		}
	}
	fs := afero.NewMemMapFs()
	body := "connections:\n  - name: dev\n    cli_history: true\n    ssh_tunnel:\n      host: bastion\n      bogus_ssh_key: yes\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConnections(fs, "/c.yml"); err == nil || !strings.Contains(err.Error(), "bogus_ssh_key") {
		t.Fatalf("nested unknown-key rejection lost: %v", err)
	}
}

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

func TestLoadConnections_WrapperForm(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n  - name: prod\n    driver: postgres\n    dsn: postgres://localhost/prod\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
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
	body := "connections:\n" +
		"  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    password_command: \"vault read /pg\"\n" +
		"  - name: prod\n    driver: postgres\n    dsn: postgres://localhost/prod\n    keyring: \"pgsavvy/prod\"\n" +
		"  - name: legacy\n    driver: postgres\n    dsn: postgres://localhost/legacy\n    pgpass: \"/home/u/.pgpass\"\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
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
	if got[1].KeyringRef != "pgsavvy/prod" {
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

func TestLoadConnections_RejectsLegacyFlatFormat(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "- name: dev\n  driver: postgres\n  dsn: postgres://localhost/dev\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err == nil {
		t.Fatal("expected legacy-format error, got nil")
	}
	if got != nil {
		t.Errorf("want nil slice, got %#v", got)
	}
	msg := err.Error()
	for _, want := range []string{
		"legacy flat format",
		"expected key 'connections:'",
		"DESIGN.md §11.2",
		"connections:\n  - name: dev",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("err missing %q\nerr=%s", want, msg)
		}
	}
}

func TestLoadConnections_RejectsLegacyFlatFormat_WithLeadingCommentsAndBlanks(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "# header comment\n\n- name: dev\n  driver: postgres\n  dsn: postgres://localhost/dev\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConnections(fs, "/c.yml")
	if err == nil || !strings.Contains(err.Error(), "legacy flat format") {
		t.Fatalf("want legacy-format error, got: %v", err)
	}
}

func TestLoadConnections_EmptyConnectionsKeyIsAllowed(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/c.yml", []byte("connections: ~\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
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

func TestLoadConnections_EmptyArrayIsAllowed(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/c.yml", []byte("connections: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
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

func TestLoadConnections_WithSchemaVersionLoadsCleanly(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "schema_version: 1\nconnections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("schema_version should load cleanly under KnownFields: %v", err)
	}
	if len(got) != 1 || got[0].Name != "dev" {
		t.Errorf("loaded = %+v; want one profile 'dev'", got)
	}
}

func TestLoadConnections_WithoutSchemaVersionLoadsCleanly(t *testing.T) {
	fs := afero.NewMemMapFs()
	// omitempty + known field: absence must NOT trip KnownFields(true).
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConnections(fs, "/c.yml"); err != nil {
		t.Fatalf("file without schema_version should load cleanly: %v", err)
	}
}

func TestLoadConnections_UnknownKeyAtRootIsRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "connections: []\nbogus_root: x\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConnections(fs, "/c.yml")
	if err == nil {
		t.Fatal("expected error for unknown root key, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_root") {
		t.Errorf("err should name the bad key; got: %v", err)
	}
}

func TestLoadConnections_UnknownKeyInProfileIsRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    bogus_key: x\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConnections(fs, "/c.yml")
	if err == nil {
		t.Fatal("expected error for unknown profile key, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("err should name the bad key; got: %v", err)
	}
}

func TestLoadConnections_UnknownKeyInSSHTunnelIsRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    ssh_tunnel:\n      host: bastion\n      user: ops\n      port: 22\n      identity_file: /id\n      bogus: x\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConnections(fs, "/c.yml")
	if err == nil {
		t.Fatal("expected error for unknown ssh_tunnel key, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("err should name the bad key; got: %v", err)
	}
}

func TestLoadConnections_WarnsOnPermissivePasswordFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    password: secret\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	requireConnectionFileMode(t, path, 0o644)
	var buf bytes.Buffer
	orig := warnWriter
	warnWriter = &buf
	t.Cleanup(func() { warnWriter = orig })

	got, err := LoadConnections(afero.NewOsFs(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Password != "secret" {
		t.Fatalf("got = %+v", got)
	}
	out := buf.String()
	if !strings.Contains(out, "group/world readable") {
		t.Errorf("want WARN on permissive mode, got: %q", out)
	}
	if !strings.Contains(out, path) {
		t.Errorf("WARN should name the file path; got: %q", out)
	}
}

func TestLoadConnections_NoWarnWhenStrictMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    password: secret\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	orig := warnWriter
	warnWriter = &buf
	t.Cleanup(func() { warnWriter = orig })

	if _, err := LoadConnections(afero.NewOsFs(), path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN at mode 0600, got: %q", buf.String())
	}
}

func TestLoadConnections_NoWarnWhenNoInlinePassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    keyring: pgsavvy/dev\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	orig := warnWriter
	warnWriter = &buf
	t.Cleanup(func() { warnWriter = orig })

	if _, err := LoadConnections(afero.NewOsFs(), path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no WARN when no inline password, got: %q", buf.String())
	}
}

func TestLoadConnections_RejectsExecutableCredentialCommandFromWritableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    password_command: printf-secret\n"
	if err := os.WriteFile(path, []byte(body), 0o622); err != nil {
		t.Fatal(err)
	}
	requireConnectionFileMode(t, path, 0o622)

	_, err := LoadConnections(afero.NewOsFs(), path)
	if !errors.Is(err, ErrExecutableCredentialsInsecureMode) {
		t.Fatalf("err = %v, want ErrExecutableCredentialsInsecureMode", err)
	}
}

func TestLoadConnections_AllowsExecutableCredentialCommandFromPrivateFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	body := "connections:\n  - name: dev\n    driver: postgres\n    dsn: postgres://localhost/dev\n    ssh_tunnel:\n      host: bastion\n      user: ops\n      ssh_password_command: printf-secret\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConnections(afero.NewOsFs(), path); err != nil {
		t.Fatalf("LoadConnections() error = %v", err)
	}
}

func requireConnectionFileMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	// Creation modes are filtered by umask; these fixtures need the exact mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Fatalf("fixture mode = %04o, want %04o", got, mode)
	}
}
