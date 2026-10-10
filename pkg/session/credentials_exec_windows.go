//go:build windows

package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// keyringPassphraseEnv mirrors the constant defined in credentials_exec.go
// for non-Windows builds; duplicated here so each build tag is
// self-contained.
const keyringPassphraseEnv = "PGSAVVY_KEYRING_PASSPHRASE"

// execPasswordCommand runs cmd via `cmd /C` and applies the same env-scrub
// and stderr-handling rules as the POSIX variant.
func execPasswordCommand(ctx context.Context, cmd string) (string, error) {
	c := exec.CommandContext(ctx, "cmd", "/C", cmd)
	c.Env = scrubbedEnv(os.Environ())
	c.Stdin = nil

	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = io.Discard

	runErr := c.Run()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return "", fmt.Errorf("exit code %d: %w", exitErr.ExitCode(), runErr)
		}
		return "", runErr
	}

	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func scrubbedEnv(env []string) []string {
	return scrubCredentialEnv(env, true)
}
