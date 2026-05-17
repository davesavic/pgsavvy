package session

import (
	"context"
	"errors"
	"testing"
)

func TestTerminalPrompterNonTTY(t *testing.T) {
	// In `go test`, os.Stdin is not a TTY. PromptPassword must return
	// errNoTTY immediately rather than block.
	_, err := TerminalPrompter{}.PromptPassword(context.Background(), "test")
	if !errors.Is(err, errNoTTY) {
		t.Fatalf("expected errNoTTY, got %v", err)
	}
}
