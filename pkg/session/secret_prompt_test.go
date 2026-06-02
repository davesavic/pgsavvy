package session

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedSecretPrompter_ReturnsUnsupportedWithoutBlocking(t *testing.T) {
	var p SecretPrompter = UnsupportedSecretPrompter{}
	got, err := p.PromptSecret(context.Background(), "ssh passphrase")
	if got != "" {
		t.Errorf("value = %q, want empty", got)
	}
	if !IsSecretPromptUnsupported(err) {
		t.Fatalf("IsSecretPromptUnsupported = false for err %v; want true", err)
	}
}

func TestSecretPromptPredicates_AreDistinct(t *testing.T) {
	unsupported := UnsupportedSecretPrompter{}
	_, unsupportedErr := unsupported.PromptSecret(context.Background(), "hint")

	cancelledErr := errSecretPromptCancelled

	// Unsupported is unsupported, not cancelled, not the refusal sentinel.
	if !IsSecretPromptUnsupported(unsupportedErr) {
		t.Error("unsupported err not recognised as unsupported")
	}
	if IsSecretPromptCancelled(unsupportedErr) {
		t.Error("unsupported err mis-recognised as cancelled")
	}
	if IsInteractivePromptUnsupported(unsupportedErr) {
		t.Error("unsupported secret err mis-recognised as TUIRefuse refusal")
	}

	// Cancelled is cancelled, not unsupported, not the refusal sentinel.
	if !IsSecretPromptCancelled(cancelledErr) {
		t.Error("cancelled err not recognised as cancelled")
	}
	if IsSecretPromptUnsupported(cancelledErr) {
		t.Error("cancelled err mis-recognised as unsupported")
	}
	if IsInteractivePromptUnsupported(cancelledErr) {
		t.Error("cancelled secret err mis-recognised as TUIRefuse refusal")
	}

	// The TUIRefuse refusal must not be confused for either secret error.
	refusal := errInteractivePromptNotSupported
	if IsSecretPromptUnsupported(refusal) {
		t.Error("TUIRefuse refusal mis-recognised as secret-unsupported")
	}
	if IsSecretPromptCancelled(refusal) {
		t.Error("TUIRefuse refusal mis-recognised as secret-cancelled")
	}
}

func TestSecretPromptPredicates_NilSafe(t *testing.T) {
	if IsSecretPromptUnsupported(nil) {
		t.Error("IsSecretPromptUnsupported(nil) = true")
	}
	if IsSecretPromptCancelled(nil) {
		t.Error("IsSecretPromptCancelled(nil) = true")
	}
}

func TestSecretPromptPredicates_UnwrapWrapped(t *testing.T) {
	wrapped := errors.Join(errors.New("context"), errSecretPromptCancelled)
	if !IsSecretPromptCancelled(wrapped) {
		t.Error("wrapped cancelled not recognised")
	}
}
