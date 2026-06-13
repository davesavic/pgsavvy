//go:build integration

// This file exercises the SSH-tunnel feature end-to-end through the pkg/drivers/pg
// driver against a docker bastion + a private (non-host-published) Postgres.
//
// The tunnelled DSN host is `privatepg` — a docker service name that is NOT
// resolvable on the host. It only resolves bastion-side, so a successful
// connection proves the driver routes DNS + TCP through the tunnel (F1).
//
// Gating: every Test* here Skips unless BOTH DBSAVVY_TEST_SSH_BASTION and
// DBSAVVY_TEST_SSH_KEY are set (independent of DBSAVVY_TEST_PG). Bring the
// fixture up with `task sshtunnel:up`, which prints the exports to use.
package integration_test

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

	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/goleak"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session/sshtunnel"
)

const (
	envSSHBastion = "DBSAVVY_TEST_SSH_BASTION"
	envSSHKey     = "DBSAVVY_TEST_SSH_KEY"

	// privatepgDSN points at the docker service `privatepg` on port 5432.
	// The host is resolvable only on the bastion-side `private` network, so
	// reaching it from the test host is itself the proof the tunnel works.
	privatepgDSN = "postgres://dbsavvy:dbsavvy@privatepg:5432/dbsavvy_test?sslmode=disable"
)

// requireSSH skips unless both the bastion address and key path are set.
func requireSSH(t *testing.T) {
	t.Helper()
	if os.Getenv(envSSHBastion) == "" || os.Getenv(envSSHKey) == "" {
		t.Skipf("set %s and %s to run SSH-tunnel integration tests (see `task sshtunnel:up`)", envSSHBastion, envSSHKey)
	}
}

// tunnelProfile builds a tunnelled profile whose DSN host (`privatepg`) is
// only resolvable bastion-side. KnownHosts points at a fresh temp file so
// TOFU accept-new never touches the developer's ~/.ssh.
func tunnelProfile(t *testing.T) models.Connection {
	t.Helper()
	host, port := splitBastion(t, os.Getenv(envSSHBastion))
	return models.Connection{
		Name:   "ssh-tunnel",
		Driver: "postgres",
		DSN:    privatepgDSN,
		SSHTunnel: &models.SSHTunnelConfig{
			Host:         host,
			Port:         port,
			User:         "tester",
			IdentityFile: os.Getenv(envSSHKey),
			KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
		},
	}
}

// splitBastion parses "host:port" into its parts, defaulting the port to 22.
func splitBastion(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", envSSHBastion, addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port from %q: %v", addr, err)
	}
	return host, port
}

