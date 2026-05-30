package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// seedConns writes conns to path and fails the test on error.
func seedConns(t *testing.T, fs afero.Fs, path string, conns []models.Connection) {
	t.Helper()
	if err := SaveConnections(fs, path, conns); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}
}

// assertMode0600 stats path and asserts its permission bits are exactly 0600.
func assertMode0600(t *testing.T, fs afero.Fs, path string) {
	t.Helper()
	info, err := fs.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o; want 0600", perm)
	}
}

func TestUpdateConnection_RenameCollisionRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedConns(t, fs, "/c.yml", []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
		{Name: "prod", Driver: "postgres", DSN: "postgres://localhost/prod"},
	})
	// Rename dev -> prod collides with the existing prod entry.
	err := UpdateConnection(fs, "/c.yml", "dev",
		models.Connection{Name: "prod", Driver: "postgres", DSN: "postgres://localhost/x"})
	if !errors.Is(err, ErrDuplicateConnectionName) {
		t.Fatalf("err = %v; want ErrDuplicateConnectionName", err)
	}
	// File must be unchanged.
	got, _ := LoadConnections(fs, "/c.yml")
	if len(got) != 2 || got[0].Name != "dev" || got[0].DSN != "postgres://localhost/dev" {
		t.Errorf("file mutated after rejected collision: %+v", got)
	}
}

func TestUpdateConnection_RenameToSelf(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedConns(t, fs, "/c.yml", []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
		{Name: "prod", Driver: "postgres", DSN: "postgres://localhost/prod"},
	})
	// Same name, changed DSN — must succeed.
	err := UpdateConnection(fs, "/c.yml", "dev",
		models.Connection{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/changed"})
	if err != nil {
		t.Fatalf("UpdateConnection: %v", err)
	}
	got, _ := LoadConnections(fs, "/c.yml")
	if len(got) != 2 || got[0].Name != "dev" || got[0].DSN != "postgres://localhost/changed" {
		t.Errorf("update not applied: %+v", got)
	}
	assertMode0600(t, fs, "/c.yml")
}

func TestUpdateConnection_OldNameMissing(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedConns(t, fs, "/c.yml", []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
	})
	err := UpdateConnection(fs, "/c.yml", "nope",
		models.Connection{Name: "nope2", Driver: "postgres", DSN: "postgres://x"})
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("err = %v; want ErrConnectionNotFound", err)
	}
}

func TestUpdateConnection_NeighborRoundTripsAll18Fields(t *testing.T) {
	withWarnWriter(t) // neighbor carries an inline Password; mute the WARN line
	fs := afero.NewMemMapFs()
	neighbor := models.Connection{
		Name:            "fullyloaded",
		Driver:          "postgres",
		DSN:             "postgres://localhost/full",
		Password:        "hunter2",
		PasswordCommand: "pass show db",
		KeyringRef:      "service/account",
		PgpassPath:      "/home/u/.pgpass",
		SSHTunnel: &models.SSHTunnelConfig{
			Host:         "bastion.example.com",
			User:         "jump",
			Port:         2222,
			IdentityFile: "/home/u/.ssh/id_ed25519",
		},
		Tags:             []string{"prod", "critical"},
		Color:            "#ff0000",
		Label:            "Full Production",
		Icon:             "database",
		ReadOnly:         true,
		ConfirmWrites:    true,
		ConfirmDDL:       true,
		StatementTimeout: "30s",
		HiddenSchemas:    []string{"pg_catalog", "information_schema"},
		Role:             "readonly_role",
	}
	target := models.Connection{Name: "target", Driver: "postgres", DSN: "postgres://localhost/before"}
	seedConns(t, fs, "/c.yml", []models.Connection{neighbor, target})

	// Update the DIFFERENT (target) entry; neighbor must survive untouched.
	err := UpdateConnection(fs, "/c.yml", "target",
		models.Connection{Name: "target", Driver: "postgres", DSN: "postgres://localhost/after"})
	if err != nil {
		t.Fatalf("UpdateConnection: %v", err)
	}

	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("LoadConnections: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	gotNeighbor := got[0]
	if gotNeighbor.Name != "fullyloaded" {
		t.Fatalf("neighbor order changed: got[0].Name = %q", gotNeighbor.Name)
	}
	// All 18 fields must equal the original neighbor (deep-equal via reflect).
	if !reflect.DeepEqual(neighbor, gotNeighbor) {
		t.Errorf("neighbor not preserved.\n want %+v\n  got %+v", neighbor, gotNeighbor)
	}
	if got[1].DSN != "postgres://localhost/after" {
		t.Errorf("target update not applied: %+v", got[1])
	}
	assertMode0600(t, fs, "/c.yml")
}

func TestDeleteConnection_Existing(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedConns(t, fs, "/c.yml", []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
		{Name: "prod", Driver: "postgres", DSN: "postgres://localhost/prod"},
	})
	if err := DeleteConnection(fs, "/c.yml", "dev"); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	got, _ := LoadConnections(fs, "/c.yml")
	if len(got) != 1 || got[0].Name != "prod" {
		t.Errorf("after delete = %+v; want only 'prod'", got)
	}
	assertMode0600(t, fs, "/c.yml")
}

func TestDeleteConnection_Missing(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedConns(t, fs, "/c.yml", []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
	})
	err := DeleteConnection(fs, "/c.yml", "nope")
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("err = %v; want ErrConnectionNotFound", err)
	}
	got, _ := LoadConnections(fs, "/c.yml")
	if len(got) != 1 {
		t.Errorf("file mutated after missing delete: %+v", got)
	}
}

func TestDeleteConnection_LastEntryYieldsEmptyList(t *testing.T) {
	fs := afero.NewMemMapFs()
	seedConns(t, fs, "/c.yml", []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost/dev"},
	})
	if err := DeleteConnection(fs, "/c.yml", "dev"); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	// On-disk YAML must be the empty-list wrapper, not a null/omitted key.
	data, err := afero.ReadFile(fs, "/c.yml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "connections: []") {
		t.Errorf("YAML = %q; want it to contain 'connections: []'", string(data))
	}
	got, err := LoadConnections(fs, "/c.yml")
	if err != nil {
		t.Fatalf("LoadConnections after empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("loaded = %+v; want empty", got)
	}
	assertMode0600(t, fs, "/c.yml")
}
