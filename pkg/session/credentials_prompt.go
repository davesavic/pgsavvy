package session

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/term"
)

// TerminalPrompter is the default Prompter that reads a password from
// /dev/stdin with echo disabled via golang.org/x/term.ReadPassword.
//
// When stdin is not a TTY (CI, pipes, headless services), PromptPassword
// returns errNoTTY immediately — it does NOT block waiting for input.
//
// Context handling: the underlying ReadPassword call is blocking and does
// not honor ctx cancellation. Callers needing strict ctx semantics should
// wrap with a goroutine or run the prompt under a deadline they control.
type TerminalPrompter struct{}

// PromptPassword writes "<hint>: " to stderr and reads a password from
// stdin with echo suppressed. See TerminalPrompter docs.
func (TerminalPrompter) PromptPassword(_ context.Context, hint string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errNoTTY
	}
	if _, err := fmt.Fprintf(os.Stderr, "%s: ", hint); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	pw, err := term.ReadPassword(fd)
	// Force a newline after the (suppressed) input so subsequent terminal
	// output isn't glued to the prompt.
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}
