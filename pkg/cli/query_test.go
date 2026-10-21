//go:build linux || darwin

package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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
		loaded, metadata := false, false
		service := InvocationService{
			Load: func() ([]models.Connection, error) { loaded = true; return nil, nil },
			historyLookup: func(context.Context, string) (models.Connection, error) {
				metadata = true
				return models.Connection{Name: "x"}, nil
			},
		}
		if _, err := QueryCommand(service)(context.Background(), args, IO{}); err == nil || loaded || metadata != slices.Contains(args, "--params-file") {
			t.Fatalf("invalid grammar opened profiles: %v", args)
		}
	}
	if err := os.WriteFile(path, []byte("DELETE FROM secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, metadata, parsed := false, false, false
	service := InvocationService{
		Load: func() ([]models.Connection, error) { loaded = true; return nil, nil },
		historyLookup: func(context.Context, string) (models.Connection, error) {
			metadata = true
			return models.Connection{Name: "x"}, nil
		},
		Options: pg.MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { parsed = true; return nil, nil }},
	}
	if _, err := QueryCommand(service)(context.Background(), []string{"--connection", "x", "--file", path}, IO{}); err == nil || loaded || parsed || !metadata || safeError(err).Code != UnsupportedSQL {
		t.Fatal("rejected SQL must allow metadata only, never execution/credential loading")
	}
	if _, err := QueryCommand(InvocationService{})(context.Background(), []string{"--help"}, IO{}); err != nil {
		t.Fatal("query JSON help required a connection")
	}
}

func TestMachineQuery_Policy_StaleFormRevocation(t *testing.T) {
	fs := afero.NewOsFs()
	profilesPath := filepath.Join(t.TempDir(), "connections.yml")
	profile := models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessRowRead, PasswordCommand: "must-not-run"}
	stale := profile
	if err := config.SaveConnections(fs, profilesPath, []models.Connection{profile}); err != nil {
		t.Fatal(err)
	}
	profile.CLIAccess = ""
	if err := config.SaveConnections(fs, profilesPath, []models.Connection{profile}); err != nil {
		t.Fatal(err)
	}
	stale.Label = "edited"
	if err := config.UpdateConnection(fs, profilesPath, "dev", stale); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sql")
	if err := os.WriteFile(file, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed := false
	service := InvocationService{Load: func() ([]models.Connection, error) { return LoadProfiles(fs, profilesPath) }, Options: pg.MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { parsed = true; return nil, nil }}}
	streams, out, _ := testStreams()
	_, exit := Dispatch(context.Background(), []string{"query", "--connection", "dev", "--file", file}, streams, map[string]Command{"query": QueryCommand(service)})
	if exit != ExitPolicy || parsed || out.Len() == 0 {
		t.Fatalf("stale save restored query access: exit%d parsed%v", exit, parsed)
	}
}
