package pg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/99designs/keyring"
	"github.com/adrg/xdg"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
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
	// from false to true (Connection.Cancel now dials
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
		SupportsInlineEdit:   true,
		MaxIdentifierLen:     63,
	}
	require.Equal(t, expected, pgCapabilities)
}

func TestCapabilitiesLiveCancelMatchesCancelImpl(t *testing.T) {
	// Invariant: any Capabilities flag set true must have a non-sentinel
	// implementation, and any flag set false must surface ErrNotImplemented.
	// Since HasLiveCancel was flipped to true, we exercise a
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
	// prefix. The happy live-Session path is covered by integration
	// tests.
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/pgsavvy_unit_test")
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
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:1/pgsavvy_unit_test")
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

// TestPasswordPrompterTrioStoreLoadClear locks the trio's nil-guard contract:
// never-set → nil; Set(p) → p; Set(nil) → nil. Mirrors the secret-prompter
// trio's shape. t.Cleanup restores whatever was installed before the test so
// the package-global does not leak across tests.
func TestPasswordPrompterTrioStoreLoadClear(t *testing.T) {
	prev := globalPasswordPrompter.Load()
	t.Cleanup(func() { globalPasswordPrompter.Store(prev) })

	// Force a known clear baseline for the boundary assertion.
	globalPasswordPrompter.Store(nil)
	require.Nil(t, passwordPrompter(), "passwordPrompter() must be nil when never set / cleared")

	p := &staticPrompter{password: "x"}
	SetPasswordPrompter(p)
	require.Same(t, p, passwordPrompter(), "SetPasswordPrompter(p) must store p")

	SetPasswordPrompter(nil)
	require.Nil(t, passwordPrompter(), "SetPasswordPrompter(nil) must clear")
}

// TestOpenUsesInstalledGlobalPrompterResult proves the global prompter's
// RESULT is carried into resolution (not merely that the global is consulted):
// a creds-less profile pointed at an UNREACHABLE host resolves the password via
// the installed global, then Open progresses PAST credential resolution to the
// dial/ping phase and fails there — NOT at the no-credential refusal sentinel.
//
// Assertion choice: asserting the exact password byte reached pgx's dial config
// is not observable without a live server (the password is consumed inside
// pgxpool dial). We therefore assert (a) the global recording prompter WAS
// invoked, and (b) Open failed at the DIAL phase ("pg: ping:" / "pg: open:"),
// proving resolution succeeded with the prompted value rather than short-
// circuiting on errNoCredentialMechanism. A nil/empty result would have
// surfaced the refusal sentinel before any dial.
func TestOpenUsesInstalledGlobalPrompterResult(t *testing.T) {
	prev := globalPasswordPrompter.Load()
	t.Cleanup(func() { globalPasswordPrompter.Store(prev) })

	recorder := &staticPrompter{password: "pw-from-prompt"}
	SetPasswordPrompter(recorder)

	// Driver's startup prompter is the TUI refuser; if the global were ignored
	// the final step would refuse and Open would never dial.
	d := &Driver{prompter: session.TUIRefusePrompter{}}

	// Unreachable host (port 1) with NO inline creds, NO command, NO keyring,
	// NO pgpass → the final waterfall step is the only credential source.
	profile := drivers.ConnectionProfile{
		Name: "unreachable",
		DSN:  "postgres://u@127.0.0.1:1/pgsavvy_unit_test",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := d.Open(ctx, profile, nil)
	require.Nil(t, conn)
	require.Error(t, err)
	require.NotEmpty(t, recorder.lastHint, "global prompter must have been invoked")
	require.False(t, session.IsKeyringPassphraseRequiredInTUI(err),
		"must not be the keyring refusal")
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"resolution must not stall; failure must come from the dial phase")
	require.Contains(t, err.Error(), "pg:",
		"Open must progress past resolution to a dial/ping error, not the refusal sentinel")
}

// TestOpenNoSourceRefusalPreservedWithoutGlobal asserts the no-credential
// refusal is unchanged when no global is installed: a creds-less profile with a
// TUIRefusePrompter startup prompter returns the refusal sentinel and never
// dials.
func TestOpenNoSourceRefusalPreservedWithoutGlobal(t *testing.T) {
	prev := globalPasswordPrompter.Load()
	t.Cleanup(func() { globalPasswordPrompter.Store(prev) })
	globalPasswordPrompter.Store(nil) // boundary: global never set

	require.Nil(t, passwordPrompter(), "precondition: no global installed")

	d := &Driver{prompter: session.TUIRefusePrompter{}}
	profile := drivers.ConnectionProfile{
		Name: "no-creds",
		DSN:  "postgres://u@127.0.0.1:1/pgsavvy_unit_test",
	}

	conn, err := d.Open(context.Background(), profile, nil)
	require.Nil(t, conn)
	require.True(t, session.IsInteractivePromptUnsupported(err),
		"creds-less Open under TUIRefusePrompter must surface the prompt-refusal sentinel; got %v", err)
}

// TestOpenKeyringRefusesUnderInstalledGlobal is R1: a keyring_ref profile that
// requires a passphrase still returns errKeyringPassphraseRequiredInTUI even
// when a global password prompter is installed — proving the keyring step uses
// the Driver's startup (refusing) prompter, NOT the global.
func TestOpenKeyringRefusesUnderInstalledGlobal(t *testing.T) {
	// Isolate XDG so the keyring lives in a temp dir, and ensure the env
	// passphrase is unset so passphraseFunc reaches the prompter type-switch.
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })
	t.Setenv("PGSAVVY_KEYRING_PASSPHRASE", "")

	// Seed a keyring item so kr.Get triggers the passphrase func (an empty
	// keyring would fail with key-not-found before any passphrase prompt).
	dir := filepath.Join(tmp, "pgsavvy", "keyring")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	kr, err := keyring.Open(keyring.Config{
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		ServiceName:      "pgsavvy",
		FileDir:          dir,
		FilePasswordFunc: keyring.FixedStringPrompt("seed-phrase"),
	})
	require.NoError(t, err)
	require.NoError(t, kr.Set(keyring.Item{Key: "prod-db", Data: []byte("kr-secret")}))

	// Install a global that would happily return a password IF it were ever
	// consulted for the keyring step — it must NOT be.
	prev := globalPasswordPrompter.Load()
	t.Cleanup(func() { globalPasswordPrompter.Store(prev) })
	SetPasswordPrompter(&staticPrompter{password: "should-not-be-used"})

	d := &Driver{prompter: session.TUIRefusePrompter{}}
	profile := drivers.ConnectionProfile{
		Name:       "prod-db",
		KeyringRef: "prod-db",
		DSN:        "postgres://u@127.0.0.1:1/pgsavvy_unit_test",
	}

	conn, err := d.Open(context.Background(), profile, nil)
	require.Nil(t, conn)
	require.True(t, session.IsKeyringPassphraseRequiredInTUI(err),
		"keyring passphrase refusal must be preserved under an installed global; got %v", err)
}
