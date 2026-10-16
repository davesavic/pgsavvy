//go:build linux

package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineQuery_Policy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sql")
	if err := os.WriteFile(path, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{}, {"--connection", "x"}, {"--connection", "x", "--file", path, "--max-rows", "0"}, {"--connection", "x", "--file", path, "--max-rows", "1001"}, {"--connection", "x", "--file", path, "--bad"}, {"--connection", "x", "--file", path, "--params-file", "-"}, {"--connection", "x", "--file", path, "--file", path}} {
		loaded := false
		service := InvocationService{Load: func() ([]models.Connection, error) { loaded = true; return nil, nil }}
		if _, err := QueryCommand(service)(context.Background(), args, IO{}); err == nil || loaded {
			t.Fatalf("invalid grammar opened profiles: %v", args)
		}
	}
	if err := os.WriteFile(path, []byte("DELETE FROM secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := false
	if _, err := QueryCommand(InvocationService{Load: func() ([]models.Connection, error) { loaded = true; return nil, nil }})(context.Background(), []string{"--connection", "x", "--file", path}, IO{}); err == nil || loaded {
		t.Fatal("modifying AST reached loader")
	}
	if _, err := QueryCommand(InvocationService{})(context.Background(), []string{"--help"}, IO{}); err != nil {
		t.Fatal("query JSON help required a connection")
	}
}

func TestMachineQuery_Policy_StaleFormRevocation(t *testing.T) {
	fs := afero.NewMemMapFs()
	profile := models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessRowRead, PasswordCommand: "must-not-run"}
	stale := profile
	if err := config.SaveConnections(fs, "/c.yml", []models.Connection{profile}); err != nil {
		t.Fatal(err)
	}
	profile.CLIAccess = ""
	if err := config.SaveConnections(fs, "/c.yml", []models.Connection{profile}); err != nil {
		t.Fatal(err)
	}
	stale.Label = "edited"
	if err := config.UpdateConnection(fs, "/c.yml", "dev", stale); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sql")
	if err := os.WriteFile(file, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed := false
	service := InvocationService{Load: func() ([]models.Connection, error) { return LoadProfiles(fs, "/c.yml") }, Options: pg.MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { parsed = true; return nil, nil }}}
	streams, out, _ := testStreams()
	_, exit := Dispatch(context.Background(), []string{"query", "--connection", "dev", "--file", file}, streams, map[string]Command{"query": QueryCommand(service)})
	if exit != ExitPolicy || parsed || out.Len() == 0 {
		t.Fatalf("stale save restored query access: exit%d parsed%v", exit, parsed)
	}
}
