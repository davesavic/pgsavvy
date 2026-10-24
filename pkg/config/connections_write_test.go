package config

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type renameFailFs struct {
	afero.Fs
}

func (r *renameFailFs) Rename(oldname, newname string) error {
	return errors.New("simulated rename failure")
}

// withWarnWriter swaps warnWriter and restores it on test cleanup. Returns the
// buffer to inspect.
func withWarnWriter(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := warnWriter
	warnWriter = buf
	t.Cleanup(func() { warnWriter = prev })
	return buf
}

func TestSaveConnections_RoundTrip(t *testing.T) {
	fs := newConnectionsTestFs()
	conns := []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
		{Name: "prod", Driver: "postgres", DSN: "postgres://localhost/prod"},
	}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("LoadConnections (KnownFields strict): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "dev" || got[1].Name != "prod" {
		t.Errorf("names = %q,%q; want dev,prod", got[0].Name, got[1].Name)
	}
}

func TestSaveConnections_SSHTunnelAllFieldsRoundTrip(t *testing.T) {
	fs := newConnectionsTestFs()
	conns := []models.Connection{{
		Name:   "dev",
		Driver: "postgres",
		DSN:    "postgres://localhost/dev",
		SSHTunnel: &models.SSHTunnelConfig{
			Host:              "bastion.example.com",
			User:              "jump",
			Port:              2222,
			IdentityFile:      "/home/u/.ssh/id_ed25519",
			IdentityFromAgent: true,
			PassphraseCommand: "pass show ssh/key",
			KnownHosts:        "/home/u/.ssh/known_hosts",
		},
	}}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(conns[0].SSHTunnel, got[0].SSHTunnel) {
		t.Errorf("ssh tunnel not preserved.\n want %+v\n  got %+v", conns[0].SSHTunnel, got[0].SSHTunnel)
	}
}

