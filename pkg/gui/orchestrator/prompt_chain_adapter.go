package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

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
// Single-use per call: the adapter does not guard against concurrent
// PromptString / PromptChoice calls. The helpers are single-popup
// resources; a second call's Prompt/Choose overwrites the first call's
// callbacks. This is by design — the TUI is single-user — but callers
// (and tests) MUST NOT issue overlapping calls. See
// TestChainedPrompterAdapter_PromptString_ConcurrentCallsIsolatedPerInvocation
// for the documented behavior under accidental overlap.
type chainedPrompterAdapter struct {
	promptHelp promptPopup
	choiceHelp choicePopup
	onUIThread func(func() error)
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
// Not safe for concurrent calls (see chainedPrompterAdapter godoc).
func (a *chainedPrompterAdapter) PromptString(ctx context.Context, title, label string, validate func(string) error) (string, error) {
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
							schedule(fmt.Sprintf("%s: %s — %s", title, label, err.Error()))
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
// Not safe for concurrent calls (see chainedPrompterAdapter godoc).
func (a *chainedPrompterAdapter) PromptChoice(ctx context.Context, title, label string, choices []string) (string, error) {
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
