//go:build linux || darwin

package pg

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func temporaryWireTarget(t *testing.T, fixture *machineWireFixture) session.TemporaryTarget {
	t.Helper()
	_, port, err := net.SplitHostPort(fixture.addr)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "SECRET-passfile")
	if err := os.WriteFile(path, []byte("127.0.0.1:"+port+":database:username:SECRET-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := session.NewTemporaryTarget(session.TemporaryTargetFields{Port: port, Database: "database", Username: "username", Passfile: path})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func temporaryMachineEnvironment(t *testing.T) {
	t.Helper()
	machineTestEnvironment(t)
	// The saved fixture intentionally supplies these ambient paths. Temporary
	// mode rejects nonempty paths rather than ignoring them.
	t.Setenv("PGPASSFILE", "")
	t.Setenv("PGSERVICEFILE", "")
}

func TestTemporaryOpen_RejectsBeforeCredentialAndDial(t *testing.T) {
	temporaryMachineEnvironment(t)
	fixture := newMachineWireFixture(t)
	target := temporaryWireTarget(t, fixture)
	for _, tc := range []struct {
		name    string
		target  session.TemporaryTarget
		options MachineOptions
		env     string
	}{
		{name: "zero-target"},
		{name: "environment", target: target, env: "PGSERVICE"},
		{name: "connect-timeout", target: target, options: MachineOptions{ConnectTimeout: 11 * time.Second}},
		{name: "cleanup-timeout", target: target, options: MachineOptions{CleanupTimeout: -1}},
		{name: "statement-timeout", target: target, options: MachineOptions{StatementTimeout: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(tc.env, "SECRET")
			}
			tc.options.Dial = func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("invalid target dialed")
				return nil, nil
			}
			_, err := openTemporaryMachine(context.Background(), tc.target, tc.options, func(context.Context, session.TemporaryTarget) (string, error) {
				t.Fatal("invalid target acquired credential")
				return "", nil
			})
			if err == nil || err.Error() != "INVALID_INPUT" {
				t.Fatalf("invalid temporary open: %v", err)
			}
		})
	}
	if fixture.connections.Load() != 0 {
		t.Fatal("invalid opening reached server")
	}
}

