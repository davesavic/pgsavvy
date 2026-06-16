package orchestrator

import (
	"context"

	"github.com/davesavic/pgsavvy/pkg/session"
)

// passwordPromptAdapter bridges a session.SecretPrompter (PromptSecret) to a
// session.Prompter (PromptPassword) so the masked SSH SecretPromptHelper can
// also serve the driver's final database-credential step. The call signatures
// are identical — only the method name differs — so the adapter delegates
// verbatim, preserving the returned value and any cancel/busy sentinel error
// unmodified.
type passwordPromptAdapter struct {
	h session.SecretPrompter
}

// PromptPassword delegates to the wrapped SecretPrompter's PromptSecret,
// returning its (value, error) unchanged.
func (a passwordPromptAdapter) PromptPassword(ctx context.Context, hint string) (string, error) {
	return a.h.PromptSecret(ctx, hint)
}
