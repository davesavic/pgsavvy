package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// statementTimeoutRe is the permissive grammar for Connection.StatementTimeout.
// It is the SOLE regex gate, but not the sole defense: CanonicalizeStatementTimeout
// also rejects forbidden bytes and rebuilds the literal from a hard-coded unit
// table before it is interpolated into the SET command. See epic dbsavvy-921 §D7
// and dbsavvy-921.5's review-plan resolutions.
var statementTimeoutRe = regexp.MustCompile(`^(0|\d+\s*(ms|s|min|h|d))$`)

// validStatementTimeoutUnits is the closed allowlist consulted after the regex
// has matched. The canonical literal is rebuilt as digits + (one of these keys),
// so a malicious unit string can never reach the SET command even if the regex
// is later widened by accident.
var validStatementTimeoutUnits = map[string]struct{}{
	"ms":  {},
	"s":   {},
	"min": {},
	"h":   {},
	"d":   {},
}

// statementTimeoutForbiddenBytes are the SQL-meaningful bytes that must never
// appear in the raw input. The regex already rejects most of these for legal
// inputs, but this is the belt for the braces — it catches injection vectors
// that slip past a future regex bug.
const statementTimeoutForbiddenBytes = "';\"\\\x00\r\n\t"

// ErrStatementTimeoutInvalid is returned by BuildPgxConfig (via
// CanonicalizeStatementTimeout) when the profile's statement_timeout cannot
// be canonicalized to a known-safe literal.
var ErrStatementTimeoutInvalid = errors.New("session: invalid statement_timeout (want \"0\" or \"<n>{ms|s|min|h|d}\")")

// BuildPgxConfig translates a connection profile + a resolved password into a
// *pgxpool.Config ready for pgxpool.NewWithConfig.
//
// Wiring:
//   - cfg.ConnConfig.Password is overwritten with the password argument unless
//     password is the empty auto-discovery sentinel returned by
//     ResolvePassword; in that case pgx's built-in ~/.pgpass / PGPASSWORD
//     discovery runs at dial time.
//   - cfg.AfterConnect issues "SET statement_timeout = '<canonical>'" on every
//     fresh pool connection, and additionally "SET default_transaction_read_only
//     = on" iff profile.ReadOnly is true (epic dbsavvy-921 §D7, §D8). Both SETs
//     are re-applied on every pool-conn recycle by virtue of running in
//     AfterConnect (§D12).
//   - Pool defaults: MinConns=2, MaxConns=8, MaxConnLifetime=30m,
//     MaxConnIdleTime=5m, HealthCheckPeriod=1m (§11.3). MinConns was raised
//     from 1 to 2 in epic dbsavvy-66p.4 so that Connection.Cancel and any
//     concurrent session never compete for the only available pool slot —
//     the cancel path opens a fresh raw TCP cancel-request rather than
//     acquiring a pool conn, so MinConns=2 is conservative belt-and-braces
//     for callers that DO acquire a second session for sentinel queries.
//
// statement_timeout is validated at config-build time, never at SET time, so a
// misconfigured profile fails fast on the first call to BuildPgxConfig instead
// of poisoning the pool's AfterConnect hook.
//
// When the DSN resolves to a non-loopback host with sslmode=disable (or no
// sslmode at all — pgx's default "prefer" falls back to plaintext), a single
// stderr WARN is emitted. The config is still returned.
//
// pkg/session/profile.go is pgx-flavored in v1 because per-driver pool config
// is unavoidable; sibling helpers (profile_mysql.go etc.) appear in later
// epics. See §11.3.
//
// The ctx argument is reserved for future hooks; v1 performs no I/O.
func BuildPgxConfig(_ context.Context, profile models.Connection, password string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(profile.DSN)
	if err != nil {
		// pgxpool surfaces the DSN literal in its error; scrub inline
		// credentials before returning. See /review-plan-2026-05-17 Sec-7.
		return nil, fmt.Errorf("session: parse dsn: %s", RedactDSN(err.Error()))
	}

	rawTimeout := profile.StatementTimeout
	if rawTimeout == "" {
		rawTimeout = "0"
	}
	canonicalTimeout, err := CanonicalizeStatementTimeout(rawTimeout)
	if err != nil {
		return nil, err
	}

	// Empty sentinel = "let pgx auto-discover credentials at dial time".
	// Overwriting with "" would clobber any DSN-encoded password or value
	// pgxpool.ParseConfig already wired in from PGPASSWORD / pgpass.
	if password != "" {
		cfg.ConnConfig.Password = password
	}

	readOnly := profile.ReadOnly
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return runAfterConnect(ctx, conn, readOnly, canonicalTimeout)
	}

	cfg.MinConns = 2
	cfg.MaxConns = 8
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	if shouldWarnInsecureSSL(profile.DSN, cfg) {
		warnInsecureSSL(os.Stderr, profile.Name, cfg.ConnConfig.Host, sslModeForLog(profile.DSN))
	}

	return cfg, nil
}

