//go:build linux || darwin

package session

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// ResolveMachinePassword uses the single already-parsed target, never a second
// interpretation of profile fields, and never an interactive fallback.
func ResolveMachinePassword(ctx context.Context, profile models.Connection, target *pgconn.Config, options MachineCredentialOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", &MachineCredentialError{Kind: MachineCredentialTimeout}
	}
	if target == nil {
		return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	if profile.Password != "" {
		return profile.Password, nil
	}
	if profile.PasswordCommand != "" {
		execute := options.Exec
		if execute == nil {
			execute = func(ctx context.Context, command string) (string, error) {
				return ExecMachineCredential(ctx, command, MachineCredentialLimits{})
			}
		}
		password, err := execute(ctx, profile.PasswordCommand)
		if err != nil {
			return "", machineCredentialError(err)
		}
		if password != "" {
			return password, nil
		}
	}
	if profile.KeyringRef != "" {
		lookup := options.Keyring
		if lookup == nil {
			lookup = resolveMachineKeyring
		}
		password, err := lookup(ctx, profile.KeyringRef)
		if err != nil {
			return "", machineCredentialError(err)
		}
		if password != "" {
			return password, nil
		}
	}
	if profile.PgpassPath != "" {
		password, err := machinePgpass(profile.PgpassPath, target)
		if err != nil {
			return "", machineCredentialError(err)
		}
		if password != "" {
			return password, nil
		}
	}
	return target.Password, nil
}

func machineCredentialError(err error) *MachineCredentialError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &MachineCredentialError{Kind: MachineCredentialTimeout}
	}
	var machine *MachineCredentialError
	if errors.As(err, &machine) {
		return machine
	}
	return &MachineCredentialError{Kind: MachineCredentialUnavailable}
}

func resolveMachineKeyring(ctx context.Context, ref string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	item, err := loadKeyringItem(ref, func(string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if password := os.Getenv(keyringPassphraseEnv); password != "" {
			return password, nil
		}
		return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}, io.Discard)
	if err != nil {
		return "", machineCredentialError(err)
	}
	// Native filename aliases must not resolve a different profile's secret.
	if item.Key != ref {
		return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	return string(item.Data), nil
}

func machinePgpass(path string, target *pgconn.Config) (string, error) {
	data, err := readMachinePgpassFile(path)
	if err != nil {
		return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	endpoints := []*pgconn.FallbackConfig{{Host: target.Host, Port: target.Port}}
	endpoints = append(endpoints, target.Fallbacks...)
	password := ""
	for index, endpoint := range endpoints {
		if endpoint == nil {
			return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
		}
		host := endpoint.Host
		if network, _ := pgconn.NetworkAddress(host, endpoint.Port); network == "unix" {
			host = "localhost"
		}
		matched := false
		candidate := ""
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := splitPgpassLine(line)
			if len(fields) != 5 {
				continue
			}
			if pgpassFieldMatches(fields[0], host) && pgpassFieldMatches(fields[1], strconv.Itoa(int(endpoint.Port))) && pgpassFieldMatches(fields[2], target.Database) && pgpassFieldMatches(fields[3], target.User) {
				candidate = fields[4]
				matched = true
				break
			}
		}
		if !matched {
			return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
		}
		if index > 0 && candidate != password {
			return "", &MachineCredentialError{Kind: MachinePgpassFailover}
		}
		password = candidate
	}
	return password, nil
}
