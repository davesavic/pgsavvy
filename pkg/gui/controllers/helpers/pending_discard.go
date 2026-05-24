package helpers

import (
	"errors"
	"fmt"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// DiscardConfirmThreshold is the size above which DiscardAll prompts
// the user before clearing the PendingEditSet. At-or-below the
// threshold the discard runs immediately.
//
// Per DESIGN.md §12.5.2 / dbsavvy-bwq.10 the value is 5.
const DiscardConfirmThreshold = 5

// pendingDiscardToastTTL bounds the post-discard status toast.
const pendingDiscardToastTTL = 4 * time.Second

// confirmer is the narrow ConfirmHelper surface the discard helper
// consumes. *ui.ConfirmHelper satisfies it.
type confirmer interface {
	Confirm(title, body string, onYes func() error, onNo func() error) error
}

// statusToast is the narrow toast surface the discard helper consumes
// for post-discard feedback. *ui.ToastHelper satisfies it. May be nil —
// toasts then no-op.
type statusToast interface {
	Show(message string, ttl time.Duration)
}

// PendingDiscardHelper drives the `<leader>cu` / `<leader>cU` discard
// flows, the `:q` quit guard, and the table-switch confirmation prompt.
//
// All collaborators are nil-tolerant — the helper degrades gracefully
// (no-ops on missing wiring) rather than panicking, so the constructor
// can run before every dependency is wired during bootstrap.
//
// Concurrency: PendingEditSet provides its own sync.RWMutex; the helper
// methods are otherwise stateless and safe to call from the gocui
// MainLoop.
type PendingDiscardHelper struct {
	set     *models.PendingEditSet
	confirm confirmer
	toast   statusToast
}

// PendingDiscardDeps bundles PendingDiscardHelper's collaborators. Only
// Set is required for the discard primitives to work; Confirm and Toast
// may be nil during early bootstrap or in unit tests.
type PendingDiscardDeps struct {
	// Set is the per-table pending-edit collection the helper mutates.
	// Required.
	Set *models.PendingEditSet

	// Confirm pushes a CONFIRMATION popup for DiscardAll (>threshold)
	// and PromptDiscardOnTableSwitch. nil disables the popup path:
	// DiscardAll then clears unconditionally; PromptDiscardOnTableSwitch
	// returns true (proceed) without prompting.
	Confirm confirmer

	// Toast surfaces post-discard status messages. May be nil.
	Toast statusToast
}

// NewPendingDiscardHelper constructs a helper. The returned value is
// non-nil; passing zero deps does NOT panic — methods nil-check the
// PendingEditSet before mutating it.
func NewPendingDiscardHelper(deps PendingDiscardDeps) *PendingDiscardHelper {
	return &PendingDiscardHelper{
		set:     deps.Set,
		confirm: deps.Confirm,
		toast:   deps.Toast,
	}
}

// DiscardAtCursor removes the staged PendingEdit (if any) for the
// supplied primary key + column. No-op when the set is nil, when no
// matching edit exists, or when pk is empty.
//
// Returns nil unconditionally — discarding a non-existent edit is not
// an error; the binding handler should still claim the keystroke.
func (h *PendingDiscardHelper) DiscardAtCursor(pk []any, col string) error {
	if h == nil || h.set == nil {
		return nil
	}
	if len(pk) == 0 || col == "" {
		return nil
	}
	before := h.set.Count()
	h.set.Remove(pk, col)
	after := h.set.Count()
	if after < before {
		h.emitToast(fmt.Sprintf("discarded pending edit on %s", col))
	}
	return nil
}

// DiscardAll clears the entire PendingEditSet. When Count > 5 the
// helper opens a y/N confirmation popup; "y" calls Clear, "n" / Esc
// preserves the set. At-or-below the threshold the helper clears
// immediately without prompting.
//
// Returns the confirm helper's Push error verbatim when a popup is
// opened; nil on the synchronous-clear path or when the set is empty.
func (h *PendingDiscardHelper) DiscardAll() error {
	if h == nil || h.set == nil {
		return nil
	}
	count := h.set.Count()
	if count == 0 {
		return nil
	}
	if count <= DiscardConfirmThreshold || h.confirm == nil {
		h.set.Clear()
		h.emitToast(fmt.Sprintf("discarded %d pending edit(s)", count))
		return nil
	}
	title := "Discard pending edits"
	body := fmt.Sprintf("Discard %d pending edits?", count)
	return h.confirm.Confirm(title, body,
		func() error {
			h.set.Clear()
			h.emitToast(fmt.Sprintf("discarded %d pending edit(s)", count))
			return nil
		},
		nil,
	)
}

// ErrQuitBlockedByPending is returned by BlockQuitIfPending when the
// PendingEditSet has staged edits. The error message is the user-facing
// instruction surface — no further translation required.
var ErrQuitBlockedByPending = errors.New("quit blocked: pending edits")

// BlockQuitIfPending is the precondition the `:q` ExCommand handler
// calls before returning gocui.ErrQuit. Returns a formatted error when
// the PendingEditSet is non-empty, listing the three recovery paths
// (`:w`, `:q!`, `<leader>cU`). Returns nil when the set is empty or
// the helper / set is nil (quit proceeds).
func (h *PendingDiscardHelper) BlockQuitIfPending() error {
	if h == nil || h.set == nil {
		return nil
	}
	count := h.set.Count()
	if count == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d pending edits. :w to commit, :q! to discard, <leader>cU to discard interactively",
		ErrQuitBlockedByPending, count)
}

