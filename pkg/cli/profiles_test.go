//go:build linux

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/spf13/afero"
)

func TestMachineProfiles_Allowlist(t *testing.T) {
	profile := models.Connection{Name: "dev", Driver: "postgres", Tags: []string{"tag"}, Label: "label", DSN: "SECRET", Host: "SECRET", User: "SECRET", Password: "SECRET", PasswordCommand: "SECRET", KeyringRef: "SECRET", PgpassPath: "SECRET", SSHTunnel: &models.SSHTunnelConfig{Host: "SECRET"}}
	public := PublicConnection(profile)
	public.Tags[0] = "copy"
	encoded, err := json.Marshal(public)
	if err != nil || strings.Contains(string(encoded), "SECRET") || profile.Tags[0] != "tag" {
		t.Fatal("profile serialization leaked or shares caller data")
	}
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	if len(fields) != 7 || fields["credential_status"] != "not_checked" || fields["cli_access"] != "disabled" {
		t.Fatal("profile allowlist changed")
	}
	service := InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{profile}, nil }}
	for _, args := range [][]string{{"list"}, {"show", "dev"}} {
		envelope, err := ConnectionsCommand(service)(context.Background(), args, IO{})
		if err != nil || !envelope.ResultComplete {
			t.Fatal("offline discovery attempted credentials")
		}
	}
	service.Load = func() ([]models.Connection, error) { return nil, nil }
	envelope, err := ConnectionsCommand(service)(context.Background(), []string{"list"}, IO{})
	if err != nil || envelope.Data.([]PublicProfile) == nil {
		t.Fatal("offline empty list lost array")
	}
}

func TestMachineProfiles_RegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "connections.yml")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(afero.NewOsFs(), fifo); err == nil {
		t.Fatal("profile FIFO accepted")
	}
	if _, err := LoadProfiles(afero.NewOsFs(), dir); err == nil {
		t.Fatal("profile directory accepted")
	}
	if profiles, err := LoadProfiles(afero.NewOsFs(), filepath.Join(dir, "missing")); err != nil || profiles == nil || len(profiles) != 0 {
		t.Fatal("offline missing config did not stay empty")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("profile loading created files")
	}
}
