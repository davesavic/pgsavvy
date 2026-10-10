//go:build !windows

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

// keyringPassphraseEnv is the env var consulted for the keyring file-backend
// passphrase. Documented in DESIGN.md §11.2 (epic D2). It is also stripped
// from password_command child env so external helpers cannot exfiltrate it.
const keyringPassphraseEnv = "PGSAVVY_KEYRING_PASSPHRASE"

// execPasswordCommand runs cmd via a POSIX shell with the user's
// environment (minus PGSAVVY_KEYRING_PASSPHRASE), enforces ctx as the
// timeout, and returns trimmed stdout on exit 0.
//
// Shell resolution: $SHELL → /bin/bash → /bin/sh. If none exist as
// executables, returns errNoUsableShell.
//
// Errors retain exit and cancellation diagnostics, never helper output.
// Stderr is always discarded: it can contain secrets absent from stdout,
// including partial credentials that substring scrubbing cannot recognize.
func execPasswordCommand(ctx context.Context, cmd string) (string, error) {
	shell, err := resolvePosixShell()
	if err != nil {
		return "", err
	}

	c := exec.CommandContext(ctx, shell, "-c", cmd)
	c.Env = scrubbedEnv(os.Environ())
	c.Stdin = nil // explicit: no stdin

	var stdout bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = io.Discard

	runErr := runPasswordCommand(ctx, c)
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return "", fmt.Errorf("exit code %d: %w", exitErr.ExitCode(), runErr)
		}
		return "", runErr
	}

	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// resolvePosixShell picks the first usable shell.
func resolvePosixShell() (string, error) {
	candidates := []string{os.Getenv("SHELL"), "/bin/bash", "/bin/sh"}
	for _, sh := range candidates {
		if sh == "" {
			continue
		}
		if info, err := os.Stat(sh); err == nil && !info.IsDir() {
			return sh, nil
		}
	}
	return "", errNoUsableShell
}

// scrubbedEnv returns a copy of env with the keyring-passphrase variable
// removed. This prevents helper processes from observing it via /proc/self/environ
// (epic Critic resolution).
func scrubbedEnv(env []string) []string {
	return scrubCredentialEnv(env, false)
}
