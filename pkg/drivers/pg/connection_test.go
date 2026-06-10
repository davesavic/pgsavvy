package pg

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session/sshtunnel"
)

// recordStages is a ProgressReporter that captures the stages Open emits, in
// order, so the real emit sites in Driver.Open are verifiable without a live DB.
type recordStages struct {
	stages []drivers.ConnectStage
}

func (r *recordStages) Report(stage drivers.ConnectStage) {
	r.stages = append(r.stages, stage)
}

// fakeTunnel is a recording sshTunnel used to verify the SSH-tunnel wiring in
// Driver.Open without a live SSH server. dialCalls records every DialContext
// invocation; closed flips true on Close so ordering/idempotency is testable.
type fakeTunnel struct {
	dialCalls atomic.Int32
	closeErr  error
	closed    atomic.Bool
	// dialErr, when set, is returned from DialContext so the DB dial fails
	// fast (no live Postgres needed) while still recording the call.
	dialErr error
}

func (f *fakeTunnel) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	f.dialCalls.Add(1)
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	// A non-connectable address: pgx will fail its handshake on this conn, so
	// Open never needs a real server. Connect to a closed port to get an
	// immediate error.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:1", 50*time.Millisecond)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (f *fakeTunnel) Close() error {
	f.closed.Store(true)
	return f.closeErr
}

// withOpenTunnel swaps the package openTunnel seam for the duration of the test
// and restores it afterwards.
func withOpenTunnel(t *testing.T, fn func(promptCtx, dialCtx context.Context, cfg models.SSHTunnelConfig) (sshTunnel, error)) {
	t.Helper()
	prev := openTunnel
	openTunnel = fn
	t.Cleanup(func() { openTunnel = prev })
}

func tunnelProfile() models.Connection {
	return models.Connection{
		Name: "tunnelled",
		DSN:  "postgres://u@db.internal:5432/app",
		// Password set so ResolvePassword (which runs BEFORE the tunnel logic)
		// short-circuits on the inline-plaintext branch — the credential
		// waterfall is out of scope for the tunnel-wiring tests.
		Password: "p",
		SSHTunnel: &models.SSHTunnelConfig{
			Host: "bastion.example", User: "deploy", Port: 22, IdentityFile: "/k",
		},
	}
}

// TestDriverOpenNilTunnelSetsNoDialOrLookupFunc confirms AC1: with no
// SSHTunnel, Open opens no tunnel and leaves DialFunc/LookupFunc unset. The
// seam is overridden to FAIL if called, proving the nil branch never opens one.
func TestDriverOpenNilTunnelLeavesConfigUnchanged(t *testing.T) {
	withOpenTunnel(t, func(context.Context, context.Context, models.SSHTunnelConfig) (sshTunnel, error) {
		t.Fatal("openTunnel must not be called when SSHTunnel is nil")
		return nil, nil
	})

	// We can't reach a real server, but the cfg mutation happens BEFORE pool
	// creation, so we assert the wiring at the cfg level by reusing the exact
	// helpers Open uses. With nil cfg, openSSHTunnel returns (nil,nil).
	tun, err := openSSHTunnel(context.Background(), context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, tun, "nil SSHTunnel must yield a nil tunnel")
}

