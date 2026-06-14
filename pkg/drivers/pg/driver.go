package pg

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
	"github.com/davesavic/dbsavvy/pkg/session/sshtunnel"
)

// sshTunnel is the minimal SSH-tunnel surface the pg driver depends on. It is
// satisfied by *sshtunnel.Tunnel and exists ONLY so the openTunnel seam can be
// overridden with a fake in unit tests (sshtunnel.Open establishes a live SSH
// client, which cannot run offline). Connection stores this interface, not the
// concrete type, so the fake flows end-to-end through Open and Close.
type sshTunnel interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

// openTunnel is the package-private seam for establishing an SSH tunnel. It
// defaults to sshtunnel.OpenWithPrompter; tests reassign it to inject a
// recording fake. Keep this the SMALLEST possible seam — do not grow it into a
// strategy object.
//
// promptCtx bounds interactive secret resolution (no network deadline — see
// connectTimeout); dialCtx bounds the bastion dial + handshake.
var openTunnel = func(promptCtx, dialCtx context.Context, cfg models.SSHTunnelConfig) (sshTunnel, error) {
	return sshtunnel.OpenWithPrompter(promptCtx, dialCtx, cfg, secretPrompter())
}

// connectTimeout bounds the NETWORK phase of Open (bastion dial + SSH
// handshake + pool dial + pool.Ping + SELECT version()). It deliberately
// excludes interactive credential prompts, which Open resolves before deriving
// the dialCtx. Long enough to ride out a slow tunneled handshake, short enough
// that an unreachable host fails fast instead of wedging the UI. Raised from
// the former 10s adapters-layer budget: a tunneled connect
// must fit an SSH handshake plus AfterConnect SET round-trips through the
// bastion, which 10s could not cover once the prompt was excluded.
const connectTimeout = 30 * time.Second

// globalLogger is the package-level *slog.Logger used by Driver / Connection
// / Session instrumentation emits. It is set ONCE by SetGlobalLogger from the
// app entry-point AFTER logs.Open succeeds — preserving the init-time
// drivers.Register invariant (the registration in main.go runs at init time,
// before logs.Open has run). When unset, all instrumentation emits are no-ops
// (logs.Event tolerates a nil logger).
//
// The atomic.Pointer wrapper makes concurrent reads race-free; emits acquire
// the current pointer via Load.
var globalLogger atomic.Pointer[slog.Logger]

// SetGlobalLogger installs the package-level logger used by instrumentation
// emits. Safe to call multiple times (last write wins). Pass nil to disable
// emits (e.g. for tests that want to silence the driver). See AD-11.
func SetGlobalLogger(l *slog.Logger) {
	globalLogger.Store(l)
}

// pkgLogger returns the currently-installed package logger or nil. Helpers
// in this package call logs.Event(pkgLogger(), …) so a nil logger silently
// no-ops.
func pkgLogger() *slog.Logger { return globalLogger.Load() }

// globalSecretPrompter is the package-level masked SSH secret prompter,
// installed late by SetSecretPrompter from the app entry-point AFTER the
// GUI (and its prompt popup) is wired — mirroring globalLogger above and
// preserving the init-time drivers.Register invariant (registration runs
// before any GUI exists). When unset, Load returns nil and the tunnel
// layer's nil-prompter contract applies: NO interactive prompting, exactly
// as the headless sshtunnel.Open path (see sshtunnel.OpenWithPrompter).
var globalSecretPrompter atomic.Pointer[session.SecretPrompter]

// SetSecretPrompter installs the masked interactive SSH secret prompter used
// by Open when a tunnelled profile needs an encrypted-key passphrase or SSH
// password and no *_command is configured. Pass nil to disable interactive
// prompting (headless). Safe to call multiple times (last write wins).
func SetSecretPrompter(p session.SecretPrompter) {
	if p == nil {
		globalSecretPrompter.Store(nil)
		return
	}
	globalSecretPrompter.Store(&p)
}

