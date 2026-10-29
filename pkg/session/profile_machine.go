//go:build linux || darwin || windows

package session

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// BuildMachineConfig parses once using existing trusted profile semantics.
// Parsing pool settings preserves DSN compatibility but creates no pool.
func BuildMachineConfig(ctx context.Context, profile models.Connection) (*pgx.ConnConfig, error) {
	parsed, err := buildPgxConfig(ctx, profile, "", io.Discard)
	if err != nil {
		return nil, &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	cfg := parsed.ConnConfig.Copy()
	cfg.Tracer = nil
	cfg.OnNotice = nil
	cfg.OnNotification = nil
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	for name, value := range map[string]string{"application_name": "pgsavvy-machine", "client_encoding": "UTF8", "DateStyle": "ISO", "TimeZone": "UTC", "IntervalStyle": "postgres", "bytea_output": "hex", "extra_float_digits": "3", "statement_timeout": strconv.Itoa(MachineStatementMilliseconds(profile.StatementTimeout, 10000)), "lock_timeout": "1000"} {
		cfg.RuntimeParams[name] = value
	}
	return cfg, nil
}

// MachineStatementMilliseconds retains a shorter saved profile limit. ceilingMS
// must be positive and fit PostgreSQL's signed 32-bit millisecond representation.
func MachineStatementMilliseconds(raw string, ceilingMS int) int {
	canonical, err := CanonicalizeStatementTimeout(raw)
	if raw == "" || err != nil || canonical == "0" {
		return ceilingMS
	}
	index := strings.IndexFunc(canonical, func(r rune) bool { return r < '0' || r > '9' })
	if index < 0 {
		return ceilingMS
	}
	n, err := strconv.ParseUint(canonical[:index], 10, 64)
	if err != nil || n == 0 {
		return ceilingMS
	}
	multiplier := map[string]uint64{"ms": 1, "s": 1000, "min": 60000, "h": 3600000, "d": 86400000}[canonical[index:]]
	if n > uint64(ceilingMS)/multiplier {
		return ceilingMS
	}
	return int(n * multiplier)
}
