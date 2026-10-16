//go:build linux || darwin || windows

package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineOpen_TargetSnapshot(t *testing.T) {
	dir := machineProfileFixture(t)
	service := filepath.Join(dir, "services")
	if err := os.WriteFile(service, []byte("[fixture]\nhost=service-host\nport=6543\nuser=service-user\ndbname=service-db\nsslmode=disable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", service)
	for _, profile := range []models.Connection{
		{DSN: "host=first,second port=5432,6543 user=u dbname=d sslmode=disable"},
		{DSN: "postgres://u@first:6543/d?sslmode=disable", Host: "ignored"},
		{DSN: "service=fixture"},
		{Host: "/tmp/synthetic-pg", User: "u", Database: "d", SSLMode: "disable"},
	} {
		cfg, err := BuildMachineConfig(context.Background(), profile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Tracer != nil || cfg.OnNotice != nil || cfg.RuntimeParams["default_transaction_read_only"] != "on" || cfg.RuntimeParams["client_encoding"] != "UTF8" {
			t.Fatal("machine defaults missing")
		}
		if profile.Host == "ignored" && cfg.Host != "first" {
			t.Fatal("discrete fields overrode DSN")
		}
		if profile.DSN == "service=fixture" && (cfg.Host != "service-host" || cfg.Port != 6543 || cfg.User != "service-user" || cfg.Database != "service-db") {
			t.Fatal("service defaults lost")
		}
	}
	for raw, want := range map[string]int{"": 10000, "0": 10000, "0ms": 10000, "1ms": 1, "5s": 5000, "30s": 10000, "99d": 10000, "99999999999999999999999s": 10000} {
		if got := MachineStatementMilliseconds(raw); got != want {
			t.Fatalf("timeout %s: %d want%d", raw, got, want)
		}
	}
	if _, err := BuildMachineConfig(context.Background(), models.Connection{Host: "localhost", StatementTimeout: "1s; SECRET"}); err == nil {
		t.Fatal("invalid timeout accepted")
	}
}
