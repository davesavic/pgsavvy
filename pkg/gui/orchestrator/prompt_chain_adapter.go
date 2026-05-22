package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

// ErrPromptBusy is returned by PromptString / PromptChoice when another
// prompt is already in flight on this adapter. The helpers are
// single-popup resources and the adapter rejects overlapping calls at
// the entry gate rather than clobbering the live call's callbacks.
// Callers (e.g. orchestrator flows) inspect this sentinel to decide
// whether to log, surface a toast, or back off.
var ErrPromptBusy = errors.New("prompt: another prompt is in flight")

// promptPopup is the subset of *ui.PromptHelper the adapter touches.
// Declared as an interface so tests can inject a fake that captures the
// onSubmit closure directly (exercising the adapter's defensive paths
// the real helper guards against before the closure runs).
type promptPopup interface {
	Prompt(label, initial string, onSubmit func(value string) error, onCancel func() error) error
	Submit(value string) error
	Cancel() error
	Active() bool
}

// choicePopup is the subset of *ui.ChoiceHelper the adapter touches.
// Same rationale as promptPopup — see TestChainedPrompterAdapter_
// PromptChoice_OutOfRangeRePushes for the motivating test.
type choicePopup interface {
	Choose(label string, choices []string, onSubmit func(idx int) error, onCancel func() error) error
	Submit(idx int) error
	Cancel() error
	Active() bool
}

// chainedPrompterAdapter satisfies data.ChainedPrompter synchronously by
// driving the async *ui.PromptHelper + *ui.ChoiceHelper popups over an
// internal result channel. Per dbsavvy-m47 Architecture Decision #1, the
// adapter blocks the caller goroutine (which the orchestrator wraps in
// OnWorker per m47.4) while the user types/picks on the gocui MainLoop.
//
// Concurrency model (AD #3):
//   - Every helper mutation (Prompt/Submit/Cancel/Choose) is scheduled
//     via onUIThread so it lands on the gocui MainLoop.
//   - The result channel is buffered size 1; a Submit racing with a ctx
//     cancel never blocks the helper goroutine.
//   - A ctx-watcher goroutine schedules helper.Cancel via onUIThread on
//     ctx.Done(); it exits cleanly via the deferred close(done). The
//     scheduled Cancel is guarded by helper.Active() on the UI lane so a
//     ctx-cancel racing a successful Submit cannot pop a sibling
//     popup/side-context off the focus tree.
//
// Validation loop (AD #2):
//   - On validate error, the adapter re-pushes the popup via onUIThread
//     with initial=raw input (preserved across re-prompts) and a label
//     that embeds the original label + the validation error message.
//   - On validate success, the trimmed value is sent on the result
//     channel; the raw input is kept in lastValue only for the next
//     re-push.
//
// Overlapping-call contract: PromptString and PromptChoice share a
// single busy flag guarded by mu. The first caller wins; any
// PromptString / PromptChoice call that arrives while another is in
// flight returns ("", ErrPromptBusy) immediately, without touching the
// helpers. The in-flight call proceeds undisturbed. The busy flag
// clears on every return path (submit success, validate-then-success,
// user cancel, ctx cancel) via a deferred reset installed right after
// the gate. See TestChainedPrompterAdapter_PromptString_
// OverlappingCallsRejectedWithErrPromptBusy and
// TestChainedPrompterAdapter_RejectsConcurrentCalls.
//
// Known limitation: a panic inside the onSubmit / onCancel closure
// executes on the gocui UI goroutine, not on the caller goroutine
// where the deferred reset lives. The caller stays blocked on the
// result channel, so a UI-goroutine panic does NOT clear busy.
// Production callbacks must not panic; the helpers' own recovery
// boundary covers accidental panics inside the popup machinery.
type chainedPrompterAdapter struct {
	promptHelp promptPopup
	choiceHelp choicePopup
	onUIThread func(func() error)

	// mu guards busy. Held only across the flag transitions at
	// entry and the deferred reset — never held across helper IO
	// or the blocking result select.
	mu sync.Mutex
	// busy is true while a PromptString or PromptChoice call is
	// in flight. Shared across both methods because the helpers
	// share the same single popup surface.
	busy bool
}

// newChainedPrompterAdapter constructs the adapter. All three fields are
// required; nil onUIThread would deadlock the caller. m47.4 wires this
// from gui.go with g.OnUIThread as the scheduler.
func newChainedPrompterAdapter(p *ui.PromptHelper, c *ui.ChoiceHelper, onUIThread func(func() error)) *chainedPrompterAdapter {
	return &chainedPrompterAdapter{
		promptHelp: p,
		choiceHelp: c,
		onUIThread: onUIThread,
	}
}

// promptResult carries either the validated value (trimmed) or the
// cancel/error path back to the blocked caller.
type promptResult struct {
	value string
	err   error
}

