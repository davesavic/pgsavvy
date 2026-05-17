package session

import (
	"context"
	"errors"
)

// errInteractivePromptNotSupported is the sentinel returned by
// TUIRefusePrompter.PromptPassword. It is the final-step refusal in the
// credentials waterfall: every non-interactive mechanism (inline,
// password_command, keyring, pgpass) was either absent or empty, and the
// prompter refuses to take over because dbsavvy is running in TUI mode where a
// blocking stdin read would conflict with the gocui main loop.
//
// The sentinel is intentionally UNEXPORTED. Callers outside this package
// detect it via IsInteractivePromptUnsupported.
var errInteractivePromptNotSupported = errors.New(
	"session: interactive password prompt not supported in TUI mode; " +
		"configure password_command, keyring, or pgpass")

// TUIRefusePrompter is the Prompter installed when dbsavvy runs in TUI mode.
// Every non-prompter step in the ResolvePassword waterfall (inline,
// password_command, keyring, pgpass) continues to work; only the final
// interactive prompt step refuses with errInteractivePromptNotSupported so the
// UI layer can render a toast directing the operator to one of the
// non-interactive mechanisms.
//
// TUIRefusePrompter is a zero-size value; embed or pass it by value.
type TUIRefusePrompter struct{}

// PromptPassword always returns an error wrapping
// errInteractivePromptNotSupported. The hint is included in the error message
// purely for log/debug context; callers SHOULD switch on the predicate
// IsInteractivePromptUnsupported rather than parse the string.
func (TUIRefusePrompter) PromptPassword(_ context.Context, hint string) (string, error) {
	if hint == "" {
		return "", errInteractivePromptNotSupported
	}
	return "", interactivePromptError{hint: hint}
}

// interactivePromptError wraps errInteractivePromptNotSupported with a hint
// label. It satisfies errors.Is for the sentinel so the public predicate works
// regardless of which variant was returned.
type interactivePromptError struct {
	hint string
}

func (e interactivePromptError) Error() string {
	return errInteractivePromptNotSupported.Error() + " (hint: " + e.hint + ")"
}

func (e interactivePromptError) Unwrap() error {
	return errInteractivePromptNotSupported
}

// IsInteractivePromptUnsupported reports whether err (or anything it wraps)
// is the TUI-mode prompt-refusal sentinel emitted by TUIRefusePrompter. The
// toast helper consumes this predicate to render the "configure
// password_command, keyring, or pgpass" guidance.
func IsInteractivePromptUnsupported(err error) bool {
	return errors.Is(err, errInteractivePromptNotSupported)
}
