//go:build linux || darwin

package session

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func temporaryEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range temporaryPostgresEnvironment {
		t.Setenv(name, "")
	}
}

func temporaryFields() TemporaryTargetFields {
	return TemporaryTargetFields{Port: "5432", Database: "database", Username: "username", Passfile: "/private/SECRET-passfile"}
}

func TestTemporaryTarget_Validation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*TemporaryTargetFields)
	}{
		{"missing-port", func(f *TemporaryTargetFields) { f.Port = "" }},
		{"zero-port", func(f *TemporaryTargetFields) { f.Port = "0" }},
		{"large-port", func(f *TemporaryTargetFields) { f.Port = "65536" }},
		{"signed-port", func(f *TemporaryTargetFields) { f.Port = "+5432" }},
		{"space-port", func(f *TemporaryTargetFields) { f.Port = " 5432" }},
		{"decimal-only", func(f *TemporaryTargetFields) { f.Port = "54.32" }},
		{"missing-database", func(f *TemporaryTargetFields) { f.Database = "" }},
		{"missing-username", func(f *TemporaryTargetFields) { f.Username = "" }},
		{"missing-passfile", func(f *TemporaryTargetFields) { f.Passfile = "" }},
		{"remote", func(f *TemporaryTargetFields) { f.Host = "192.0.2.1" }},
		{"hostname", func(f *TemporaryTargetFields) { f.Host = "localhost" }},
		{"dns", func(f *TemporaryTargetFields) { f.Host = "example.test" }},
		{"dsn", func(f *TemporaryTargetFields) { f.Host = "postgres://user@127.0.0.1/db" }},
		{"multi", func(f *TemporaryTargetFields) { f.Host = "127.0.0.1,::1" }},
		{"zone", func(f *TemporaryTargetFields) { f.Host = "::1%lo" }},
		{"brackets", func(f *TemporaryTargetFields) { f.Host = "[::1]" }},
		{"host-port", func(f *TemporaryTargetFields) { f.Host = "127.0.0.1:5432" }},
		{"mixed", func(f *TemporaryTargetFields) { f.Host, f.SocketDir = "127.0.0.1", "/tmp/socket" }},
		{"relative-socket", func(f *TemporaryTargetFields) { f.SocketDir = "tmp/socket" }},
		{"unclean-socket", func(f *TemporaryTargetFields) { f.SocketDir = "/tmp/../socket" }},
		{"socket-port-required", func(f *TemporaryTargetFields) { f.SocketDir, f.Port = "/tmp/socket", "" }},
		{"control", func(f *TemporaryTargetFields) { f.Database = "db\nsecret" }},
		{"unicode-control", func(f *TemporaryTargetFields) { f.Username = "user\u0085" }},
		{"utf8", func(f *TemporaryTargetFields) { f.Username = "user\xff" }},
		{"bound", func(f *TemporaryTargetFields) { f.Database = strings.Repeat("a", 1<<20+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := temporaryFields()
			tc.alter(&fields)
			target, err := NewTemporaryTarget(fields)
			if err == nil || err.Error() != "INVALID_INPUT" || target.Validate() == nil {
				t.Fatalf("invalid target accepted or unsafe error: %v", err)
			}
		})
	}
	for host, want := range map[string]string{"": "127.0.0.1", "127.1.2.3": "127.1.2.3", "0:0:0:0:0:0:0:1": "::1", "::ffff:127.0.0.1": "127.0.0.1"} {
		fields := temporaryFields()
		fields.Host, fields.Port = host, "0005432"
		target, err := NewTemporaryTarget(fields)
		if err != nil || target.Host() != want || target.Port() != 5432 || target.Database() != fields.Database || target.Username() != fields.Username {
			t.Fatalf("canonical target: %v", err)
		}
		fields.Passfile = "changed"
		if strings.Contains(fmt.Sprintf("%v %+v %#v", target, target, target), "SECRET") || target.passfile != "/private/SECRET-passfile" {
			t.Fatal("target mutation or private formatting leak")
		}
	}
	if (TemporaryTarget{}).Validate() == nil {
		t.Fatal("zero target is executable")
	}
}

