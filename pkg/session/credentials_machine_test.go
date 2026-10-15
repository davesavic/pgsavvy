//go:build linux

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func machineCredentialFixture(t *testing.T) (*pgconn.Config, string) {
	t.Helper()
	dir := t.TempDir()
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "PG") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("HOME", dir)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, filepath.Join(dir, name))
	}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(dir, "no-agent"))
	t.Setenv("PGSERVICEFILE", filepath.Join(dir, "no-service"))
	t.Setenv("PGPASSFILE", filepath.Join(dir, "no-pass"))
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("ENV", "")
	t.Setenv("BASH_ENV", "")
	return &pgconn.Config{Host: "host", Port: 5432, Database: "db", User: "user", Password: "parsed"}, dir
}

func TestMachineCredentials_Precedence(t *testing.T) {
	target, dir := machineCredentialFixture(t)
	path := filepath.Join(dir, "pgpass")
	if err := os.WriteFile(path, []byte("host:5432:db:user:pgpass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		inline, helper, keyring, passfile, want string
		execs, keys                             int
	}{
		{"inline", "helper", "keyring", path, "inline", 0, 0},
		{"", "helper", "keyring", path, "helper", 1, 0},
		{"", "", "keyring", path, "keyring", 1, 1},
		{"", "", "", path, "pgpass", 1, 1},
		{"", "", "", "", "parsed", 1, 1},
	} {
		execs, keys := 0, 0
		profile := models.Connection{Password: tc.inline, PasswordCommand: "trusted", KeyringRef: "ref", PgpassPath: tc.passfile}
		password, err := ResolveMachinePassword(context.Background(), profile, target, MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { execs++; return tc.helper, nil }, Keyring: func(context.Context, string) (string, error) { keys++; return tc.keyring, nil }})
		if err != nil || password != tc.want || execs != tc.execs || keys != tc.keys {
			t.Fatalf("precedence: %q %v exec=%d key=%d want=%+v", password, err, execs, keys, tc)
		}
	}
	for _, kind := range []string{"helper", "keyring"} {
		options := MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { return "", errors.New("SECRET COMMAND OUTPUT") }, Keyring: func(context.Context, string) (string, error) { return "", errors.New("SECRET KEYRING") }}
		profile := models.Connection{KeyringRef: "ref"}
		if kind == "helper" {
			profile.PasswordCommand = "command"
		}
		_, err := ResolveMachinePassword(context.Background(), profile, target, options)
		var safe *MachineCredentialError
		if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe %s failure: %v", kind, err)
		}
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			password, err := ResolveMachinePassword(context.Background(), models.Connection{Password: "isolated"}, target, MachineCredentialOptions{})
			if err != nil || password != "isolated" {
				t.Error("concurrent resolution failed")
			}
		})
	}
	wg.Wait()
}

