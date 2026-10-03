package ui

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

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
	Generation() uint64
	CancelGeneration(uint64) error
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
	err       error
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
	var settled sync.Once
	var generation atomic.Uint64
	finish := func(result secretResult, before func()) {
		settled.Do(func() {
			if before != nil {
				before()
			}
			resCh <- result
		})
	}
	clearMask := func() {
		if own := generation.Load(); own == 0 || h.open.Generation() == own {
			h.masker.SetMasked(false)
		}
	}

	h.schedule(func() error {
		if err := ctx.Err(); err != nil {
			finish(secretResult{err: session.NewSecretPromptCancelled(err)}, nil)
			return nil
		}
		h.masker.SetMasked(true)
		err := h.open.Prompt(hint, "",
			func(value string) error {
				finish(secretResult{value: value}, func() {
					clearMask()
					// Lifecycle metadata only; never log the secret value.
					h.logEvent("secret_prompt.submit", slog.Int("len", len(value)))
				})
				return nil
			},
			func() error {
				finish(secretResult{cancelled: true}, clearMask)
				return nil
			},
		)
		generation.Store(h.open.Generation())
		if err != nil {
			finish(secretResult{err: err}, clearMask)
		}
		return err
	})

	var r secretResult
	select {
	case <-ctx.Done():
		finish(secretResult{err: session.NewSecretPromptCancelled(ctx.Err())}, nil)
		// Both checks run on the UI lane; delayed cleanup must not unmask or
		// dismiss a replacement popup, even after this worker has returned.
		h.schedule(func() error {
			own := generation.Load()
			if own == 0 || h.open.Generation() != own {
				return nil
			}
			h.masker.SetMasked(false)
			return h.open.CancelGeneration(own)
		})
		r = <-resCh
	case r = <-resCh:
	}
	if r.err != nil {
		return "", r.err
	}
	if r.cancelled {
		return "", session.NewSecretPromptCancelled(nil)
	}
	return r.value, nil
}