func TestTemporaryOpen_IsolatedSourcesAndReadOnlyHelpers(t *testing.T) {
	temporaryMachineEnvironment(t)
	fixture := newMachineWireFixture(t)
	target := temporaryWireTarget(t, fixture)
	dials := 0
	client, err := OpenTemporaryMachine(context.Background(), target, MachineOptions{
		RequiredAccess: models.CLIAccessFull,
		ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
			t.Fatal("saved profile/config accessed")
			return nil, nil
		},
		Credentials: session.MachineCredentialOptions{
			Exec:    func(context.Context, string) (string, error) { t.Fatal("saved helper/SSH accessed"); return "", nil },
			Keyring: func(context.Context, string) (string, error) { t.Fatal("saved keyring accessed"); return "", nil },
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials++
			if network != "tcp" || address != fixture.addr {
				t.Fatalf("alternate target dial: %s %s", network, address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
		StatementTimeout: 45*time.Second + time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(context.Background()) }()
	if !client.temporary || client.access != models.CLIAccessRowRead || client.tunnel != nil || client.statementMS != 45001 || dials != 1 {
		t.Fatal("temporary provenance, limits or isolation lost")
	}
	if _, err := client.Execute(context.Background(), MachineQuery{SQL: "DELETE FROM anything"}); err == nil || err.Error() != "POLICY_DENIED" {
		t.Fatalf("native saved execution accepted temporary provenance: %v", err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.startup["user"] != "username" || fixture.startup["database"] != "database" || fixture.startup["statement_timeout"] != "45001" || fixture.startup["standard_conforming_strings"] != "on" || fixture.startup["options"] != "" {
		t.Fatal("startup target/settings mismatch")
	}
	for _, sql := range fixture.queries {
		if strings.Contains(sql, "default_transaction_read_only") || strings.Contains(sql, "ROLE") {
			t.Fatal("temporary initialization overrides server policy or saved role")
		}
	}
	if !strings.Contains(strings.Join(fixture.queries, ";"), "BEGIN READ ONLY") {
		t.Fatal("Ping was not read-only")
	}
}

func TestTemporaryOpen_CredentialFailuresNeverDial(t *testing.T) {
	temporaryMachineEnvironment(t)
	fixture := newMachineWireFixture(t)
	target := temporaryWireTarget(t, fixture)
	for _, cause := range []error{errors.New("SECRET raw credential error"), context.DeadlineExceeded, nil} {
		_, err := openTemporaryMachine(context.Background(), target, MachineOptions{Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("credential failure dialed")
			return nil, nil
		}}, func(context.Context, session.TemporaryTarget) (string, error) { return "", cause })
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("credential error leaked/fell back: %v", err)
		}
	}
}

func TestTemporaryOpen_SocketTarget(t *testing.T) {
	temporaryMachineEnvironment(t)
	fixture := newMachineWireFixture(t)
	_, port, _ := net.SplitHostPort(fixture.addr)
	path := filepath.Join(t.TempDir(), "passfile")
	if err := os.WriteFile(path, []byte("localhost:"+port+":database:username:socket-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := "/tmp/synthetic, socket'\\dir"
	target, err := session.NewTemporaryTarget(session.TemporaryTargetFields{SocketDir: socket, Port: port, Database: "database", Username: "username", Passfile: path})
	if err != nil {
		t.Fatal(err)
	}
	dials := 0
	client, err := OpenTemporaryMachine(context.Background(), target, MachineOptions{Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials++
		if network != "unix" || address != filepath.Join(socket, ".s.PGSQL."+port) {
			t.Fatalf("socket target changed: %s %s", network, address)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", fixture.addr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil || dials != 1 {
		t.Fatalf("socket cleanup/fallback: err=%v dials=%d", err, dials)
	}
}

func TestTemporaryOpen_LifecycleDeadlineAndCleanup(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	temporaryMachineEnvironment(t)
	fixture := newMachineWireFixture(t)
	fixture.stallStartup = true
	target := temporaryWireTarget(t, fixture)
	start := time.Now()
	_, err := OpenTemporaryMachine(context.Background(), target, MachineOptions{ConnectTimeout: 35 * time.Millisecond})
	if err == nil || err.Error() != "TIMEOUT" || time.Since(start) > time.Second || fixture.connections.Load() != 1 {
		t.Fatalf("startup cancellation/fallback: %v attempts=%d elapsed=%v", err, fixture.connections.Load(), time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = openTemporaryMachine(ctx, target, MachineOptions{}, func(context.Context, session.TemporaryTarget) (string, error) {
		t.Fatal("precancelled credential")
		return "", nil
	})
	if err == nil || err.Error() != "TIMEOUT" {
		t.Fatalf("precancelled open: %v", err)
	}
	ready := newMachineWireFixture(t)
	client, err := OpenTemporaryMachine(context.Background(), temporaryWireTarget(t, ready), MachineOptions{CleanupTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal("close is not idempotent")
	}
	select {
	case <-client.conn.PgConn().CleanupDone():
	default:
		t.Fatal("close left pgx cleanup outstanding")
	}
	if ready.connections.Load() != 1 || strconv.Itoa(client.statementMS) != "10000" {
		t.Fatal("unexpected lifecycle defaults or retry")
	}
	ctx, cancel = context.WithCancel(context.Background())
	client, err = OpenTemporaryMachine(ctx, temporaryWireTarget(t, ready), MachineOptions{CleanupTimeout: 50 * time.Millisecond})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	_ = client.raw.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.raw.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancelled transport remained readable")
	}
	client.stop() // Join the parent watcher before observing transport disposal.
	client.transport.Lock()
	aborted := client.aborted
	client.transport.Unlock()
	if !aborted {
		t.Fatal("parent cancellation did not discard the owned transport")
	}
	_ = client.Close(context.Background())
	select {
	case <-client.conn.PgConn().CleanupDone():
	default:
		t.Fatal("parent cancellation left pgx cleanup outstanding")
	}
}
