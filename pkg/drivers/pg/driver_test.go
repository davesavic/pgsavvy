package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// stubPrompter is a no-op Prompter used by tests that only need a non-nil
// session.Prompter (the password path is never exercised in the unit suite).
type stubPrompter struct{}

func (stubPrompter) PromptPassword(_ context.Context, _ string) (string, error) {
	return "", nil
}

// staticPrompter records the most recent hint and returns a fixed password.
// Used to confirm New's prompter is the one captured by the returned Driver.
type staticPrompter struct {
	password string
	lastHint string
}

func (p *staticPrompter) PromptPassword(_ context.Context, hint string) (string, error) {
	p.lastHint = hint
	return p.password, nil
}

func TestNewReturnsFactoryYieldingDriverWithPrompter(t *testing.T) {
	prompter := &staticPrompter{password: "secret"}
	factory := New(prompter)
	require.NotNil(t, factory, "New must return a non-nil drivers.Factory")

	d, err := factory(context.Background())
	require.NoError(t, err)
	require.NotNil(t, d)

	pgd, ok := d.(*Driver)
	require.True(t, ok, "factory must yield a *pg.Driver")
	require.Same(t, prompter, pgd.prompter, "Driver must capture the prompter passed to New")
}

// _ asserts at compile time that New's return type is exactly drivers.Factory;
// a signature drift on Factory breaks the package build before any test runs.
var _ drivers.Factory = New(stubPrompter{})

func TestDriverNameIsPostgres(t *testing.T) {
	d := &Driver{}
	require.Equal(t, "postgres", d.Name())
}

func TestDriverCapabilitiesEqualsSingleSourceVar(t *testing.T) {
	d := &Driver{}
	require.Equal(t, pgCapabilities, d.Capabilities())
}

func TestPgCapabilitiesShape(t *testing.T) {
	// Locks the documented §11.1 capability set. HasLiveCancel was flipped
	// from false to true in epic dbsavvy-66p.4 (Connection.Cancel now dials
	// a fresh CancelRequest packet); the corresponding invariant test
	// TestCapabilitiesLiveCancelMatchesCancelImpl enforces that the flag and
	// the impl stay in lock-step.
	expected := drivers.Capabilities{
		HasSchemas:           true,
		HasMaterializedViews: true,
		HasArrayTypes:        true,
		HasJSONTypes:         true,
		HasLiveCancel:        true,
		HasExplainAnalyze:    true,
		HasNotice:            true,
		HasListenNotify:      true,
		SupportsCursor:       true,
		MaxIdentifierLen:     63,
	}
	require.Equal(t, expected, pgCapabilities)
}

func TestCapabilitiesLiveCancelMatchesCancelImpl(t *testing.T) {
	// Invariant: any Capabilities flag set true must have a non-sentinel
	// implementation, and any flag set false must surface ErrNotImplemented.
	// Since epic dbsavvy-66p.4 flipped HasLiveCancel to true, we exercise a
	// nil-PID Cancel (which short-circuits BEFORE the pool is touched) and
	// require it does NOT return ErrNotImplemented; the precondition error
	// drivers.ErrInvalidQueryID is the expected response.
	c := &Connection{}
	err := c.Cancel(context.Background(), models.QueryID{})
	if pgCapabilities.HasLiveCancel {
		require.False(t, errors.Is(err, drivers.ErrNotImplemented),
			"HasLiveCancel=true but Cancel still returns ErrNotImplemented")
	} else {
		require.True(t, errors.Is(err, drivers.ErrNotImplemented),
			"HasLiveCancel=false requires Cancel to return ErrNotImplemented")
	}
}

// TestConnectionCancelZeroPIDReturnsErrInvalidQueryID exercises the
// precondition-guard branch in Connection.Cancel: a QueryID with BackendPID=0
// is rejected before any pool / network I/O. This complements the integration
// tests in cancel_test.go which cover the live-cancel happy path.
func TestConnectionCancelZeroPIDReturnsErrInvalidQueryID(t *testing.T) {
	c := &Connection{}
	err := c.Cancel(context.Background(), models.QueryID{})
	require.ErrorIs(t, err, drivers.ErrInvalidQueryID)
}

func TestConnectionCancelHonorsCanceledCtx(t *testing.T) {
	// Pre-cancelled ctx must short-circuit BEFORE pool / dial work — the
	// receiver here has a nil pool, so any code path that reaches pool.Config
	// would nil-panic. The test passes iff the ctx.Err() branch wins.
	c := &Connection{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Cancel(ctx, models.QueryID{BackendPID: 42})
	require.ErrorIs(t, err, context.Canceled)
}

func TestConnectionAcquireSessionErrorsWithoutLivePool(t *testing.T) {
	// pgxpool.NewWithConfig is lazy; the first Acquire is what actually
	// dials. Without a live server, Acquire surfaces a dial error, which
	// AcquireSession MUST wrap with the documented "pg: acquire session:"
	// prefix. The happy live-Session path is covered by 921.10 integration
	// tests.
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/dbsavvy_unit_test")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	c := &Connection{pool: pool, serverVersion: "test"}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	sess, err := c.AcquireSession(ctx)
	require.Nil(t, sess)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pg: acquire session:")
}

func TestConnectionServerVersionReturnsCachedValue(t *testing.T) {
	const cached = "PostgreSQL 16.4 on x86_64-pc-linux-gnu, compiled by gcc"
	c := &Connection{serverVersion: cached}

	// ServerVersion must be a pointer-load: 10 calls return the identical
	// cached string with no detour through the pool. The Connection is
	// constructed with a nil pool here; if ServerVersion ever started
	// touching pool internally, this loop would nil-panic.
	for range 10 {
		require.Equal(t, cached, c.ServerVersion())
	}
}

func TestConnectionCloseIsIdempotent(t *testing.T) {
	// pgxpool.NewWithConfig does not dial; it constructs the pool lazily.
	// We can therefore exercise real pool.Close() in a unit test without a
	// live Postgres.
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/dbsavvy_unit_test")
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)

	c := &Connection{pool: pool, serverVersion: "test"}
	require.NoError(t, c.Close(), "first Close")
	require.NoError(t, c.Close(), "second Close must be a no-op, not panic")
	require.NoError(t, c.Close(), "third Close still a no-op")
}

func TestInterfaceAssertions(t *testing.T) {
	// Forces the compile-time assertions in driver.go and connection.go to
	// participate in the test binary. A signature drift in drivers.Driver
	// or drivers.Connection breaks here, not in a downstream consumer.
	var _ drivers.Driver = (*Driver)(nil)
	var _ drivers.Connection = (*Connection)(nil)
}

// Compile-only assurance that session.Prompter satisfies our usage shape.
var _ session.Prompter = stubPrompter{}
