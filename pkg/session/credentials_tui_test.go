package session

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestTUIRefusePrompter_PromptPasswordReturnsSentinel(t *testing.T) {
	_, err := TUIRefusePrompter{}.PromptPassword(context.Background(), "password for prod")
	if err == nil {
		t.Fatal("expected error from TUIRefusePrompter.PromptPassword")
	}
	if !IsInteractivePromptUnsupported(err) {
		t.Fatalf("IsInteractivePromptUnsupported = false; want true (err=%v)", err)
	}
	// The wrapped variant must also unwrap to the bare sentinel via errors.Is.
	if !errors.Is(err, errInteractivePromptNotSupported) {
		t.Fatalf("errors.Is(err, errInteractivePromptNotSupported) = false (err=%v)", err)
	}
	// Defense in depth: not confusable with errNoTTY.
	if errors.Is(err, errNoTTY) {
		t.Fatalf("err must not be errNoTTY; got %v", err)
	}
}

func TestTUIRefusePrompter_PromptPasswordEmptyHint(t *testing.T) {
	_, err := TUIRefusePrompter{}.PromptPassword(context.Background(), "")
	if !IsInteractivePromptUnsupported(err) {
		t.Fatalf("empty hint should still return the sentinel; err=%v", err)
	}
}

func TestIsInteractivePromptUnsupportedNilAndOthers(t *testing.T) {
	if IsInteractivePromptUnsupported(nil) {
		t.Fatal("nil err should not be reported as interactive-prompt-unsupported")
	}
	if IsInteractivePromptUnsupported(errors.New("unrelated")) {
		t.Fatal("unrelated err should not be reported as interactive-prompt-unsupported")
	}
}

func TestResolvePassword_TUIRefusePrompterRefusesAtPromptStep(t *testing.T) {
	// No inline / password_command / keyring / pgpass → waterfall lands on
	// the prompter, which refuses.
	profile := models.Connection{Name: "p"}
	_, err := ResolvePassword(context.Background(), profile, TUIRefusePrompter{})
	if err == nil {
		t.Fatal("expected error from waterfall when TUI prompter is the only step")
	}
	if !IsInteractivePromptUnsupported(err) {
		t.Fatalf("expected IsInteractivePromptUnsupported(err)==true, got err=%v", err)
	}
}

func TestResolvePassword_TUIRefusePrompterDoesNotBlockPasswordCommand(t *testing.T) {
	// password_command satisfies the waterfall before the prompter is reached.
	profile := models.Connection{Name: "p", PasswordCommand: "printf hunter2"}
	got, err := ResolvePassword(context.Background(), profile, TUIRefusePrompter{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q want hunter2", got)
	}
}

func TestKeyringPassphraseUnsetInTUIRefuses(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	// Seed a keyring item using its OWN passphrase via the test helper —
	// our code-under-test is the passphraseFunc returning the typed error
	// BEFORE it ever opens the store via the prompter path.
	seedKeyring(t, tmp+"/pgsavvy/keyring", "phrase", "k", "v")

	// Ensure env is unset.
	t.Setenv(keyringPassphraseEnv, "")
	_ = os.Unsetenv(keyringPassphraseEnv)

	profile := models.Connection{Name: "p", KeyringRef: "k"}
	_, err := ResolvePassword(context.Background(), profile, TUIRefusePrompter{})
	if err == nil {
		t.Fatal("expected typed keyring-TUI refusal")
	}
	if !IsKeyringPassphraseRequiredInTUI(err) {
		t.Fatalf("IsKeyringPassphraseRequiredInTUI = false; got err=%v", err)
	}
	// MUST NOT be confused with the generic interactive-prompt refusal.
	if IsInteractivePromptUnsupported(err) {
		t.Fatalf("keyring-TUI refusal must be DISTINCT from interactive-prompt refusal; got err=%v", err)
	}
}

func TestPasswordCommandEmptyOutputRefuses(t *testing.T) {
	// A password_command that exits 0 with empty stdout MUST fall through to
	// the next waterfall step, NOT silently succeed with an empty password.
	// When the next step is a refusing prompter, the caller receives the
	// refusal sentinel — proving the empty stdout was not interpreted as a
	// valid credential.
	profile := models.Connection{
		Name:            "p",
		PasswordCommand: "printf ''",
	}
	_, err := ResolvePassword(context.Background(), profile, TUIRefusePrompter{})
	if err == nil {
		t.Fatal("expected refusal when password_command emits empty stdout")
	}
	if !IsInteractivePromptUnsupported(err) {
		t.Fatalf("empty password_command stdout should fall through to the prompter; got %v", err)
	}

	// Sanity: with no prompter at all, empty stdout returns the
	// auto-discovery sentinel ("", nil) — proving the fallthrough is real.
	got, err := ResolvePassword(context.Background(), models.Connection{
		Name:            "p2",
		PasswordCommand: "printf ''",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected err with nil prompter: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q want empty sentinel", got)
	}

	// Sanity: with a stub prompter, the prompter IS reached after the empty
	// password_command — additional proof the empty result was not silently
	// accepted upstream.
	stub := &fakePrompter{value: "from-prompter"}
	got, err = ResolvePassword(context.Background(), models.Connection{
		Name:            "p3",
		PasswordCommand: "printf ''",
	}, stub)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "from-prompter" {
		t.Fatalf("got %q want from-prompter", got)
	}
	if stub.calls != 1 {
		t.Fatalf("expected exactly 1 prompter call, got %d", stub.calls)
	}
}
