package orchestrator

import (
	"context"
	"errors"
	"testing"
)

// fakeSecretPrompter records the ctx/hint it received and returns a
// caller-configured (value, error) pair, letting the adapter test assert exact
// passthrough.
type fakeSecretPrompter struct {
	gotCtx  context.Context
	gotHint string
	retVal  string
	retErr  error
}

func (f *fakeSecretPrompter) PromptSecret(ctx context.Context, hint string) (string, error) {
	f.gotCtx = ctx
	f.gotHint = hint
	return f.retVal, f.retErr
}

func TestPasswordPromptAdapter_Delegation(t *testing.T) {
	sentinel := errors.New("boom")
	fake := &fakeSecretPrompter{retVal: "pw", retErr: sentinel}
	adapter := passwordPromptAdapter{h: fake}

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	const hint = "password for X"

	val, err := adapter.PromptPassword(ctx, hint)

	if fake.gotCtx != ctx {
		t.Fatalf("ctx not passed through: got %v want %v", fake.gotCtx, ctx)
	}
	if fake.gotHint != hint {
		t.Fatalf("hint not passed through: got %q want %q", fake.gotHint, hint)
	}
	if val != "pw" {
		t.Fatalf("value mutated: got %q want %q", val, "pw")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error not passed through: got %v want %v", err, sentinel)
	}
}

func TestPasswordPromptAdapter_ErrorPassthrough(t *testing.T) {
	sentinel := errors.New("cancelled")
	fake := &fakeSecretPrompter{retVal: "", retErr: sentinel}
	adapter := passwordPromptAdapter{h: fake}

	_, err := adapter.PromptPassword(context.Background(), "hint")

	if !errors.Is(err, sentinel) {
		t.Fatalf("sentinel error not preserved: got %v want %v", err, sentinel)
	}
}

func TestPasswordPromptAdapter_EmptyValue(t *testing.T) {
	fake := &fakeSecretPrompter{retVal: "", retErr: nil}
	adapter := passwordPromptAdapter{h: fake}

	val, err := adapter.PromptPassword(context.Background(), "hint")

	if val != "" {
		t.Fatalf("empty value mutated: got %q want %q", val, "")
	}
	if err != nil {
		t.Fatalf("nil error mutated: got %v", err)
	}
}
