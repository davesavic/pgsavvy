//go:build linux || darwin

package cli

import (
	"context"
	"runtime"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jackc/pgx/v5"
)

func TestMachineConnections_Grammar(t *testing.T) {
	for _, args := range [][]string{nil, {"missing"}, {"list", "extra"}, {"show"}, {"test", "--connection", "dev"}, {"test", "a", "b"}, {"list", "--bad"}, {"test", "--help", "extra"}} {
		loaded := false
		_, err := ConnectionsCommand(InvocationService{Load: func() ([]models.Connection, error) { loaded = true; return nil, nil }})(context.Background(), args, IO{})
		if err == nil || loaded {
			t.Fatalf("invalid grammar loaded profiles: %v", args)
		}
	}
	for _, args := range [][]string{{"--help"}, {"list", "--help"}, {"show", "--help"}, {"test", "--help"}} {
		if _, err := ConnectionsCommand(InvocationService{})(context.Background(), args, IO{}); err != nil {
			t.Fatal("offline help failed")
		}
	}
	for _, profiles := range [][]models.Connection{nil, {{Name: "dev"}}, {{Name: "dev", CLIAccess: models.CLIAccessMetadata}, {Name: "dev", CLIAccess: models.CLIAccessRowRead}}} {
		parsed := false
		service := InvocationService{Load: func() ([]models.Connection, error) { return profiles, nil }, Options: pg.MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { parsed = true; return nil, nil }}}
		if _, err := ConnectionsCommand(service)(context.Background(), []string{"test", "dev"}, IO{}); err == nil || parsed {
			t.Fatal("disabled/ambiguous/missing connection parsed secrets")
		}
	}
	service := InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{{Name: "-dev"}}, nil }}
	streams, out, _ := testStreams()
	_, exit := Dispatch(context.Background(), []string{"connections", "show", "--", "-dev"}, streams, map[string]Command{"connections": ConnectionsCommand(service)})
	if exit != 0 || out.Len() == 0 {
		t.Fatal("literal dash-prefixed profile selector failed")
	}
}

func TestMachineConnections_OfflineProfiles(t *testing.T) {
	profile := models.Connection{Name: "dev", Driver: "postgres", Tags: []string{"tag"}, Label: "label", DSN: "SECRET", Host: "SECRET", User: "SECRET", Password: "SECRET", PasswordCommand: "SECRET", KeyringRef: "SECRET", PgpassPath: "SECRET", SSHTunnel: &models.SSHTunnelConfig{Host: "SECRET"}}
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

func TestMachineConnections_Capabilities(t *testing.T) {
	envelope, err := CapabilitiesCommand()(context.Background(), nil, IO{})
	if err != nil {
		t.Fatal(err)
	}
	data := envelope.Data.(map[string]any)
	commands := data["commands"].([]capabilityCommand)
	if len(commands) != 9 || data["platform"] != runtime.GOOS || data["catalog_visibility_implies_row_access"] != false {
		t.Fatal("capabilities do not describe production scope")
	}
	seen := map[string]bool{}
	for _, command := range commands {
		if seen[command.Command] || command.Usage == "" {
			t.Fatal("invalid capability grammar")
		}
		seen[command.Command] = true
	}
	for _, name := range []string{"connections list", "connections show", "connections test", "schema list", "schema tables", "schema describe", "schema relationships", "query", "capabilities"} {
		if !seen[name] {
			t.Fatal("missing implemented capability")
		}
	}
	if _, err := CapabilitiesCommand()(context.Background(), []string{"--bad"}, IO{}); err == nil {
		t.Fatal("unknown capability flag accepted")
	}
}