func TestTemporaryEnvironment_RejectedBeforeAcquisition(t *testing.T) {
	temporaryEnvironment(t)
	target, err := NewTemporaryTarget(temporaryFields())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range temporaryPostgresEnvironment {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "SECRET")
			parses, opens := 0, 0
			_, err := buildTemporaryMachineConfig(context.Background(), target, func(string) (*pgx.ConnConfig, error) { parses++; return nil, nil })
			_, readErr := resolveTemporaryMachinePassword(context.Background(), target, func(string) (*os.File, error) { opens++; return nil, nil })
			if err == nil || readErr == nil || err.Error() != "INVALID_INPUT" || readErr.Error() != "INVALID_INPUT" || parses != 0 || opens != 0 {
				t.Fatalf("environment reached acquisition: parse=%d open=%d err=%v read=%v", parses, opens, err, readErr)
			}
			if os.Getenv(name) != "SECRET" {
				t.Fatal("environment mutated")
			}
		})
	}
	_, err = buildTemporaryMachineConfig(context.Background(), TemporaryTarget{}, func(string) (*pgx.ConnConfig, error) { t.Fatal("invalid target parsed"); return nil, nil })
	if err == nil {
		t.Fatal("zero target accepted")
	}
}

func TestTemporaryConfig_IsolatedCanonicalBootstrap(t *testing.T) {
	temporaryEnvironment(t)
	for _, socket := range []string{"", "/tmp/synthetic, socket'\\dir"} {
		fields := temporaryFields()
		fields.Database, fields.Username, fields.SocketDir = "db ' \\ /?service=SECRET", "user ' \\ ?", socket
		target, err := NewTemporaryTarget(fields)
		if err != nil {
			t.Fatal(err)
		}
		parses := 0
		cfg, err := buildTemporaryMachineConfig(context.Background(), target, func(connection string) (*pgx.ConnConfig, error) {
			parses++
			if strings.Contains(connection, fields.Passfile) || strings.Contains(connection, "password=") || !strings.Contains(connection, "passfile=/dev/null") || !strings.Contains(connection, "sslmode=disable") {
				t.Fatal("bootstrap contains alternate credentials")
			}
			return pgx.ParseConfig(connection)
		})
		if err != nil || parses != 1 {
			t.Fatalf("bootstrap: %v parses=%d", err, parses)
		}
		wantHost := target.Host()
		if socket != "" {
			wantHost = socket
		}
		wantRuntime := map[string]string{"application_name": "pgsavvy-machine", "client_encoding": "UTF8", "DateStyle": "ISO", "TimeZone": "UTC", "IntervalStyle": "postgres", "bytea_output": "hex", "extra_float_digits": "3", "statement_timeout": "10000", "lock_timeout": "1000", "standard_conforming_strings": "on"}
		if cfg.Host != wantHost || cfg.Port != target.Port() || cfg.User != fields.Username || cfg.Database != fields.Database || cfg.Password != "" || len(cfg.Fallbacks) != 0 || cfg.TLSConfig != nil || cfg.ValidateConnect != nil || cfg.AfterConnect != nil || cfg.KerberosSrvName != "" || cfg.KerberosSpn != "" || !reflect.DeepEqual(cfg.RuntimeParams, wantRuntime) || cfg.DefaultQueryExecMode != pgx.QueryExecModeExec {
			t.Fatal("bootstrap changed selectors or retained ambient influence")
		}
		if _, err := cfg.LookupFunc(context.Background(), "example.test"); err == nil {
			t.Fatal("DNS resolver available")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target, _ := NewTemporaryTarget(temporaryFields())
	_, err := buildTemporaryMachineConfig(ctx, target, func(string) (*pgx.ConnConfig, error) { t.Fatal("cancelled parse"); return nil, nil })
	if err == nil || err.Error() != "TIMEOUT" {
		t.Fatalf("cancelled bootstrap: %v", err)
	}
}