// secretPrompter returns the installed prompter or nil. nil routes through the
// tunnel layer's headless path (no interactive entry), identical to today.
func secretPrompter() session.SecretPrompter {
	if p := globalSecretPrompter.Load(); p != nil {
		return *p
	}
	return nil
}

// pgCapabilities is the single-source-of-truth Capabilities value for the
// Postgres driver. Tests assert deep-equality against this var rather than a
// literal so a future field addition can't silently drift the public surface.
// HasLiveCancel was flipped from false to true: Connection.Cancel now dials a
// fresh CancelRequest packet against the same server using the per-session
// secret key captured at AcquireSession time. See connection.go:Cancel.
var pgCapabilities = drivers.Capabilities{
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

// Driver is the Postgres implementation of drivers.Driver. It captures the
// session.Prompter passed to New so that Open can run the credentials
// waterfall against an interactive fallback.
type Driver struct {
	prompter session.Prompter
}

// New returns a drivers.Factory closure that, when invoked, yields a
// *Driver wrapping prompter. The closure shape (rather than returning
// *Driver directly) lets main.go register the factory before any per-process
// configuration is read.
func New(prompter session.Prompter) drivers.Factory {
	return func(_ context.Context) (drivers.Driver, error) {
		return &Driver{prompter: prompter}, nil
	}
}

// Name returns the canonical engine identifier registered with drivers.Register.
func (d *Driver) Name() string { return "postgres" }

// Capabilities returns the static feature flags advertised by the Postgres
// driver. The returned struct equals pgCapabilities (deep-equality testable).
func (d *Driver) Capabilities() drivers.Capabilities { return pgCapabilities }

// Open resolves the profile's credentials, builds a pgxpool.Config via
// session.BuildPgxConfig, opens a pool, pings it, and caches SELECT version()
// in the returned *Connection.
//
// Errors that may embed a DSN (pgxpool.NewWithConfig / pool.Ping) are passed
// through session.RedactDSN before being returned so inline credentials do
// not leak into logs or the TUI. Errors from ResolvePassword and
// BuildPgxConfig propagate unchanged (they never include the DSN literal).
func (d *Driver) Open(ctx context.Context, profile drivers.ConnectionProfile, reporter drivers.ProgressReporter) (drivers.Connection, error) {
	log := pkgLogger()
	redactedDSN := session.RedactConnectionString(profile.DSN)
	logs.Event(log, "db", "conn_open",
		slog.String("profile", profile.Name),
		slog.String("redacted_dsn", redactedDSN),
	)
	start := time.Now()

	emitDone := func(err error) {
		attrs := []slog.Attr{
			slog.String("profile", profile.Name),
			slog.String("redacted_dsn", redactedDSN),
			slog.Int64("ms", time.Since(start).Milliseconds()),
		}
		if err != nil {
			attrs = append(attrs, slog.Any("err", session.RedactConnectionString(err.Error())))
		}
		logs.Event(log, "db", "conn_open_done", attrs...)
	}

	password, err := session.ResolvePassword(ctx, profile, d.prompter)
	if err != nil {
		emitDone(err)
		return nil, err
	}

	cfg, err := session.BuildPgxConfig(ctx, profile, password)
	if err != nil {
		emitDone(err)
		return nil, err
	}

	// NOTICE/WARNING plumbing: the per-Connection router
	// is constructed BEFORE pool creation so cfg.ConnConfig.OnNotice can be
	// wired exactly once — the pgconn handler is captured at pool dial time
	// and cannot be replaced thereafter. session.BuildPgxConfig does NOT set
	// OnNotice itself, by design: the router is a per-Connection object and
	// belongs at the pg-driver wiring layer.
	router := NewNoticeRouter()
	cfg.ConnConfig.OnNotice = router.route

	// connectTimeout bounds only the NETWORK phase (bastion dial + SSH
	// handshake + pool dial + ping + SELECT version()) — it is derived HERE,
	// after credential and interactive-secret resolution, so a human typing a
	// passphrase is never charged against the dial budget.
	// The parent ctx (untimed by the connect path) still governs the prompt and
	// remains cancellable for supersession.
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	// SSH tunnel (epic ssh-tunnel T5): when the profile carries an
	// SSHTunnelConfig the tunnel MUST be established before the pool is
	// created so every DB dial routes through it. On success we mutate cfg
	// (same idiom as OnNotice above) to inject the tunnel's DialContext as
	// pgconn's DialFunc, and an identity LookupFunc so pgconn does NOT resolve
	// the bastion-only DSN host client-side — the raw hostname must reach
	// DialContext for the SSH server to resolve it bastion-side (F1).
	//
	// The prompt phase runs under ctx (untimed); the bastion dial + handshake
	// run under dialCtx (the connect budget).
	tunnel, err := openSSHTunnel(ctx, dialCtx, profile.SSHTunnel)
	if err != nil {
		emitDone(err)
		return nil, err
	}
	if tunnel != nil {
		cfg.ConnConfig.DialFunc = tunnel.DialContext
		cfg.ConnConfig.LookupFunc = identityLookup
		drivers.ReportStage(reporter, drivers.StageTunnel)
	}

	pool, err := pgxpool.NewWithConfig(dialCtx, cfg)
	if err != nil {
		closeTunnel(tunnel)
		wrapped := fmt.Errorf("pg: open: %s", session.RedactDSN(err.Error()))
		emitDone(wrapped)
		return nil, wrapped
	}

	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		closeTunnel(tunnel)
		wrapped := fmt.Errorf("pg: ping: %s", session.RedactDSN(err.Error()))
		emitDone(wrapped)
		return nil, wrapped
	}
	drivers.ReportStage(reporter, drivers.StageAuthenticated)

	var version string
	if err := pool.QueryRow(dialCtx, "SELECT version()").Scan(&version); err != nil {
		pool.Close()
		closeTunnel(tunnel)
		wrapped := fmt.Errorf("pg: server version: %w", err)
		emitDone(wrapped)
		return nil, wrapped
	}

	emitDone(nil)
	return &Connection{
		pool:          pool,
		serverVersion: version,
		majorVersion:  parseMajorVersion(version),
		notices:       router,
		tunnel:        tunnel,
	}, nil
}