func TestSaveConnections_SSHOmitemptyKeysAbsent(t *testing.T) {
	fs := newConnectionsTestFs()
	// Nil tunnel and zero-value new fields must not emit keys.
	conns := []models.Connection{{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"}}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/c.yml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "ssh_tunnel:") {
		t.Errorf("nil tunnel emitted ssh_tunnel key:\n%s", body)
	}

	// A tunnel with only Host/User set must not emit the omitempty new fields.
	conns2 := []models.Connection{{
		Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev",
		SSHTunnel: &models.SSHTunnelConfig{Host: "bastion", User: "ops"},
	}}
	if err := SaveConnections(fs, "/c.yml", conns2); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	raw, err = afero.ReadFile(fs, "/c.yml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body = string(raw)
	for _, key := range []string{"port", "identity_file", "identity_from_agent", "passphrase_command", "known_hosts"} {
		if strings.Contains(body, key) {
			t.Errorf("empty %q emitted a key:\n%s", key, body)
		}
	}
}

func TestSaveConnections_DropsDSNWhenDiscreteSet(t *testing.T) {
	fs := newConnectionsTestFs()
	conns := []models.Connection{{
		Name: "dev", Driver: "postgres",
		DSN:  "postgres://legacy@old/db",
		Host: "newhost", Port: 5432, User: "app", Database: "appdb", SSLMode: "require",
	}}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/c.yml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(raw)
	// The dsn key is always emitted (no omitempty on the field), but its
	// VALUE must be cleared so it cannot override the discrete fields on load.
	if strings.Contains(body, "postgres://legacy@old/db") {
		t.Errorf("legacy dsn VALUE persisted despite discrete fields set:\n%s", body)
	}
	if !strings.Contains(body, `dsn: ""`) {
		t.Errorf("dsn value not cleared:\n%s", body)
	}
	if !strings.Contains(body, "host: newhost") {
		t.Errorf("discrete host not written:\n%s", body)
	}
	// Caller's in-memory struct must NOT be mutated (operate on a copy).
	if conns[0].DSN != "postgres://legacy@old/db" {
		t.Errorf("caller DSN mutated: %q", conns[0].DSN)
	}
}

func TestSaveConnections_KeepsDSNWhenNoDiscrete(t *testing.T) {
	fs := newConnectionsTestFs()
	conns := []models.Connection{{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"}}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	raw, _ := afero.ReadFile(fs, "/c.yml")
	if !strings.Contains(string(raw), "dsn: postgres://localhost/dev") {
		t.Errorf("dsn dropped when no discrete fields set:\n%s", raw)
	}
}

func TestSaveConnections_StripsInlinePassword(t *testing.T) {
	withWarnWriter(t) // silence WARN
	fs := newConnectionsTestFs()
	conns := []models.Connection{{
		Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev",
		Password: "hunter2supersecret",
	}}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	raw, err := afero.ReadFile(fs, "/c.yml")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "password:") {
		t.Errorf("password key persisted:\n%s", body)
	}
	if strings.Contains(body, "hunter2supersecret") {
		t.Errorf("plaintext password value leaked into written bytes:\n%s", body)
	}
	// Caller's in-memory struct must NOT be mutated.
	if conns[0].Password != "hunter2supersecret" {
		t.Errorf("caller Password mutated: %q", conns[0].Password)
	}
}

func TestSaveConnections_RenameFailureRemovesTmp(t *testing.T) {
	base := newConnectionsTestFs()
	fs := &renameFailFs{Fs: base}
	conns := []models.Connection{{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"}}
	err := SaveConnections(fs, "/c.yml", conns)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if exists, _ := afero.Exists(base, "/c.yml.tmp"); exists {
		t.Errorf("tmp file remains after rename failure")
	}
	if exists, _ := afero.Exists(base, "/c.yml"); exists {
		t.Errorf("final file unexpectedly created after rename failure")
	}
}

func TestSaveConnections_WarnOnInlinePassword(t *testing.T) {
	buf := withWarnWriter(t)
	fs := newConnectionsTestFs()
	conns := []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev", Password: "hunter2"},
	}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "plaintext password") {
		t.Errorf("warning output = %q; want it to mention plaintext password", out)
	}
}

func TestSaveConnections_NoWarnWhenClean(t *testing.T) {
	buf := withWarnWriter(t)
	fs := newConnectionsTestFs()
	conns := []models.Connection{{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"}}
	if err := SaveConnections(fs, "/c.yml", conns); err != nil {
		t.Fatalf("SaveConnections: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no warning, got %q", buf.String())
	}
}

func TestAppendConnection_MissingFileCreates(t *testing.T) {
	fs := newConnectionsTestFs()
	c := models.Connection{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"}
	if err := AppendConnection(fs, "/c.yml", c); err != nil {
		t.Fatalf("AppendConnection: %v", err)
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(got) != 1 || got[0].Name != "dev" {
		t.Errorf("loaded = %+v; want one profile 'dev'", got)
	}
}

func TestAppendConnection_DuplicateName(t *testing.T) {
	fs := newConnectionsTestFs()
	c := models.Connection{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"}
	if err := AppendConnection(fs, "/c.yml", c); err != nil {
		t.Fatalf("first AppendConnection: %v", err)
	}
	dup := models.Connection{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/other"}
	err := AppendConnection(fs, "/c.yml", dup)
	if !errors.Is(err, ErrDuplicateConnectionName) {
		t.Fatalf("err = %v; want ErrDuplicateConnectionName", err)
	}
	// Ensure no overwrite happened.
	got, _ := LoadConnections(fs, "/c.yml")
	if len(got) != 1 || got[0].DSN != "postgres://localhost/dev" {
		t.Errorf("file mutated after duplicate rejection: %+v", got)
	}
}

func TestAppendConnection_PropagatesLoadError(t *testing.T) {
	fs := newConnectionsTestFs()
	// Legacy flat form — LoadConnections rejects it loudly (M10f).
	body := "- name: dev\n  driver: postgres\n  dsn: postgres://localhost/dev\n"
	if err := afero.WriteFile(fs, "/c.yml", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AppendConnection(fs, "/c.yml", models.Connection{Name: "new", Driver: "postgres", DSN: "postgres://x"})
	if err == nil {
		t.Fatal("expected load error from legacy flat form, got nil")
	}
	if !strings.Contains(err.Error(), "legacy flat") {
		t.Errorf("err = %v; want legacy-flat load error propagated", err)
	}
}

func TestIsInlinePasswordPresent(t *testing.T) {
	cases := []struct {
		name string
		in   []models.Connection
		want bool
	}{
		{"empty", nil, false},
		{"none", []models.Connection{{Name: "a"}, {Name: "b"}}, false},
		{"first", []models.Connection{{Name: "a", Password: "x"}, {Name: "b"}}, true},
		{"last", []models.Connection{{Name: "a"}, {Name: "b", Password: "x"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsInlinePasswordPresent(c.in); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMachineAccess_RoundTrip(t *testing.T) {
	for _, policy := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
		t.Run(string(policy), func(t *testing.T) {
			fs := newConnectionsTestFs()
			conns := []models.Connection{{Name: "dev", CLIAccess: policy}}
			if err := SaveConnections(fs, "/private/c.yml", conns); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConnections(fs, "/private/c.yml")
			if err != nil || len(got) != 1 || got[0].CLIAccess != policy || conns[0].CLIAccess != policy {
				t.Fatalf("round trip: %v, %v", got, err)
			}
			for path, mode := range map[string]uint32{"/private": 0o700, "/private/c.yml": 0o600} {
				info, err := fs.Stat(path)
				if err != nil || uint32(info.Mode().Perm()) != mode {
					t.Fatalf("mode %s: %v, %v", path, info, err)
				}
			}
		})
	}
	fs := newConnectionsTestFs()
	before := []byte("connections: []\n")
	if err := afero.WriteFile(fs, "/c.yml", before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveConnections(fs, "/c.yml", []models.Connection{{CLIAccess: "invalid"}}); err == nil {
		t.Fatal("invalid policy saved")
	}
	after, _ := afero.ReadFile(fs, "/c.yml")
	if !bytes.Equal(before, after) {
		t.Fatal("invalid save changed file")
	}
}

func TestMachineAccess_FormPreservation(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		for _, policy := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
			fs := newConnectionsTestFs()
			stale := models.Connection{Name: "dev", CLIAccess: models.CLIAccessRowRead}
			if renamed {
				stale.Name = "renamed"
			}
			stale.Label = "edited"
			if err := SaveConnections(fs, "/c.yml", []models.Connection{{Name: "dev", CLIAccess: policy}, {Name: "other", CLIAccess: models.CLIAccessMetadata}}); err != nil {
				t.Fatal(err)
			}
			if err := UpdateConnection(fs, "/c.yml", "dev", stale); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConnections(fs, "/c.yml")
			if err != nil || len(got) != 2 || got[0].CLIAccess != policy || got[0].Label != "edited" || got[0].Name != stale.Name || got[1].CLIAccess != models.CLIAccessMetadata {
				t.Fatalf("disk policy %q, rename %v: %v, %v", policy, renamed, got, err)
			}
			if stale.CLIAccess != models.CLIAccessRowRead {
				t.Fatal("caller copy mutated")
			}
		}
	}
}

func TestMachineAccess_FullSafeguardsRoundTrip(t *testing.T) {
	for _, writes := range []bool{false, true} {
		for _, ddl := range []bool{false, true} {
			for _, readOnly := range []bool{false, true} {
				for _, history := range []bool{false, true} {
					fs := newConnectionsTestFs()
					want := models.Connection{Name: "dev", CLIAccess: models.CLIAccessFull, ConfirmWrites: writes, ConfirmDDL: ddl, ReadOnly: readOnly, CLIHistory: history}
					if err := SaveConnections(fs, "/c.yml", []models.Connection{want}); err != nil {
						t.Fatal(err)
					}
					got, err := LoadConnections(fs, "/c.yml")
					if err != nil || len(got) != 1 || !reflect.DeepEqual(want, got[0]) {
						t.Fatalf("full policy safeguards: want %+v, got %v, %v", want, got, err)
					}
				}
			}
		}
	}
}

type changingConnectionFs struct {
	afero.Fs
	change func()
}

func (fs *changingConnectionFs) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	if flag&os.O_CREATE != 0 && fs.change != nil {
		change := fs.change
		fs.change = nil
		change()
	}
	return fs.Fs.OpenFile(name, flag, mode)
}

func TestMachineAccess_FormPreservation_Conflicts(t *testing.T) {
	for _, name := range []string{"dev", "renamed"} {
		t.Run(name, func(t *testing.T) {
			base := newConnectionsTestFs()
			if err := SaveConnections(base, "/private/c.yml", []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessRowRead}}); err != nil {
				t.Fatal(err)
			}
			revoked := []byte("connections:\n  - name: dev\n    cli_access: disabled\n")
			fs := &changingConnectionFs{Fs: base, change: func() {
				if err := afero.WriteFile(base, "/private/c.yml", revoked, 0o600); err != nil {
					t.Fatal(err)
				}
			}}
			err := UpdateConnection(fs, "/private/c.yml", "dev", models.Connection{Name: name, Label: "stale", CLIAccess: models.CLIAccessRowRead})
			if !errors.Is(err, ErrConnectionsChanged) {
				t.Fatalf("conflict: %v", err)
			}
			data, _ := afero.ReadFile(base, "/private/c.yml")
			if !bytes.Equal(data, revoked) {
				t.Fatal("intervening revocation overwritten")
			}
			entries, err := afero.ReadDir(base, "/private")
			if err != nil || len(entries) != 1 || entries[0].Name() != "c.yml" {
				t.Fatalf("temporary file leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestMachineAccess_FormPreservation_DuplicateAndDenied(t *testing.T) {
	base := newConnectionsTestFs()
	conns := []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessRowRead}, {Name: "dev", CLIAccess: models.CLIAccessDisabled}}
	if err := SaveConnections(base, "/private/c.yml", conns); err != nil {
		t.Fatal(err)
	}
	before, _ := afero.ReadFile(base, "/private/c.yml")
	if err := UpdateConnection(base, "/private/c.yml", "dev", models.Connection{Name: "dev"}); !errors.Is(err, ErrDuplicateConnectionName) {
		t.Fatalf("ambiguous profile: %v", err)
	}
	readOnly := &connectionsTestFs{Fs: afero.NewReadOnlyFs(base), lock: base.lock}
	if err := SaveConnections(readOnly, "/private/c.yml", nil); err == nil || !strings.Contains(err.Error(), "atomic_yaml: mkdir") {
		t.Fatalf("read-only publication failure not exercised: %v", err)
	}
	after, _ := afero.ReadFile(base, "/private/c.yml")
	if !bytes.Equal(before, after) {
		t.Fatal("rejected write changed profiles")
	}
}
