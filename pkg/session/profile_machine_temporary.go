//go:build linux || darwin

package session

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// TemporaryTargetFields contains explicit selectors, never a saved profile or DSN.
// Empty Host selects 127.0.0.1 unless SocketDir is supplied. Port is required even
// for sockets. Callers must distinguish omitted flags from explicitly empty flags.
type TemporaryTargetFields struct {
	Host, SocketDir, Port, Database, Username, Passfile string
}

// TemporaryTarget is a validated value. Its credential path is deliberately private.
type TemporaryTarget struct {
	host, socketDir, database, username, passfile string
	port                                          uint16
	valid                                         bool
}

func temporaryInvalidInput() error {
	return &MachineCredentialError{Kind: "INVALID_INPUT"}
}

func temporarySelector(value string) bool {
	return value != "" && len(value) <= 1<<20 && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func NewTemporaryTarget(fields TemporaryTargetFields) (TemporaryTarget, error) {
	if !temporarySelector(fields.Database) || !temporarySelector(fields.Username) || !temporarySelector(fields.Passfile) || fields.Port == "" || len(fields.Port) > 1<<20 {
		return TemporaryTarget{}, temporaryInvalidInput()
	}
	for _, digit := range fields.Port {
		if digit < '0' || digit > '9' {
			return TemporaryTarget{}, temporaryInvalidInput()
		}
	}
	port, err := strconv.ParseUint(fields.Port, 10, 16)
	if err != nil || port == 0 || (fields.Host != "" && fields.SocketDir != "") {
		return TemporaryTarget{}, temporaryInvalidInput()
	}
	target := TemporaryTarget{database: fields.Database, username: fields.Username, passfile: fields.Passfile, port: uint16(port), valid: true}
	if fields.SocketDir != "" {
		if !temporarySelector(fields.SocketDir) || !filepath.IsAbs(fields.SocketDir) || filepath.Clean(fields.SocketDir) != fields.SocketDir {
			return TemporaryTarget{}, temporaryInvalidInput()
		}
		target.socketDir = fields.SocketDir
	} else {
		host := fields.Host
		if host == "" {
			host = "127.0.0.1"
		}
		address, err := netip.ParseAddr(host)
		if err != nil || address.Zone() != "" || !address.Unmap().IsLoopback() {
			return TemporaryTarget{}, temporaryInvalidInput()
		}
		target.host = address.Unmap().String()
	}
	return target, nil
}

func (t TemporaryTarget) Validate() error {
	if !t.valid {
		return temporaryInvalidInput()
	}
	return nil
}

func (t TemporaryTarget) Host() string      { return t.host }
func (t TemporaryTarget) SocketDir() string { return t.socketDir }
func (t TemporaryTarget) Port() uint16      { return t.port }
func (t TemporaryTarget) Database() string  { return t.database }
func (t TemporaryTarget) Username() string  { return t.username }

// Formatting must not disclose the private passfile path, even with %#v.
func (t TemporaryTarget) String() string   { return "temporary local target" }
func (t TemporaryTarget) GoString() string { return t.String() }

// Keep this list aligned with pgconn.parseEnvSettings in the pinned pgx version.
var temporaryPostgresEnvironment = [...]string{
	"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
	"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT",
	"PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION",
	"PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS",
	"PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION",
}

func ValidateTemporaryEnvironment() error {
	for _, name := range temporaryPostgresEnvironment {
		if os.Getenv(name) != "" {
			return temporaryInvalidInput()
		}
	}
	return nil
}

func validateTemporaryAcquisition(ctx context.Context, target TemporaryTarget) error {
	if ctx.Err() != nil {
		return &MachineCredentialError{Kind: MachineCredentialTimeout}
	}
	if err := target.Validate(); err != nil {
		return err
	}
	return ValidateTemporaryEnvironment()
}

// BuildTemporaryMachineConfig never interprets a saved profile. pgx may stat its
// default paths, but can read credentials only from the explicit /dev/null.
func BuildTemporaryMachineConfig(ctx context.Context, target TemporaryTarget) (*pgx.ConnConfig, error) {
	return buildTemporaryMachineConfig(ctx, target, pgx.ParseConfig)
}

func buildTemporaryMachineConfig(ctx context.Context, target TemporaryTarget, parse func(string) (*pgx.ConnConfig, error)) (*pgx.ConnConfig, error) {
	if err := validateTemporaryAcquisition(ctx, target); err != nil {
		return nil, err
	}
	quote := func(value string) string {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
	}
	host := target.host
	if target.socketDir != "" {
		host = target.socketDir
	}
	connection := "host=" + quote(host) + " port=" + strconv.Itoa(int(target.port)) + " dbname=" + quote(target.database) + " user=" + quote(target.username) + " sslmode=disable passfile=/dev/null target_session_attrs=any min_protocol_version=3.0 max_protocol_version=3.0 channel_binding=disable"
	cfg, err := parse(connection)
	if err != nil || cfg == nil {
		return nil, &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	if err := validateTemporaryAcquisition(ctx, target); err != nil {
		return nil, err
	}
	cfg.Password = ""
	// pgconn splits host on commas even for socket paths. Restore the complete
	// canonical endpoint together with its TLS/fallback settings.
	cfg.Host, cfg.Port, cfg.Database, cfg.User = host, target.port, target.database, target.username
	cfg.Fallbacks = nil
	cfg.TLSConfig = nil
	cfg.ValidateConnect = nil
	cfg.AfterConnect = nil
	cfg.KerberosSrvName, cfg.KerberosSpn = "", ""
	cfg.Tracer, cfg.OnNotice, cfg.OnNotification = nil, nil, nil
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.RuntimeParams = map[string]string{
		"application_name": "pgsavvy-machine", "client_encoding": "UTF8",
		"DateStyle": "ISO", "TimeZone": "UTC", "IntervalStyle": "postgres",
		"bytea_output": "hex", "extra_float_digits": "3", "statement_timeout": "10000",
		"lock_timeout": "1000", "standard_conforming_strings": "on",
	}
	// Numeric loopback hosts must never cause resolver or search-domain access.
	cfg.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if host != target.host || target.socketDir != "" {
			return nil, temporaryInvalidInput()
		}
		return []string{host}, nil
	}
	return cfg, nil
}
