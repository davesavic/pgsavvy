package ui

import (
	"context"
	"log/slog"

	"github.com/davesavic/dbsavvy/pkg/session"
)

// secretPromptOpener is the narrow surface SecretPromptHelper needs to push a
// single-line prompt popup and have its <cr>/<esc> bindings invoke the
// supplied submit / cancel callbacks. *PromptHelper satisfies it.
type secretPromptOpener interface {
	Prompt(label, initial string, onSubmit func(value string) error, onCancel func() error) error
}

// secretMasker toggles masked (secret) rendering on the PROMPT popup so the
// typed value is never echoed to the screen or the content buffer.
// *guicontext.PromptContext satisfies it via SetMasked.
type secretMasker interface {
	SetMasked(on bool)
}

// uiScheduler runs fn on the gocui MainLoop. Production wires
// Gui.OnUIThread; tests inject an inline goroutine. The PromptSecret call
// itself runs OFF the main loop (a worker goroutine, per the busy-counter
// threading model), so it blocks waiting for the scheduled prompt to be
// answered.
type uiScheduler func(fn func() error)

// SecretPromptHelper is the TUI session.SecretPrompter. PromptSecret is a
// BLOCKING call meant to run on a worker goroutine: it schedules a masked
// prompt onto the gocui MainLoop and waits on a channel for submit / cancel,
// honoring ctx cancellation so it never leaks.
//
// It deliberately does NOT go through the credentials-waterfall
// TUIRefusePrompter refusal — this is the seam used when masked interactive
// entry IS available.
type SecretPromptHelper struct {
	open     secretPromptOpener
	masker   secretMasker
	schedule uiScheduler
	logger   *slog.Logger
}

// NewSecretPromptHelper builds the helper. open pushes the popup, masker
// toggles masked rendering, schedule runs the push on the MainLoop.
func NewSecretPromptHelper(open secretPromptOpener, masker secretMasker, schedule uiScheduler) *SecretPromptHelper {
	return &SecretPromptHelper{open: open, masker: masker, schedule: schedule}
}

// SetLogger installs a logger used only for non-sensitive lifecycle events.
// The secret value is NEVER logged.
func (h *SecretPromptHelper) SetLogger(l *slog.Logger) { h.logger = l }

// logEvent emits a non-sensitive lifecycle event. Nil-safe.
func (h *SecretPromptHelper) logEvent(msg string, attrs ...slog.Attr) {
	if h.logger == nil {
		return
	}
	h.logger.LogAttrs(context.Background(), slog.LevelDebug, msg, attrs...)
}

// secretResult carries the masked-prompt outcome from the MainLoop callbacks
// back to the blocked PromptSecret caller.
type secretResult struct {
	value     string
	cancelled bool
}

// PromptSecret schedules a masked prompt and blocks until the user submits or
// cancels, or ctx is done. Submit → (value, nil); empty submit → ("", nil);
// cancel or ctx-done → ("", errSecretPromptCancelled). Never routes through
// the TUIRefuse refusal.
func (h *SecretPromptHelper) PromptSecret(ctx context.Context, hint string) (string, error) {
	resCh := make(chan secretResult, 1)

	h.schedule(func() error {
		h.masker.SetMasked(true)
		return h.open.Prompt(hint, "",
			func(value string) error {
				h.masker.SetMasked(false)
				// Lifecycle event only — the secret VALUE is never logged,
				// only its length, so it cannot leak into the log sink.
				h.logEvent("secret_prompt.submit", slog.Int("len", len(value)))
				resCh <- secretResult{value: value}
				return nil
			},
			func() error {
				h.masker.SetMasked(false)
				resCh <- secretResult{cancelled: true}
				return nil
			},
		)
	})

	select {
	case <-ctx.Done():
		// Clear the mask on the MainLoop so a later normal prompt is not
		// masked. The pending submit/cancel callbacks become no-op sends into
		// the buffered channel (or are never invoked); either way no leak.
		h.schedule(func() error {
			h.masker.SetMasked(false)
			return nil
		})
		return "", session.NewSecretPromptCancelled(ctx.Err())
	case r := <-resCh:
		if r.cancelled {
			return "", session.NewSecretPromptCancelled(nil)
		}
		return r.value, nil
	}
}