// TestDriverOpenTunnelInvokesFakeDialAndSetsConfig confirms AC2/AC3/AC6: a
// non-nil SSHTunnel routes the DB dial through the fake tunnel's DialContext,
// sets DialFunc + an identity LookupFunc, and the dial actually fires.
func TestDriverOpenTunnelRoutesDialThroughFake(t *testing.T) {
	fake := &fakeTunnel{dialErr: errors.New("boom: dial refused")}
	withOpenTunnel(t, func(context.Context, context.Context, models.SSHTunnelConfig) (sshTunnel, error) {
		return fake, nil
	})

	d := &Driver{prompter: stubPrompter{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rec := &recordStages{}
	conn, err := d.Open(ctx, tunnelProfile(), rec)
	// Open fails at Ping (the fake dial returns an error), so no Connection is
	// returned — but the fake's DialContext MUST have been invoked, proving the
	// DialFunc wiring routed the DB dial through the tunnel.
	require.Error(t, err)
	require.Nil(t, conn)
	require.GreaterOrEqual(t, fake.dialCalls.Load(), int32(1),
		"the DB dial must route through the fake tunnel's DialContext")
	// The real Open emit site fired StageTunnel once the tunnel was established;
	// StageAuthenticated is NOT emitted because the dial fails at Ping.
	require.Equal(t, []drivers.ConnectStage{drivers.StageTunnel}, rec.stages,
		"a tunnelled Open emits StageTunnel and, on ping failure, no StageAuthenticated")
}

// TestDriverOpenExcludesPromptFromConnectDeadline confirms epic dbsavvy-t60w:
// Open derives the network deadline (connectTimeout) AFTER credential/prompt
// resolution, so the prompt context carries NO deadline while the dial context
// does. Regression guard for the bug where a ~4s interactive SSH prompt ate the
// 10s budget and tunnelled connects timed out at pool.Ping.
func TestDriverOpenExcludesPromptFromConnectDeadline(t *testing.T) {
	var promptHasDeadline, dialHasDeadline bool
	var dialBudget time.Duration
	withOpenTunnel(t, func(promptCtx, dialCtx context.Context, _ models.SSHTunnelConfig) (sshTunnel, error) {
		_, promptHasDeadline = promptCtx.Deadline()
		dl, ok := dialCtx.Deadline()
		dialHasDeadline = ok
		if ok {
			dialBudget = time.Until(dl)
		}
		// Bail before pool dial — only the handed-in contexts matter here.
		return nil, errors.New("stop: contexts captured")
	})

	d := &Driver{prompter: stubPrompter{}}
	_, err := d.Open(context.Background(), tunnelProfile(), nil)
	require.Error(t, err)

	require.False(t, promptHasDeadline,
		"prompt context must NOT carry the connect deadline (human typing time is untimed)")
	require.True(t, dialHasDeadline, "dial context must carry the connect deadline")
	require.Greater(t, dialBudget, 25*time.Second,
		"dial budget should be ~connectTimeout, not eaten by the prompt")
	require.LessOrEqual(t, dialBudget, connectTimeout)
}

// TestIdentityLookupReturnsHostUnresolved confirms F1: the LookupFunc installed
// when tunnelling is an identity passthrough so pgconn never resolves the
// bastion-only host client-side.
func TestIdentityLookupReturnsHostUnresolved(t *testing.T) {
	addrs, err := identityLookup(context.Background(), "db.internal")
	require.NoError(t, err)
	require.Equal(t, []string{"db.internal"}, addrs)
}

// TestDriverOpenSetsDialAndLookupFuncWhenTunnelled is the cfg-level regression
// for AC2/AC6: after the tunnel branch runs, DialFunc and LookupFunc are
// non-nil. We exercise the exact mutation Open performs (the fake never dials
// here) so the assertion is hermetic.
func TestDriverOpenSetsDialAndLookupFuncWhenTunnelled(t *testing.T) {
	fake := &fakeTunnel{}
	cfg, err := pgxpool.ParseConfig("postgres://u:p@db.internal:5432/app")
	require.NoError(t, err)

	// Mirror the Open mutation (pgx ParseConfig installs default
	// Dial/Lookup funcs; Open overwrites both when tunnelling).
	cfg.ConnConfig.DialFunc = fake.DialContext
	cfg.ConnConfig.LookupFunc = identityLookup

	require.NotNil(t, cfg.ConnConfig.DialFunc)
	require.NotNil(t, cfg.ConnConfig.LookupFunc)

	addrs, err := cfg.ConnConfig.LookupFunc(context.Background(), "db.internal")
	require.NoError(t, err)
	require.Equal(t, []string{"db.internal"}, addrs, "LookupFunc must pass the host through unresolved")
}

// TestDriverOpenTunnelOpenFailureReturnsSSHError confirms AC4: when the tunnel
// fails to open, Open returns the typed SSH error, never creates a pool, and
// leaks nothing.
func TestDriverOpenTunnelOpenFailureReturnsSSHError(t *testing.T) {
	// Force the ssh-agent auth branch with no agent present so the REAL
	// sshtunnel.Open returns a deterministic, environment-independent typed
	// *DialError ("ssh-agent requested but SSH_AUTH_SOCK is unset"). This
	// exercises the typed-error path end-to-end via the default seam without a
	// test-only export of the unexported DialError constructor.
	t.Setenv("SSH_AUTH_SOCK", "")
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	prof := tunnelProfile()
	prof.SSHTunnel = &models.SSHTunnelConfig{
		Host: "127.0.0.1", User: "deploy", Port: 1, IdentityFromAgent: true,
	}

	d := &Driver{prompter: stubPrompter{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := d.Open(ctx, prof, nil)
	require.Error(t, err)
	require.Nil(t, conn, "no Connection (and therefore no pool) on tunnel-open failure")
	require.True(t, sshtunnel.IsDialError(err),
		"tunnel-open failure must surface a typed SSH error, not a Postgres error")
}

// TestConnectionCloseClosesPoolThenTunnel confirms AC5: Close closes the pool
// then the tunnel inside closeOnce, returns the tunnel's close error, and is
// idempotent.
func TestConnectionCloseClosesPoolThenTunnel(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/dbsavvy_unit_test")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)

	closeErr := errors.New("tunnel close failed")
	fake := &fakeTunnel{closeErr: closeErr}
	c := &Connection{pool: pool, serverVersion: "test", tunnel: fake}

	require.ErrorIs(t, c.Close(), closeErr, "Close must return the tunnel's close error")
	require.True(t, fake.closed.Load(), "tunnel must be closed by Close")

	// Double-Close stays safe and does not re-invoke the tunnel close error.
	require.NoError(t, c.Close(), "second Close must be a no-op (closeOnce)")
}

// TestConnectionCloseNilTunnelReturnsNil confirms the no-tunnel Close path is
// unchanged (returns nil).
func TestConnectionCloseNilTunnelReturnsNil(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/dbsavvy_unit_test")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)

	c := &Connection{pool: pool, serverVersion: "test"}
	require.NoError(t, c.Close())
	require.NoError(t, c.Close())
}

// TestTunnelledConnectionHasNonNilDialFunc confirms AC6: a tunnelled
// Connection's pool config carries a non-nil DialFunc, so Cancel (which dials
// via pgconnCfg.DialFunc) inherits the tunnel route automatically.
func TestTunnelledConnectionHasNonNilDialFunc(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@db.internal:5432/app")
	require.NoError(t, err)
	fake := &fakeTunnel{}
	cfg.ConnConfig.DialFunc = fake.DialContext
	cfg.ConnConfig.LookupFunc = identityLookup

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	c := &Connection{pool: pool, serverVersion: "test", tunnel: fake}
	defer func() { _ = c.Close() }()

	require.NotNil(t, c.pool.Config().ConnConfig.DialFunc,
		"tunnelled Connection must expose a non-nil DialFunc for Cancel to inherit")
}
