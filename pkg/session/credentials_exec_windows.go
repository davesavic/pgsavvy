//go:build windows

package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// keyringPassphraseEnv mirrors the constant defined in credentials_exec.go
// for non-Windows builds; duplicated here so each build tag is
// self-contained.
const keyringPassphraseEnv = "PGSAVVY_KEYRING_PASSPHRASE"

const maxStderrBytes = 4 * 1024

// execPasswordCommand runs cmd via `cmd /C` and applies the same env-scrub
// and stderr-handling rules as the POSIX variant.
func execPasswordCommand(ctx context.Context, cmd string) (string, error) {
	c := exec.CommandContext(ctx, "cmd", "/C", cmd)
	c.Env = scrubbedEnv(os.Environ())
	c.Stdin = nil

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	runErr := c.Run()
	if runErr != nil {
		stderrBytes := stderr.Bytes()
		if len(stderrBytes) > maxStderrBytes {
			stderrBytes = stderrBytes[:maxStderrBytes]
		}
		stderrStr := string(stderrBytes)
		if pw := strings.TrimRight(stdout.String(), "\r\n"); pw != "" && strings.Contains(stderrStr, pw) {
			stderrStr = strings.ReplaceAll(stderrStr, pw, "[REDACTED]")
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return "", fmt.Errorf("exit code %d: %s: stderr=%q", exitErr.ExitCode(), runErr, strings.TrimSpace(stderrStr))
		}
		return "", fmt.Errorf("%w: stderr=%q", runErr, strings.TrimSpace(stderrStr))
	}

	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func scrubbedEnv(env []string) []string {
	prefix := keyringPassphraseEnv + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}
