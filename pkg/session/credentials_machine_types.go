package session

import (
	"context"
	"time"
)

type MachineCredentialFailure string

const (
	MachineCredentialUnavailable MachineCredentialFailure = "CREDENTIALS_UNAVAILABLE"
	MachineCredentialTimeout     MachineCredentialFailure = "TIMEOUT"
	MachineCredentialOutputLimit MachineCredentialFailure = "RESOURCE_FAILURE"
	MachinePgpassFailover        MachineCredentialFailure = "UNSUPPORTED_PGPASS_FAILOVER"
)

type MachineCredentialError struct{ Kind MachineCredentialFailure }

func (e *MachineCredentialError) Error() string { return string(e.Kind) }

type (
	MachineCredentialLimits struct {
		Timeout   time.Duration
		MaxStdout int
	}
	MachineCredentialOptions struct {
		Exec    func(context.Context, string) (string, error)
		Keyring func(context.Context, string) (string, error)
	}
)
