//go:build linux || darwin || windows

package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

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
		if cfg.Tracer != nil || cfg.OnNotice != nil || cfg.RuntimeParams["client_encoding"] != "UTF8" {
			t.Fatal("machine defaults missing")
		}
		if _, present := cfg.RuntimeParams["default_transaction_read_only"]; present {
			t.Fatal("machine read-only setup ran before target selection")
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

func TestMachineConfig_TargetSelection(t *testing.T) {
	for _, tc := range []struct {
		name, dsn, env, envOptions, startup, options string
		readOnly, validate                           bool
	}{
		{name: "absent", dsn: "host=first sslmode=disable"},
		{name: "read_only_profile", dsn: "host=first sslmode=disable", readOnly: true},
		{name: "url_read_write", dsn: "postgres://u@first/d?sslmode=disable&target_session_attrs=read-write", validate: true},
		{name: "keyword_read_write", dsn: "host=first sslmode=disable target_session_attrs=read-write", validate: true},
		{name: "environment_read_write", dsn: "host=first sslmode=disable", env: "read-write", validate: true},
		{name: "explicit_over_environment", dsn: "host=first sslmode=disable target_session_attrs=read-write", env: "read-only", validate: true},
		{name: "explicit_any_over_environment", dsn: "host=first sslmode=disable target_session_attrs=any default_transaction_read_only=on", env: "read-write", startup: "on"},
		{name: "url_startup_on", dsn: "postgres://u@first/d?sslmode=disable&target_session_attrs=read-write&default_transaction_read_only=on", startup: "on", validate: true},
		{name: "keyword_startup_off", dsn: "host=first sslmode=disable target_session_attrs=read-write default_transaction_read_only=off", startup: "off", validate: true},
		{name: "pgoptions", dsn: "host=first sslmode=disable target_session_attrs=read-write", envOptions: "-c default_transaction_read_only=on", options: "-c default_transaction_read_only=on", validate: true},
		{name: "explicit_options", dsn: "host=first sslmode=disable target_session_attrs=read-write options='-c default_transaction_read_only=off'", envOptions: "-c default_transaction_read_only=on", options: "-c default_transaction_read_only=off", validate: true},
		{name: "service_read_write", dsn: "service=fixture", env: "read-only", startup: "off", validate: true},
		{name: "explicit_any_over_service", dsn: "service=fixture target_session_attrs=any", env: "read-write", startup: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := machineProfileFixture(t)
			service := filepath.Join(dir, "services")
			if err := os.WriteFile(service, []byte("[fixture]\nhost=first\nuser=u\ndbname=d\nsslmode=disable\ntarget_session_attrs=read-write\ndefault_transaction_read_only=off\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PGSERVICEFILE", service)
			t.Setenv("PGTARGETSESSIONATTRS", tc.env)
			t.Setenv("PGOPTIONS", tc.envOptions)
			profile := models.Connection{DSN: tc.dsn, ReadOnly: tc.readOnly, StatementTimeout: "30s"}
			cfg, err := BuildMachineConfig(context.Background(), profile)
			if err != nil {
				t.Fatal(err)
			}
			if (cfg.ValidateConnect != nil) != tc.validate {
				t.Fatal("original target validator presence changed")
			}
			startup, present := cfg.RuntimeParams["default_transaction_read_only"]
			if startup != tc.startup || present != (tc.startup != "") || cfg.RuntimeParams["options"] != tc.options {
				t.Fatal("user startup settings or absence changed")
			}
			if cfg.Host != "first" || cfg.Tracer != nil || cfg.OnNotice != nil || cfg.OnNotification != nil || cfg.DefaultQueryExecMode != pgx.QueryExecModeExec {
				t.Fatal("target, callbacks or query mode changed")
			}
			for name, want := range map[string]string{"application_name": "pgsavvy-machine", "client_encoding": "UTF8", "DateStyle": "ISO", "TimeZone": "UTC", "IntervalStyle": "postgres", "bytea_output": "hex", "extra_float_digits": "3", "statement_timeout": "10000", "lock_timeout": "1000"} {
				if cfg.RuntimeParams[name] != want {
					t.Errorf("machine default %s changed", name)
				}
			}
		})
	}
	t.Run("fallback_snapshot", func(t *testing.T) {
		machineProfileFixture(t)
		profile := models.Connection{DSN: "host=first,second port=5432,6543 user=u dbname=d sslmode=prefer target_session_attrs=read-write search_path=custom options='-c default_transaction_read_only=off'"}
		cfg, err := BuildMachineConfig(context.Background(), profile)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ValidateConnect == nil || cfg.Host != "first" || cfg.Port != 5432 || cfg.TLSConfig == nil || len(cfg.Fallbacks) != 3 || cfg.Fallbacks[0].Host != "first" || cfg.Fallbacks[0].Port != 5432 || cfg.Fallbacks[0].TLSConfig != nil || cfg.Fallbacks[1].Host != "second" || cfg.Fallbacks[1].Port != 6543 || cfg.Fallbacks[1].TLSConfig == nil || cfg.Fallbacks[2].Host != "second" || cfg.Fallbacks[2].Port != 6543 || cfg.Fallbacks[2].TLSConfig != nil {
			t.Fatal("read-write selector or ordered TLS/endpoint fallbacks changed")
		}
		copy := cfg.Copy()
		copy.RuntimeParams["search_path"] = "changed"
		copy.RuntimeParams["options"] = "changed"
		copy.Fallbacks[1].Host = "changed"
		if cfg.RuntimeParams["search_path"] != "custom" || cfg.RuntimeParams["options"] != "-c default_transaction_read_only=off" || cfg.Fallbacks[1].Host != "second" {
			t.Fatal("parsed user settings or fallback copies share mutable state")
		}
	})
}
