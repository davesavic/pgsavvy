//go:build linux

package session

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type machineHelperOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *machineHelperOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.overflow = true
		b.cancel()
		return 0, &MachineCredentialError{Kind: MachineCredentialOutputLimit}
	}
	return b.buffer.Write(p)
}

// ExecMachineCredential executes only commands from trusted saved profiles.
// It reuses the legacy owned-process-group supervisor, with machine-only caps.
func ExecMachineCredential(parent context.Context, command string, limits MachineCredentialLimits) (string, error) {
	if limits.Timeout == 0 {
		limits.Timeout = 5 * time.Second
	}
	if limits.MaxStdout == 0 {
		limits.MaxStdout = 32 << 10
	}
	if command == "" || limits.Timeout <= 0 || limits.Timeout > 5*time.Second || limits.MaxStdout < 1 || limits.MaxStdout > 32<<10 {
		return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	ctx, cancel := context.WithTimeout(parent, limits.Timeout)
	defer cancel()
	shell, err := resolvePosixShell()
	if err != nil {
		return "", &MachineCredentialError{Kind: MachineCredentialUnavailable}
	}
	c := exec.CommandContext(ctx, shell, "-c", command)
	c.Env = scrubbedEnv(os.Environ())
	c.Stdin = nil
	c.Stderr = io.Discard
	stdout := &machineHelperOutput{limit: limits.MaxStdout, cancel: cancel}
	c.Stdout = stdout
	err = runPasswordCommand(ctx, c)
	if stdout.overflow {
		return "", &MachineCredentialError{Kind: MachineCredentialOutputLimit}
	}
	if err != nil {
		return "", machineCredentialError(err)
	}
	return strings.TrimRight(stdout.buffer.String(), "\r\n"), nil
}
