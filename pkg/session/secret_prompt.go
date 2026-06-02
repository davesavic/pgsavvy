package session

import (
	"context"
	"errors"
)

// SecretPrompter returns a secret string obtained from MASKED interactive
// input. Unlike the credentials waterfall's Prompter (which TUIRefusePrompter
// deliberately refuses), a SecretPrompter is the seam used when an interactive
// masked entry IS available — e.g. an SSH key passphrase typed into the TUI
// popup. Implementations MUST respect ctx cancellation.
//
// hint is a short human-readable label (e.g. "passphrase for id_ed25519").
// Returning ("", nil) means "the user submitted an empty secret"; the caller
// decides whether that is valid. This is distinct from the two typed errors:
//
//   - errSecretPromptCancelled  — the user dismissed the prompt (Esc/cancel).
//   - errSecretPromptUnsupported — no interactive masked entry is available
//     (headless / no-TTY). The default UnsupportedSecretPrompter returns this
//     immediately and never blocks.
type SecretPrompter interface {
	PromptSecret(ctx context.Context, hint string) (string, error)
}

// Unexported sentinels, mirroring the credentials.go / credentials_tui.go
// pattern. Callers outside this package use the IsSecretPrompt* predicates.
// These are intentionally distinct from errInteractivePromptNotSupported
// (credentials_tui.go) so a masked-prompt cancellation/absence is never
// mistaken for the TUIRefusePrompter waterfall refusal.
var (
	errSecretPromptCancelled = errors.New(
		"session: masked secret prompt cancelled by user")
	errSecretPromptUnsupported = errors.New(
		"session: masked secret prompt not supported (no interactive TTY)")
	errSecretPromptBusy = errors.New(
		"session: masked secret prompt already in progress")
)

// NewSecretPromptCancelled returns the user-cancellation error. cause (may be
// nil — e.g. an Esc dismissal) is wrapped for debug context (e.g. the ctx
// error on ctx cancellation); IsSecretPromptCancelled still matches via the
// errSecretPromptCancelled sentinel. Exported so the TUI SecretPrompter (which
// lives in pkg/gui) can emit the canonical typed error without re-deriving the
// unexported sentinel.
func NewSecretPromptCancelled(cause error) error {
	if cause == nil {
		return errSecretPromptCancelled
	}
	return secretPromptCancelledError{cause: cause}
}

// secretPromptCancelledError wraps errSecretPromptCancelled with an optional
// cause. It satisfies errors.Is for the sentinel so IsSecretPromptCancelled
// works regardless of variant, mirroring interactivePromptError in
// credentials_tui.go.
type secretPromptCancelledError struct {
	cause error
}

func (e secretPromptCancelledError) Error() string {
	return errSecretPromptCancelled.Error() + ": " + e.cause.Error()
}

func (e secretPromptCancelledError) Is(target error) bool {
	return target == errSecretPromptCancelled
}

func (e secretPromptCancelledError) Unwrap() error { return e.cause }

// IsSecretPromptCancelled reports whether err (or anything it wraps) is the
// user-cancellation sentinel returned by a SecretPrompter on Esc/cancel.
func IsSecretPromptCancelled(err error) bool {
	return errors.Is(err, errSecretPromptCancelled)
}

// IsSecretPromptUnsupported reports whether err (or anything it wraps) is the
// no-interactive-entry sentinel returned by UnsupportedSecretPrompter (and any
// other headless SecretPrompter).
func IsSecretPromptUnsupported(err error) bool {
	return errors.Is(err, errSecretPromptUnsupported)
}

// NewSecretPromptBusy returns the overlapping-prompt sentinel: a second
// PromptSecret while one is already in flight returns this immediately rather
// than clobbering the single shared popup surface. Exported so the TUI
// SecretPrompter (pkg/gui) can emit the canonical typed error.
func NewSecretPromptBusy() error { return errSecretPromptBusy }

// IsSecretPromptBusy reports whether err is the overlapping-prompt sentinel.
func IsSecretPromptBusy(err error) bool {
	return errors.Is(err, errSecretPromptBusy)
}

// UnsupportedSecretPrompter is the headless default SecretPrompter. Its
// PromptSecret returns errSecretPromptUnsupported immediately and NEVER blocks,
// so non-TUI / headless callers fail fast rather than hanging on a prompt that
// can never be answered. Zero-size value; pass it by value.
type UnsupportedSecretPrompter struct{}

// PromptSecret always returns ("", errSecretPromptUnsupported).
func (UnsupportedSecretPrompter) PromptSecret(_ context.Context, _ string) (string, error) {
	return "", errSecretPromptUnsupported
}
