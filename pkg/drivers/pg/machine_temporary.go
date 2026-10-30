//go:build linux || darwin

package pg

import (
	"context"
	"strconv"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// OpenTemporaryMachine accepts no saved profile. Only Dial and the timeout fields
// of options are used; ParseConfig, Credentials and RequiredAccess are saved-only.
// Native Execute stays denied; temporary scripts require the separate atomic API.
func OpenTemporaryMachine(ctx context.Context, target session.TemporaryTarget, options MachineOptions) (*MachineClient, error) {
	return openTemporaryMachine(ctx, target, options, session.ResolveTemporaryMachinePassword)
}

func openTemporaryMachine(ctx context.Context, target session.TemporaryTarget, options MachineOptions, password func(context.Context, session.TemporaryTarget) (string, error)) (*MachineClient, error) {
	if ctx.Err() != nil {
		return nil, &MachineError{Code: "TIMEOUT"}
	}
	if err := target.Validate(); err != nil {
		return nil, &MachineError{Code: "INVALID_INPUT"}
	}
	if err := session.ValidateTemporaryEnvironment(); err != nil {
		return nil, &MachineError{Code: "INVALID_INPUT"}
	}
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = 10 * time.Second
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = 2 * time.Second
	}
	if options.StatementTimeout == 0 {
		options.StatementTimeout = 10 * time.Second
	}
	if options.ConnectTimeout <= 0 || options.ConnectTimeout > 10*time.Second || options.CleanupTimeout <= 0 || options.CleanupTimeout > 2*time.Second || options.StatementTimeout <= 0 || options.StatementTimeout > (1<<31-1)*time.Millisecond {
		return nil, &MachineError{Code: "INVALID_INPUT"}
	}
	connectCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
	defer cancel()
	cfg, err := session.BuildTemporaryMachineConfig(connectCtx, target)
	if err != nil {
		return nil, machineFailure(connectCtx, err, "CONNECTION_UNAVAILABLE")
	}
	cfg.Password, err = password(connectCtx, target)
	if err != nil {
		return nil, machineFailure(connectCtx, err, "CREDENTIALS_UNAVAILABLE")
	}
	if cfg.Password == "" {
		return nil, &MachineError{Code: "CREDENTIALS_UNAVAILABLE"}
	}
	statementMS := int((options.StatementTimeout + time.Millisecond - 1) / time.Millisecond)
	cfg.RuntimeParams["statement_timeout"] = strconv.Itoa(statementMS)
	client := &MachineClient{access: models.CLIAccessRowRead, temporary: true, statementMS: statementMS, cleanup: options.CleanupTimeout, dialAllowed: true}
	// The empty profile cannot activate saved role, SSH or credentials. Do not set
	// default_transaction_read_only: the atomic API owns each transaction's mode.
	return connectMachine(ctx, connectCtx, models.Connection{}, options, cfg, client)
}