// pgConnExecer is the slice of *pgx.Conn that runAfterConnect uses. Extracting
// it as an interface lets unit tests record the executed SET commands without
// dialling a live Postgres.
type pgConnExecer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// runAfterConnect applies the per-session SET commands required by the profile.
// canonicalTimeout MUST already have been validated by CanonicalizeStatementTimeout.
func runAfterConnect(ctx context.Context, conn pgConnExecer, readOnly bool, canonicalTimeout string) error {
	if readOnly {
		if _, err := conn.Exec(ctx, "SET default_transaction_read_only = on"); err != nil {
			return fmt.Errorf("session: SET default_transaction_read_only: %w", err)
		}
	}
	if _, err := conn.Exec(ctx, "SET statement_timeout = '"+canonicalTimeout+"'"); err != nil {
		return fmt.Errorf("session: SET statement_timeout: %w", err)
	}
	return nil
}

// CanonicalizeStatementTimeout validates raw and rebuilds the literal from the
// hard-coded unit table. The returned string is safe to interpolate into a SET
// statement_timeout = '<>' command: only digits and one allowlisted unit suffix
// can ever appear.
func CanonicalizeStatementTimeout(raw string) (string, error) {
	if strings.ContainsAny(raw, statementTimeoutForbiddenBytes) {
		return "", fmt.Errorf("%w: forbidden byte in %q", ErrStatementTimeoutInvalid, raw)
	}
	if !statementTimeoutRe.MatchString(raw) {
		return "", fmt.Errorf("%w: %q", ErrStatementTimeoutInvalid, raw)
	}
	if raw == "0" {
		return "0", nil
	}
	// Split into <digits><optional-ws><unit>.
	var i int
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	number := raw[:i]
	unit := strings.TrimSpace(raw[i:])
	if _, ok := validStatementTimeoutUnits[unit]; !ok {
		// Unreachable when the regex matches, but defends against a future
		// regex relax.
		return "", fmt.Errorf("%w: unknown unit %q", ErrStatementTimeoutInvalid, unit)
	}
	return number + unit, nil
}

// shouldWarnInsecureSSL reports whether BuildPgxConfig should emit the
// "remote + plaintext" warning for the given DSN / parsed config.
func shouldWarnInsecureSSL(dsn string, cfg *pgxpool.Config) bool {
	if cfg == nil || cfg.ConnConfig == nil {
		return false
	}
	if isLoopbackHost(cfg.ConnConfig.Host) {
		return false
	}
	return sslModeForLog(dsn) == "disable"
}

// warnInsecureSSL formats and writes the stderr WARN. Extracted so tests can
// drive it with a bytes.Buffer.
func warnInsecureSSL(w io.Writer, name, host, mode string) {
	if name == "" {
		name = "<unnamed>"
	}
	_, _ = fmt.Fprintf(w, "WARN: dbsavvy: connection %q targets non-loopback host %s with sslmode=%s — traffic is unencrypted\n", name, host, mode)
}

// sslModeForLog returns the sslmode advertised by the DSN, or pgx's default
// ("prefer") when the DSN omits it. "unknown" is returned only on a malformed
// DSN, which pgxpool.ParseConfig would already have rejected upstream — kept
// here so the function is safe to call on any string.
func sslModeForLog(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "unknown"
	}
	if v := u.Query().Get("sslmode"); v != "" {
		return v
	}
	return "prefer"
}

// dsnInlineCredRe matches the userinfo section of a URL-form DSN
// ("scheme://user:password@host..."), capturing the username so the
// password can be replaced with "***" without dropping the user. Only
// URL-form DSNs are covered; keyword/value DSNs ("user=... password=...")
// are out of scope for v1 (see parseDSNFields).
var dsnInlineCredRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^:/@?\s]+):[^@/?\s]+@`)

// RedactDSN scrubs inline passwords from any string that may embed a
// URL-form Postgres DSN. Used by BuildPgxConfig and (in dbsavvy-921.8)
// pg.Driver.Open before returning errors that bubble up to logs / TUI.
func RedactDSN(s string) string {
	return dsnInlineCredRe.ReplaceAllString(s, "${1}${2}:***@")
}

// kvDSNCredRe matches `key=value` form Postgres DSNs and libpq connection
// strings where the key is password/sslpassword. The value is captured both
// for quoted (single-quoted, with potential whitespace) and unquoted forms.
// The `(?i)` makes the key match case-insensitive.
var kvDSNCredRe = regexp.MustCompile(`(?i)\b(password|sslpassword)=('[^']*'|"[^"]*"|\S+)`)

// RedactConnectionString applies both the URL-form (dsnInlineCredRe) and the
// kv-form (kvDSNCredRe) scrubs in sequence. Use this in any code path that
// emits a DSN-shaped string to logs or to a user-visible toast.
func RedactConnectionString(s string) string {
	s = dsnInlineCredRe.ReplaceAllString(s, "${1}${2}:***@")
	s = kvDSNCredRe.ReplaceAllString(s, "$1=***")
	return s
}

// isLoopbackHost is true for empty host (Unix socket), "localhost", or any
// IP whose net.IP.IsLoopback reports true.
func isLoopbackHost(h string) bool {
	if h == "" || h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}