func TestMachineCredentials_NoPrompt(t *testing.T) {
	target, dir := machineCredentialFixture(t)
	target.Password = ""
	password, err := ResolveMachinePassword(context.Background(), models.Connection{}, target, MachineCredentialOptions{})
	if err != nil || password != "" {
		t.Fatalf("passwordless fallback: %q %v", password, err)
	}
	withXDGDataHome(t, dir)
	seedKeyring(t, keyringDir(), "passphrase", "key", "value")
	_, err = ResolveMachinePassword(context.Background(), models.Connection{KeyringRef: "key"}, target, MachineCredentialOptions{})
	var safe *MachineCredentialError
	if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable {
		t.Fatalf("missing keyring passphrase prompted/succeeded: %v", err)
	}
	t.Setenv(keyringPassphraseEnv, "passphrase")
	password, err = ResolveMachinePassword(context.Background(), models.Connection{KeyringRef: "key"}, target, MachineCredentialOptions{})
	if err != nil || password != "value" {
		t.Fatalf("noninteractive keyring: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancel()
	if _, err := ResolveMachinePassword(ctx, models.Connection{Password: "inline"}, target, MachineCredentialOptions{}); err == nil {
		t.Fatal("precancelled invocation succeeded")
	}
}

func TestMachineCredentials_EffectiveTarget(t *testing.T) {
	_, dir := machineCredentialFixture(t)
	path := filepath.Join(dir, "explicit-pass")
	if err := os.WriteFile(path, []byte("effective:6543:actual:real:password\nlocalhost:5432:actual:real:socket\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dsn := range []string{"postgres://real@effective:6543/actual?sslmode=disable", "host=effective port=6543 dbname=actual user=real sslmode=disable", "host=/tmp/synthetic-pg port=5432 dbname=actual user=real sslmode=disable"} {
		target, err := pgconn.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		profile := models.Connection{DSN: dsn, Host: "ignored", User: "ignored", Database: "ignored", Port: 1, PgpassPath: path}
		password, err := ResolveMachinePassword(context.Background(), profile, target, MachineCredentialOptions{})
		want := "password"
		if strings.Contains(dsn, "/tmp/") {
			want = "socket"
		}
		if err != nil || password != want {
			t.Fatalf("effective target %q: %q %v", dsn, password, err)
		}
	}
	for _, contents := range []string{"first:5432:db:user:one\nsecond:5432:db:user:two\n", "first:5432:db:user:one\nsecond:5432:db:user:one\n", "*:5432:db:user:same\nfirst:5432:db:user:ignored\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		target, err := pgconn.ParseConfig("host=first,second port=5432 user=user dbname=db sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		password, err := ResolveMachinePassword(context.Background(), models.Connection{PgpassPath: path}, target, MachineCredentialOptions{})
		if strings.Contains(contents, "two") {
			var safe *MachineCredentialError
			if !errors.As(err, &safe) || safe.Kind != MachinePgpassFailover {
				t.Fatalf("different passwords accepted: %v", err)
			}
		} else if err != nil || (password != "one" && password != "same") {
			t.Fatalf("consistent failover: %q %v", password, err)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMachinePassword(context.Background(), models.Connection{PgpassPath: path}, &pgconn.Config{}, MachineCredentialOptions{}); err == nil {
		t.Fatal("insecure file accepted")
	}
	for _, path := range []string{dir, filepath.Join(dir, "missing")} {
		if _, err := ResolveMachinePassword(context.Background(), models.Connection{PgpassPath: path}, &pgconn.Config{}, MachineCredentialOptions{}); err == nil {
			t.Fatal("unavailable pgpass accepted")
		}
	}
}

func TestMachineCredentials_EffectiveTarget_ParsedFallbacks(t *testing.T) {
	_, dir := machineCredentialFixture(t)
	path := filepath.Join(dir, ".pgpass")
	if err := os.WriteFile(path, []byte("effective:6543:actual:real:passfile-password\nsecond:6543:actual:real:different\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"dsn", "environment", "pgpass", "defaults", "implicit-failover"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("PGPASSWORD", "")
			t.Setenv("PGPASSFILE", filepath.Join(dir, "no-pass"))
			dsn := "host=effective port=6543 user=real dbname=actual sslmode=disable"
			want := ""
			switch source {
			case "dsn":
				dsn = "postgres://real:dsn-password@effective:6543/actual?sslmode=disable"
				want = "dsn-password"
			case "environment":
				t.Setenv("PGPASSWORD", "env-password")
				want = "env-password"
			case "pgpass":
				t.Setenv("PGPASSFILE", path)
				want = "passfile-password"
			case "defaults":
				t.Setenv("PGHOST", "effective")
				t.Setenv("PGPORT", "6543")
				t.Setenv("PGUSER", "real")
				t.Setenv("PGDATABASE", "actual")
				// pgx uses the OS-account home, not HOME; keep implicit passfile
				// discovery on a synthetic file instead of real user credentials.
				t.Setenv("PGPASSFILE", path)
				dsn = "sslmode=disable"
				want = "passfile-password"
			case "implicit-failover":
				t.Setenv("PGPASSFILE", path)
				dsn = "host=effective,second port=6543 user=real dbname=actual sslmode=disable"
				want = "passfile-password"
			}
			target, err := pgconn.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ResolveMachinePassword(context.Background(), models.Connection{}, target, MachineCredentialOptions{})
			if err != nil || got != want || target.Host != "effective" || target.Port != 6543 || target.Database != "actual" || target.User != "real" {
				t.Fatalf("parsed %s fallback: %q %v", source, got, err)
			}
		})
	}
}
