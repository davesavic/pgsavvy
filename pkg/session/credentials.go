package session

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// Prompter is the interactive callback used by ResolvePassword when every
// non-interactive credential mechanism has been exhausted. Implementations
// must respect ctx cancellation when possible. See TerminalPrompter for the
// default TTY-backed implementation.
type Prompter interface {
	// PromptPassword asks the user for a password. hint is a short
	// human-readable label (e.g. "password for prod-db"). Returning ("", nil)
	// signals "user provided no value" and is treated as failure by the
	// waterfall.
	PromptPassword(ctx context.Context, hint string) (string, error)
}

// Unexported sentinel errors. Kept unexported by design: callers
// outside this package use errors.Is via wrapped sentinels exposed in later
// tasks, or simply check error presence.
var (
	errNoUsableShell         = errors.New("session: no usable shell for password_command (tried $SHELL, /bin/bash, /bin/sh)")
	errPgpassInsecureMode    = errors.New("session: pgpass file has world/group permissions; refusing to read (libpq parity)")
	errNoTTY                 = errors.New("session: stdin is not a TTY; cannot prompt for password")
	errNoCredentialMechanism = errors.New("session: no credential mechanism produced a password")
)

// ResolvePassword walks the five-step credentials waterfall for the given
// profile and returns the resolved password.
//
// Waterfall order (each step is skipped if its profile field is unset; an
// empty result also falls through to the next step):
//
//  1. profile.Password         — inline plaintext (development only).
//  2. profile.PasswordCommand  — shell command whose stdout is the password.
//  3. profile.KeyringRef       — item key in the 99designs/keyring file backend
//     at $XDG_DATA_HOME/dbsavvy/keyring (no CGO; epic D1).
//  4. profile.PgpassPath       — explicit libpq-format .pgpass file (mode 0600).
//  5. prompter                 — interactive Prompter, if non-nil.
//
// Auto-discovery sentinel: when prompter is nil AND every mechanism above
// either was unset or produced an empty value, ResolvePassword returns
// ("", nil). Callers (e.g. pkg/session BuildPgxConfig)
// interpret this as "let pgx auto-discover ~/.pgpass on dial".
//
// When prompter IS non-nil and every mechanism (including the prompter)
// produced no password, ResolvePassword returns a wrapped
// errNoCredentialMechanism error.
//
// Any mechanism that returns a typed error (e.g. errPgpassInsecureMode,
// password_command exit != 0, keyring open failure) short-circuits the
// waterfall and propagates that error wrapped with context — empty-result
// fallthrough is reserved for "this mechanism had nothing to contribute",
// not "this mechanism failed".
//
// See DESIGN.md §11.2.
func ResolvePassword(ctx context.Context, profile models.Connection, prompter Prompter) (string, error) {
	// 1. Inline plaintext. Empty value falls through (explicit AC).
	if profile.Password != "" {
		return profile.Password, nil
	}

	// 2. password_command.
	if profile.PasswordCommand != "" {
		pw, err := execPasswordCommand(ctx, profile.PasswordCommand)
		if err != nil {
			return "", fmt.Errorf("session: password_command: %w", err)
		}
		if pw != "" {
			return pw, nil
		}
	}

	// 3. keyring (file backend only).
	if profile.KeyringRef != "" {
		pw, err := resolveKeyring(ctx, profile.KeyringRef, prompter)
		if err != nil {
			return "", fmt.Errorf("session: keyring: %w", err)
		}
		if pw != "" {
			return pw, nil
		}
	}

	// 4. Explicit pgpass.
	if profile.PgpassPath != "" {
		pw, err := resolveExplicitPgpass(profile.PgpassPath, profile.DSN)
		if err != nil {
			return "", fmt.Errorf("session: pgpass: %w", err)
		}
		if pw != "" {
			return pw, nil
		}
	}

	// 5. Prompter.
	if prompter != nil {
		hint := "password"
		if profile.Name != "" {
			hint = fmt.Sprintf("password for %s", profile.Name)
		}
		pw, err := prompter.PromptPassword(ctx, hint)
		if err != nil {
			return "", fmt.Errorf("session: prompt: %w", err)
		}
		if pw != "" {
			return pw, nil
		}
		// Prompter present but returned empty → typed error.
		return "", errNoCredentialMechanism
	}

	// Auto-discovery sentinel: no prompter, nothing matched. Caller decides
	// whether to let pgx auto-discover ~/.pgpass.
	return "", nil
}

