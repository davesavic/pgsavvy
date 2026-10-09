package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestCredentialHelperOutput(t *testing.T) {
	if os.Getenv("PGSAVVY_TEST_CREDENTIAL_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("PGSAVVY_TEST_HELPER_STDOUT"))
	_, _ = os.Stderr.WriteString(os.Getenv("PGSAVVY_TEST_HELPER_STDERR"))
	os.Exit(7)
}

func TestPasswordCommandFailureOmitsOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot locate synthetic helper")
	}
	command := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=TestCredentialHelperOutput"
	if runtime.GOOS == "windows" {
		command = `"` + executable + `" -test.run=TestCredentialHelperOutput`
	}
	const secret = "synthetic-helper-secret"
	const oldStderrLimit = 4096
	for _, tt := range []struct {
		name   string
		stdout string
		stderr string
	}{
		{"stderr_only", "", secret},
		{"stdout_only", secret, ""},
		{"before_old_limit", secret, secret + strings.Repeat("x", oldStderrLimit)},
		{"across_old_limit", secret, strings.Repeat("x", oldStderrLimit-10) + secret},
		{"after_old_limit", secret, strings.Repeat("x", oldStderrLimit+10) + secret},
		{"repeated", secret, strings.Repeat(secret, 300)},
		{"whitespace", "  " + secret + "  \r\n", "\t" + secret + "\n"},
		{"unknown_stderr_secret", "different-synthetic-value", secret},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PGSAVVY_TEST_CREDENTIAL_HELPER", "1")
			t.Setenv("PGSAVVY_TEST_HELPER_STDOUT", tt.stdout)
			t.Setenv("PGSAVVY_TEST_HELPER_STDERR", tt.stderr)
			password, err := ResolvePassword(context.Background(), models.Connection{PasswordCommand: command}, nil)
			if err == nil || password != "" {
				t.Fatal("failed helper must return an error and no password")
			}
			if !strings.Contains(err.Error(), "exit code 7") {
				t.Fatal("helper error lost its exit code")
			}
			if len(err.Error()) > 256 {
				t.Fatal("helper output enlarged the error diagnostic")
			}
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			logs.Event(logger, "db", "conn_open_done", slog.Any("err", RedactConnectionString(err.Error())))
			for _, diagnostic := range []string{err.Error(), output.String()} {
				for _, forbidden := range []string{"synthetic", "different-synthetic-value", "stderr=", strings.Repeat("x", 10)} {
					if strings.Contains(diagnostic, forbidden) {
						t.Fatal("helper output leaked into a returned or logged diagnostic")
					}
				}
			}
			if !strings.Contains(output.String(), "conn_open_done") || !strings.Contains(output.String(), "exit code 7") {
				t.Fatal("downstream log lost its event or exit diagnostic")
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
				t.Fatal("helper error lost its exit-status classification")
			}
		})
	}
}
