package ui

import (
	"context"
	"log/slog"
	"sync"

	"github.com/davesavic/pgsavvy/pkg/session"
)

// secretPromptOpener is the narrow surface SecretPromptHelper needs to push a
// single-line prompt popup and have its <cr>/<esc> bindings invoke the
// supplied submit / cancel callbacks, plus dismiss the popup on ctx-cancel.
// *PromptHelper satisfies it.
type secretPromptOpener interface {
	Prompt(label, initial string, onSubmit func(value string) error, onCancel func() error) error
	Cancel() error
	Active() bool
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

	// mu guards busy. Held only across the flag transitions at entry and the
	// deferred reset — never across the blocking result select. Mirrors
	// chainedPrompterAdapter: the single popup surface allows one prompt at a
	// time.
	mu   sync.Mutex
	busy bool
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
	h.mu.Lock()
	if h.busy {
		h.mu.Unlock()
		return "", session.NewSecretPromptBusy()
	}
	h.busy = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.busy = false
		h.mu.Unlock()
	}()

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
		// Clear the mask AND dismiss the popup if it is still active, so a
		// timed-out / cancelled connect leaves no orphaned masked prompt on
		// screen. The Active() guard MUST run on the UI lane (the same
		// goroutine that mutates popup state): a ctx-cancel racing a
		// successful Submit could otherwise pop a sibling popup. Mirrors
		// chainedPrompterAdapter's ctx watcher (prompt_chain_adapter.go).
		h.schedule(func() error {
			h.masker.SetMasked(false)
			if !h.open.Active() {
				return nil
			}
			return h.open.Cancel()
		})
		return "", session.NewSecretPromptCancelled(ctx.Err())
	case r := <-resCh:
		if r.cancelled {
			return "", session.NewSecretPromptCancelled(nil)
		}
		return r.value, nil
	}
}