// PromptString blocks until the user submits a value that passes
// validate or cancels. validate runs on the trimmed input; on failure
// the adapter re-pushes the popup with the raw input as the initial
// value (so the user can edit, not retype). Returns ctx.Err() if ctx
// fires before the user acts.
//
// Overlapping calls return ("", ErrPromptBusy) immediately; the
// in-flight call proceeds undisturbed (see chainedPrompterAdapter
// godoc).
func (a *chainedPrompterAdapter) PromptString(ctx context.Context, title, label string, validate func(string) error) (string, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return "", ErrPromptBusy
	}
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	result := make(chan promptResult, 1)
	done := make(chan struct{})

	// lastValue preserves the raw (untrimmed) user input across
	// re-prompts so the popup re-opens pre-populated with what the user
	// typed. Only ever read/written from the onSubmit closure, which
	// runs serially on the UI thread — no mutex needed.
	lastValue := ""

	var schedule func(curLabel string)
	schedule = func(curLabel string) {
		a.onUIThread(func() error {
			return a.promptHelp.Prompt(curLabel, lastValue,
				func(v string) error { // onSubmit
					lastValue = v
					trimmed := strings.TrimSpace(v)
					if validate != nil {
						if err := validate(trimmed); err != nil {
							// Re-push with the error embedded in
							// the label. Schedule via onUIThread so
							// we don't race the helper.Submit pop.
							// The error lands on its own line so the
							// popup body wraps cleanly instead of
							// truncating long validator messages at
							// the popup right edge (dbsavvy-8p5).
							schedule(fmt.Sprintf("%s: %s\n%s", title, label, err.Error()))
							return nil
						}
					}
					select {
					case result <- promptResult{value: trimmed, err: nil}:
					default:
					}
					return nil
				},
				func() error { // onCancel
					select {
					case result <- promptResult{value: "", err: data.PromptCanceledErr()}:
					default:
					}
					return nil
				},
			)
		})
	}

	schedule(fmt.Sprintf("%s: %s", title, label))

	// ctx watcher: dismiss the popup if ctx fires before the user
	// acts. The Active() guard MUST run on the UI lane (same goroutine
	// that mutates `active`) because helper.Cancel performs an
	// unconditional tree.Pop(); without the guard, a ctx-cancel racing
	// a successful Submit would pop whatever sits underneath this
	// already-dismissed popup (a sibling popup or side context). The
	// select in the caller goroutine can pick ctx.Done() over the
	// result channel even after Submit has already landed.
	go func() {
		select {
		case <-ctx.Done():
			a.onUIThread(func() error {
				if !a.promptHelp.Active() {
					return nil
				}
				return a.promptHelp.Cancel()
			})
		case <-done:
		}
	}()
	defer close(done)

	select {
	case r := <-result:
		return r.value, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// PromptChoice blocks until the user picks an index that maps to a
// valid choice or cancels. If the helper reports an out-of-range index
// (defensive — ChoiceHelper.Submit already guards), the adapter
// re-pushes the popup.
//
// Overlapping calls return ("", ErrPromptBusy) immediately; the
// in-flight call proceeds undisturbed (see chainedPrompterAdapter
// godoc).
func (a *chainedPrompterAdapter) PromptChoice(ctx context.Context, title, label string, choices []string) (string, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return "", ErrPromptBusy
	}
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	result := make(chan promptResult, 1)
	done := make(chan struct{})

	var schedule func()
	schedule = func() {
		a.onUIThread(func() error {
			return a.choiceHelp.Choose(fmt.Sprintf("%s: %s", title, label), choices,
				func(idx int) error { // onSubmit
					if idx < 0 || idx >= len(choices) {
						// Defensive re-push: ChoiceHelper.Submit
						// already guards out-of-range, but if a
						// future caller path slips an invalid idx
						// through (or a test injects a fake that
						// skips the guard), we re-prompt rather than
						// panic or corrupt the result channel.
						schedule()
						return nil
					}
					select {
					case result <- promptResult{value: choices[idx], err: nil}:
					default:
					}
					return nil
				},
				func() error { // onCancel
					select {
					case result <- promptResult{value: "", err: data.PromptCanceledErr()}:
					default:
					}
					return nil
				},
			)
		})
	}

	schedule()

	// ctx watcher: same Active()-guarded pattern as PromptString. See
	// that godoc for why the guard MUST run on the UI lane.
	go func() {
		select {
		case <-ctx.Done():
			a.onUIThread(func() error {
				if !a.choiceHelp.Active() {
					return nil
				}
				return a.choiceHelp.Cancel()
			})
		case <-done:
		}
	}()
	defer close(done)

	select {
	case r := <-result:
		return r.value, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Compile-time assertions: the adapter satisfies data.ChainedPrompter,
// and the real helpers satisfy the popup interfaces (so the constructor
// signature can keep taking concrete *ui.* pointers).
var (
	_ data.ChainedPrompter = (*chainedPrompterAdapter)(nil)
	_ promptPopup          = (*ui.PromptHelper)(nil)
	_ choicePopup          = (*ui.ChoiceHelper)(nil)
)
