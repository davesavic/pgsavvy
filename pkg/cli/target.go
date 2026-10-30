//go:build linux || darwin

package cli

import (
	"context"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/session"
)

var temporaryTargetFlags = []string{"host", "socket-dir", "port", "database", "username", "passfile"}

func selectTemporaryTarget(flags map[string]string, query bool) (*session.TemporaryTarget, error) {
	_, temporary := flags["temporary"]
	_, write := flags["allow-write"]
	bad := &PublicError{Code: InvalidInput}
	if !temporary {
		for _, key := range temporaryTargetFlags {
			if _, present := flags[key]; present {
				return nil, bad
			}
		}
		if write {
			return nil, bad
		}
		return nil, nil
	}
	for _, key := range []string{"connection", "approve", "confirm-connection"} {
		if _, present := flags[key]; present {
			return nil, bad
		}
	}
	if write && !query {
		return nil, bad
	}
	for _, key := range temporaryTargetFlags {
		value, present := flags[key]
		if present && value == "" || !present && key != "host" && key != "socket-dir" {
			return nil, bad
		}
	}
	_, host := flags["host"]
	_, socket := flags["socket-dir"]
	if host && socket {
		return nil, bad
	}
	target, err := session.NewTemporaryTarget(session.TemporaryTargetFields{Host: flags["host"], SocketDir: flags["socket-dir"], Port: flags["port"], Database: flags["database"], Username: flags["username"], Passfile: flags["passfile"]})
	if err != nil {
		return nil, bad
	}
	// Reserve room for bounded error/outcome evidence even if result encoding fails.
	probe, err := encode(Envelope{Command: "query", Target: publicTemporaryTarget(target, true), Error: &PublicError{Code: ResourceFailure}})
	if err != nil || len(probe) > MaxOutputBytes-4096 {
		return nil, bad
	}
	return &target, nil
}

func publicTemporaryTarget(target session.TemporaryTarget, readOnly bool) *temporaryTargetMetadata {
	transport := "tcp"
	if target.SocketDir() != "" {
		transport = "unix"
	}
	return &temporaryTargetMetadata{Source: "temporary", Transport: transport, Host: target.Host(), SocketDir: target.SocketDir(), Port: target.Port(), Database: target.Database(), Username: target.Username(), ReadOnly: readOnly}
}

type temporaryMachine interface {
	ExecuteTemporaryAtomic(context.Context, pg.ValidatedTemporaryScript, [][]byte, pg.TemporaryAtomicOptions, pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error)
	Inspect(context.Context, pg.MachineMetadataRequest) (pg.MachineMetadata, error)
	Ping(context.Context) error
	Close(context.Context) error
}

func (s InvocationService) openTemporaryTarget(ctx context.Context, target session.TemporaryTarget) (temporaryMachine, error) {
	options := s.Options
	options.CleanupTimeout = min(CleanupTimeout, CleanupBudget(ctx))
	if timeout, ok := ctx.Value(statementTimeoutKey{}).(time.Duration); ok {
		options.StatementTimeout = timeout
	}
	if s.openTemporary != nil {
		client, err := s.openTemporary(ctx, target, options)
		if err != nil {
			return nil, PublicMachineError(err)
		}
		return client, nil
	}
	client, err := pg.OpenTemporaryMachine(ctx, target, options)
	if err != nil {
		return nil, PublicMachineError(err)
	}
	return client, nil
}
