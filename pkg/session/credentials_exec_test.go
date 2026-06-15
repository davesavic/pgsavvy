//go:build !windows

package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestExecPasswordCommand_TrimsTrailingNewlines(t *testing.T) {
	got, err := execPasswordCommand(context.Background(), "printf 'pw\\n\\n'")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pw" {
		t.Fatalf("got %q want pw", got)
	}
}

func TestPasswordCommandChildEnvLacksKeyringPassphrase(t *testing.T) {
	t.Setenv(keyringPassphraseEnv, "should-not-leak")
	out, err := execPasswordCommand(context.Background(), "env")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(out, keyringPassphraseEnv+"=") {
		t.Fatalf("env scrub failed; child saw %q in env output: %s", keyringPassphraseEnv, out)
	}
	if strings.Contains(out, "should-not-leak") {
		t.Fatalf("env scrub failed; secret value leaked: %s", out)
	}
}

func TestPasswordCommandStderrScrub(t *testing.T) {
	// stdout=hunter2, stderr echoes "hunter2 leaked" then exits non-zero.
	cmd := `printf hunter2; printf 'hunter2 leaked\n' 1>&2; exit 1`
	_, err := execPasswordCommand(context.Background(), cmd)
	if err == nil {
		t.Fatal("expected error from non-zero exit")
	}
	msg := err.Error()
	if strings.Contains(msg, "hunter2") {
		t.Fatalf("stderr scrub failed; password leaked in error: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker in error: %s", msg)
	}
}

func TestPasswordCommandHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := execPasswordCommand(ctx, "sleep 5")
	if err == nil {
		t.Fatal("expected error from ctx deadline")
	}
}

func TestResolvePassword_PasswordCommandViaWaterfall(t *testing.T) {
	profile := models.Connection{Name: "p", PasswordCommand: "printf via-cmd"}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "via-cmd" {
		t.Fatalf("got %q", got)
	}
}
