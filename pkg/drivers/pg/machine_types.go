//go:build linux || darwin

package pg

import (
	"context"
	"net"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

type MachineError struct {
	Code     string
	SQLState string
}

func (e *MachineError) Error() string { return e.Code }

type MachineOptions struct {
	RequiredAccess   models.CLIAccess
	Credentials      session.MachineCredentialOptions
	ParseConfig      func(context.Context, models.Connection) (*pgx.ConnConfig, error)
	Dial             func(context.Context, string, string) (net.Conn, error)
	ConnectTimeout   time.Duration
	StatementTimeout time.Duration
	CleanupTimeout   time.Duration
}