// ShouldPromptOnTableSwitch reports whether a same-tab re-run that
// changes the result's target table should trigger the discard prompt.
//
// Per ADR-27 (dbsavvy-bwq.10 amendment) the hook fires ONLY when the
// active tab's target.Table changes via re-run; it does NOT fire when
// OpenResultTab adds a NEW tab (e.g. gd jumps from B5/B6 which open
// fresh tabs against a different parent table). The result_tabs_helper
// callsite is responsible for distinguishing these two paths and
// invoking this predicate only on the re-run path.
//
// The predicate returns false when the set is empty (nothing to
// discard) or when current == newTarget (table did not change).
func (h *PendingDiscardHelper) ShouldPromptOnTableSwitch(current, newTarget models.Ref) bool {
	if h == nil || h.set == nil {
		return false
	}
	if h.set.IsEmpty() {
		return false
	}
	if current == newTarget {
		return false
	}
	return true
}

// PromptDiscardOnTableSwitch opens a confirmation popup asking whether
// to discard the staged edits before switching the active tab to a new
// target table. onProceed is invoked on "y" (after Clear); onCancel on
// "n" / Esc (the set is preserved, the table switch should be aborted).
//
// Returns the confirm helper's Push error verbatim. When the confirm
// helper is nil OR the set is empty OR newTarget matches the set's
// current Table, the helper clears nothing and synchronously invokes
// onProceed (treating the absence of staged edits as "nothing to ask
// about") — callers can rely on exactly one of the two callbacks
// firing.
func (h *PendingDiscardHelper) PromptDiscardOnTableSwitch(
	newTarget models.Ref,
	onProceed func() error,
	onCancel func() error,
) error {
	if h == nil || h.set == nil {
		if onProceed != nil {
			return onProceed()
		}
		return nil
	}
	if h.set.IsEmpty() || h.set.Table == newTarget {
		if onProceed != nil {
			return onProceed()
		}
		return nil
	}
	if h.confirm == nil {
		// No popup wiring — preserve the set and refuse the switch so
		// the user cannot silently drop staged edits.
		if onCancel != nil {
			return onCancel()
		}
		return nil
	}
	count := h.set.Count()
	title := "Discard pending edits"
	body := fmt.Sprintf("Discard %d pending edits to switch to %s.%s?",
		count, newTarget.Schema, newTarget.Table)
	return h.confirm.Confirm(title, body,
		func() error {
			h.set.Clear()
			h.emitToast(fmt.Sprintf("discarded %d pending edit(s)", count))
			if onProceed != nil {
				return onProceed()
			}
			return nil
		},
		onCancel,
	)
}

func (h *PendingDiscardHelper) emitToast(msg string) {
	if h == nil || h.toast == nil {
		return
	}
	h.toast.Show(msg, pendingDiscardToastTTL)
}