// resolveExplicitPgpass enforces the libpq mode policy and returns the
// matching password from path for the host/port/dbname/user encoded in dsn.
// See epic D15 and D20.
func resolveExplicitPgpass(path, dsn string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: %s mode=%04o", errPgpassInsecureMode, path, info.Mode().Perm())
	}

	host, port, dbname, user, err := parseDSNFields(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // path is user-configured profile field; mode already validated
	if err != nil {
		return "", err
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := splitPgpassLine(line)
		if len(fields) < 5 {
			continue
		}
		if !pgpassFieldMatches(fields[0], host) {
			continue
		}
		if !pgpassFieldMatches(fields[1], port) {
			continue
		}
		if !pgpassFieldMatches(fields[2], dbname) {
			continue
		}
		if !pgpassFieldMatches(fields[3], user) {
			continue
		}
		return fields[4], nil
	}

	return "", fmt.Errorf("no matching entry in %s for host=%s port=%s db=%s user=%s", path, host, port, dbname, user)
}

// splitPgpassLine splits a libpq pgpass line on unescaped colons. Backslash
// escapes a colon or backslash in any field.
func splitPgpassLine(line string) []string {
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			cur.WriteByte(line[i+1])
			i++
			continue
		}
		if c == ':' {
			fields = append(fields, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	fields = append(fields, cur.String())
	return fields
}

// pgpassFieldMatches honors the libpq "*" wildcard.
func pgpassFieldMatches(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	return pattern == value
}

// parseDSNFields extracts the four pgpass-match fields from a Postgres DSN.
// Accepts URL-form ("postgres://user:pw@host:port/db?...") DSNs. For v1
// keyword/value DSNs ("host=... user=...") are not supported here — they
// can be added later if needed.
func parseDSNFields(dsn string) (host, port, dbname, user string, err error) {
	if dsn == "" {
		return "", "", "", "", errors.New("empty dsn")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", "", "", err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", "", "", "", fmt.Errorf("unsupported dsn scheme %q (want postgres:// or postgresql://)", u.Scheme)
	}
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		port = "5432"
	}
	if host == "" {
		host = "localhost"
	}
	dbname = strings.TrimPrefix(u.Path, "/")
	if u.User != nil {
		user = u.User.Username()
	}
	return host, port, dbname, user, nil
}

// ParseDSNEndpoint extracts the host and database name from a Postgres DSN
// for display purposes (e.g. enriching a connection-picker row). It accepts
// both URL-form ("postgres://user:pw@host:port/db") and keyword/value-form
// ("host=... dbname=... user=... password=...") DSNs via pgconn.ParseConfig.
//
// SECURITY: only the discrete Host and Database fields are returned. The
// password, user, and the raw DSN literal are never surfaced. On parse
// failure the error is deliberately dropped (its text can embed the DSN /
// credentials) and ("","") is returned so callers fall back to a name-only
// render without leaking anything.
func ParseDSNEndpoint(dsn string) (host, db string) {
	if dsn == "" {
		return "", ""
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil || cfg == nil {
		return "", ""
	}
	return cfg.Host, cfg.Database
}

// keyringDir returns the path used for the file-backend keyring store. The
// directory is created lazily by the keyring library on first write; this
// function only computes the path. See env/xdg.DataHome (epic D1).
func keyringDir() string {
	return filepath.Join(xdgDataHome(), "dbsavvy", "keyring")
}
