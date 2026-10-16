//go:build unix

package session

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func machineCredentialHelperCommand(ctx context.Context, mode string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, os.Args[0], machineCredentialHelperArgs(mode, args...)...)
	command.Env = append(scrubbedEnv(os.Environ()), machineHelperFixtureEnv+"=1")
	command.Stderr = io.Discard
	return command
}

func TestMachineCredentialHelperProcess_Unix(t *testing.T) {
	t.Setenv(keyringPassphraseEnv, "synthetic-secret")
	t.Setenv("PGSAVVY_MACHINE_HELPER_VALUE", "retained")
	for _, tc := range []struct{ mode, argument, want string }{
		{"output", "raw λ\r\n", "raw λ\r\n"},
		{"stdin", "", "no-stdin"},
		{"environment", "", "retained"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := machineCredentialHelperCommand(ctx, tc.mode, tc.argument)
		var output bytes.Buffer
		command.Stdout = &output
		err := runMachinePasswordCommand(ctx, command)
		cancel()
		if err != nil || output.String() != tc.want {
			t.Fatalf("Unix delegate fixture %s failed", tc.mode)
		}
	}
}