// openSSHTunnel opens an SSH tunnel for cfg, or returns (nil, nil) when cfg is
// nil (no tunnel configured). A non-nil error is the typed SSH error from the
// tunnel layer (sshtunnel.IsDialError reports true); the pool is never created
// in that case.
func openSSHTunnel(promptCtx, dialCtx context.Context, cfg *models.SSHTunnelConfig) (sshTunnel, error) {
	if cfg == nil {
		return nil, nil
	}
	return openTunnel(promptCtx, dialCtx, *cfg)
}

// closeTunnel closes t when non-nil. Used on Open failure paths after the
// tunnel is established so a leak cannot escape a Connection that is never
// returned.
func closeTunnel(t sshTunnel) {
	if t == nil {
		return
	}
	_ = t.Close()
}

// identityLookup is an identity-passthrough pgconn.LookupFunc: it returns the
// host unresolved so the raw hostname reaches the tunnel's DialContext and the
// SSH server performs resolution bastion-side. Installed only when tunnelling.
func identityLookup(_ context.Context, host string) ([]string, error) {
	return []string{host}, nil
}

// parseMajorVersion extracts the leading numeric major from a "PostgreSQL X.Y …"
// version() string. Returns 0 when the prefix is missing or the digits don't
// parse — callers MUST treat 0 as "unknown" and suppress version-driven
// warnings.
func parseMajorVersion(s string) int {
	const prefix = "PostgreSQL "
	if !strings.HasPrefix(s, prefix) {
		return 0
	}
	rest := s[len(prefix):]
	n := 0
	digits := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		digits++
	}
	if digits == 0 {
		return 0
	}
	return n
}

var _ drivers.Driver = (*Driver)(nil)