// scalarThroughTunnel acquires a session, runs a single-row/single-column
// query, and returns the first value. Used to assert SELECT 1 == 1.
func scalarThroughTunnel(t *testing.T, profile models.Connection) any {
	t.Helper()
	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, profile, nil)
	if err != nil {
		t.Fatalf("driver open through tunnel: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		t.Fatalf("acquire session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stream, err := sess.Stream(ctx, models.Query{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("stream SELECT 1: %v", err)
	}
	defer func() { _ = stream.Close() }()

	row, ok, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("stream.Next: %v", err)
	}
	if !ok {
		t.Fatal("stream.Next returned no row for SELECT 1")
	}
	if len(row.Values) != 1 {
		t.Fatalf("SELECT 1 returned %d columns, want 1", len(row.Values))
	}
	return row.Values[0]
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSSHTunnelConnect proves bastion-side resolution of `privatepg` (F1): a
// successful SELECT 1 through a host the test machine cannot resolve directly.
func TestSSHTunnelConnect(t *testing.T) {
	requireSSH(t)
	got := scalarThroughTunnel(t, tunnelProfile(t))
	if v, ok := got.(int32); !ok || v != 1 {
		t.Fatalf("SELECT 1 through tunnel = %#v, want int32(1)", got)
	}
}

// TestSSHTunnelCancel starts pg_sleep(30) in a goroutine through the tunnel,
// then cancels it via Connection.Cancel (whose CancelRequest packet ALSO rides
// the tunnel's DialFunc). It asserts the in-flight query goroutine actually
// returns an error within a bounded time — not merely that Cancel returned nil
// (Cancel is best-effort and returns nil even on failure).
func TestSSHTunnelCancel(t *testing.T) {
	requireSSH(t)
	ctx := context.Background()

	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, tunnelProfile(t), nil)
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		t.Fatalf("acquire session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	pgsess := sess.(*pg.Session)
	qid := models.QueryID{BackendPID: pgsess.BackendPID()}
	if qid.BackendPID == 0 {
		t.Fatal("BackendPID is zero — cancel cannot be authenticated")
	}

	// Run pg_sleep(30) in a goroutine; the blocked Next must observe a
	// terminal error once the backend is cancelled.
	queryErr := make(chan error, 1)
	go func() {
		stream, serr := sess.Stream(ctx, models.Query{SQL: "SELECT pg_sleep(30)"})
		if serr != nil {
			queryErr <- serr
			return
		}
		defer func() { _ = stream.Close() }()
		_, _, nerr := stream.Next(ctx)
		queryErr <- nerr
	}()

	// Cancel is best-effort and a no-op if the backend has not yet registered
	// the query, so retry on a short interval until the in-flight query observes
	// the cancellation. This removes the start-up race without a fixed sleep.
	deadline := time.Now().Add(10 * time.Second)
	var cancelledErr error
	cancelled := false
	for !cancelled && time.Now().Before(deadline) {
		cancelCtx, cancelDone := context.WithTimeout(ctx, 2*time.Second)
		if err := conn.Cancel(cancelCtx, qid); err != nil {
			cancelDone()
			t.Fatalf("Cancel returned error: %v", err)
		}
		cancelDone()
		select {
		case cancelledErr = <-queryErr:
			cancelled = true
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !cancelled {
		t.Fatal("in-flight pg_sleep(30) did not return after repeated Cancel within 10s — cancel did not take effect")
	}
	if cancelledErr == nil {
		t.Fatal("in-flight pg_sleep(30) returned nil error after Cancel — query was NOT cancelled")
	}

	// Prove the termination is an actual server-side cancellation (SQLSTATE
	// 57014), not an unrelated tunnel/connection failure that also yields a
	// non-nil error — that distinction is the whole point of this test.
	var pgErr *pgconn.PgError
	switch {
	case errors.As(cancelledErr, &pgErr):
		if pgErr.Code != "57014" {
			t.Fatalf("in-flight query failed with SQLSTATE %s (%v), want 57014 (canceling statement due to user request)", pgErr.Code, cancelledErr)
		}
	case strings.Contains(cancelledErr.Error(), "57014"):
		// Cancellation surfaced but wrapped so errors.As cannot reach the
		// PgError; the SQLSTATE is still proof it was a server-side cancel.
	default:
		t.Fatalf("in-flight query returned %v, not a server-side cancellation (SQLSTATE 57014)", cancelledErr)
	}
	t.Logf("in-flight query cancelled as expected: %v", cancelledErr)
}

// TestSSHTunnelGoleak opens, uses, and fully closes a tunnelled connection,
// then asserts no goroutines leaked (the tunnel listener + ssh client must be
// torn down on conn.Close).
func TestSSHTunnelGoleak(t *testing.T) {
	requireSSH(t)
	t.Run("no-leak", func(t *testing.T) {
		defer goleak.VerifyNone(t,
			goleak.IgnoreCurrent(),
			goleak.IgnoreTopFunction("github.com/jackc/pgx/v5/pgxpool.(*Pool).backgroundHealthCheck"),
		)

		ctx := context.Background()
		factory := pg.New(nil)
		drv, err := factory(ctx)
		if err != nil {
			t.Fatalf("driver factory: %v", err)
		}
		conn, err := drv.Open(ctx, tunnelProfile(t), nil)
		if err != nil {
			t.Fatalf("driver open: %v", err)
		}

		sess, err := conn.AcquireSession(ctx)
		if err != nil {
			_ = conn.Close()
			t.Fatalf("acquire session: %v", err)
		}

		stream, err := sess.Stream(ctx, models.Query{SQL: "SELECT 1"})
		if err != nil {
			_ = sess.Close()
			_ = conn.Close()
			t.Fatalf("stream: %v", err)
		}
		// Drain to EOF so the session inFlight guard is released cleanly.
		for {
			_, ok, nerr := stream.Next(ctx)
			if nerr != nil {
				t.Fatalf("stream.Next: %v", nerr)
			}
			if !ok {
				break
			}
		}
		_ = stream.Close()
		_ = sess.Close()
		if err := conn.Close(); err != nil {
			t.Fatalf("conn.Close: %v", err)
		}
	})
}

// TestSSHTunnelDialFailureTyped points the tunnel at an unreachable bastion and
// asserts Open fails with a *sshtunnel.DialError (IsDialError true) whose
// message names SSH — proving the failure is attributed to the tunnel, not
// mistaken for a Postgres connection error.
func TestSSHTunnelDialFailureTyped(t *testing.T) {
	requireSSH(t)
	profile := tunnelProfile(t)
	profile.SSHTunnel.Host = "127.0.0.1"
	profile.SSHTunnel.Port = 1 // nothing listens on port 1

	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, profile, nil)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("Open against unreachable bastion succeeded; want a dial error")
	}
	if !sshtunnel.IsDialError(err) {
		t.Fatalf("error is not a *sshtunnel.DialError: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "ssh tunnel") {
		t.Fatalf("dial error message %q does not name the ssh tunnel", msg)
	}
}

// TestDirectRegressionNoTunnel guards that adding the tunnel wiring did not
// break plain (non-tunnel) connects. Skips unless DBSAVVY_TEST_PG is set.
func TestDirectRegressionNoTunnel(t *testing.T) {
	if os.Getenv(envDSN) == "" {
		t.Skipf("set %s to run the direct-connect regression", envDSN)
	}
	profile := models.Connection{
		Name:   "direct-regression",
		Driver: "postgres",
		DSN:    os.Getenv(envDSN),
	}
	got := scalarThroughTunnel(t, profile)
	if v, ok := got.(int32); !ok || v != 1 {
		t.Fatalf("direct SELECT 1 = %#v, want int32(1)", got)
	}
}
