package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// pgCapabilities is the single-source-of-truth Capabilities value for the
// Postgres driver. Tests assert deep-equality against this var rather than a
// literal so a future field addition can't silently drift the public surface.
// HasLiveCancel was flipped from false to true in epic dbsavvy-66p.4, which
// fulfils the D17 deferral from dbsavvy-921: Connection.Cancel now dials a
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
// configuration is read — see epic dbsavvy-921 D16.
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
func (d *Driver) Open(ctx context.Context, profile drivers.ConnectionProfile) (drivers.Connection, error) {
	password, err := session.ResolvePassword(ctx, profile, d.prompter)
	if err != nil {
		return nil, err
	}

	cfg, err := session.BuildPgxConfig(ctx, profile, password)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: open: %s", session.RedactDSN(err.Error()))
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %s", session.RedactDSN(err.Error()))
	}

	var version string
	if err := pool.QueryRow(ctx, "SELECT version()").Scan(&version); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: server version: %w", err)
	}

	return &Connection{
		pool:          pool,
		serverVersion: version,
		majorVersion:  parseMajorVersion(version),
	}, nil
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
