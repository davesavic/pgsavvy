//go:build unix

package session

import (
	"context"
	"os/exec"
)

func runMachinePasswordCommand(ctx context.Context, command *exec.Cmd) error {
	return runPasswordCommand(ctx, command)
}
