//go:build !windows

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

// keyringPassphraseEnv is the env var consulted for the keyring file-backend
// passphrase. Documented in DESIGN.md §11.2 (epic D2). It is also stripped
// from password_command child env so external helpers cannot exfiltrate it.
const keyringPassphraseEnv = "DBSAVVY_KEYRING_PASSPHRASE"

// maxStderrBytes caps the size of stderr included in password_command exit
// errors so a runaway helper cannot blow up an error message.
const maxStderrBytes = 4 * 1024

// execPasswordCommand runs cmd via a POSIX shell with the user's
// environment (minus DBSAVVY_KEYRING_PASSPHRASE), enforces ctx as the
// timeout, and returns trimmed stdout on exit 0.
//
// Shell resolution: $SHELL → /bin/bash → /bin/sh. If none exist as
// executables, returns errNoUsableShell.
//
// On non-zero exit, the returned error includes the exit code and the
// child's stderr capped at maxStderrBytes, substring-scrubbed to redact the
// resolved password (if it happened to leak there).
//
// On exit 0, stderr is silently discarded — we never log or echo it
// (epic Critic resolution: avoid noise that could carry secret material).
func execPasswordCommand(ctx context.Context, cmd string) (string, error) {
	shell, err := resolvePosixShell()
	if err != nil {
		return "", err
	}

	c := exec.CommandContext(ctx, shell, "-c", cmd)
	c.Env = scrubbedEnv(os.Environ())
	c.Stdin = nil // explicit: no stdin

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
		// Scrub: if the (would-be) password appears in stderr, redact it.
		// We only know the candidate password from stdout; trim and use it.
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
