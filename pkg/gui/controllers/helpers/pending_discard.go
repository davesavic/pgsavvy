package helpers

import (
	"errors"
	"fmt"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// DiscardConfirmThreshold is the size above which DiscardAll prompts
// the user before clearing the PendingEditSet. At-or-below the
// threshold the discard runs immediately.
//
// Per DESIGN.md §12.5.2 the value is 5.
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
// flows and the `:q` quit guard.
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
	allSets func() []*models.PendingEditSet
	confirm confirmer
	toast   statusToast
}

// PendingDiscardDeps bundles PendingDiscardHelper's collaborators. Only
// Set is required for the discard primitives to work; Confirm and Toast
// may be nil during early bootstrap or in unit tests.
type PendingDiscardDeps struct {
	// Set is a single fallback pending-edit collection used when AllSets
	// is nil (chiefly unit tests). Production wires AllSets instead so the
	// cross-table flows see every table's staged edits.
	Set *models.PendingEditSet

	// AllSets snapshots every per-(connID, baseTable) PendingEditSet from
	// the registry. When non-nil it is the source of truth for the
	// cross-table flows (DiscardAll, BlockQuitIfPending); when nil the
	// helper falls back to Set. Nil-safe.
	AllSets func() []*models.PendingEditSet

	// Confirm pushes a CONFIRMATION popup for DiscardAll (>threshold).
	// nil disables the popup path: DiscardAll then clears
	// unconditionally.
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
		allSets: deps.AllSets,
		confirm: deps.Confirm,
		toast:   deps.Toast,
	}
}

// resolveSets returns the collections the cross-table flows operate on:
// the registry snapshot when AllSets is wired, otherwise the single
// fallback Set. Never returns nil entries from the fallback path unless
// Set itself is nil.
func (h *PendingDiscardHelper) resolveSets() []*models.PendingEditSet {
	if h.allSets != nil {
		return h.allSets()
	}
	return []*models.PendingEditSet{h.set}
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
	if h == nil {
		return nil
	}
	return h.DiscardAllSets(h.resolveSets())
}

// DiscardAllSets clears every supplied PendingEditSet — the cross-table
// `<leader>cU` path, where each result tab's (connID, baseTable) owns a
// distinct set. The confirmation threshold applies to the COMBINED edit
// count across all sets: above DiscardConfirmThreshold the helper opens a
// single y/N popup before clearing any set; at-or-below (or with no
// confirm wiring) it clears immediately. nil sets are skipped.
//
// Returns the confirm helper's error verbatim when a popup is opened; nil
// on the synchronous-clear path or when the combined count is zero.
func (h *PendingDiscardHelper) DiscardAllSets(sets []*models.PendingEditSet) error {
	if h == nil {
		return nil
	}
	count := 0
	for _, s := range sets {
		if s != nil {
			count += s.Count()
		}
	}
	if count == 0 {
		return nil
	}
	clearAll := func() error {
		for _, s := range sets {
			if s != nil {
				s.Clear()
			}
		}
		h.emitToast(fmt.Sprintf("discarded %d pending edit(s)", count))
		return nil
	}
	if count <= DiscardConfirmThreshold || h.confirm == nil {
		return clearAll()
	}
	title := "Discard pending edits"
	body := fmt.Sprintf("Discard %d pending edits?", count)
	return h.confirm.Confirm(title, body, clearAll, nil)
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
	if h == nil {
		return nil
	}
	count := 0
	for _, s := range h.resolveSets() {
		if s != nil {
			count += s.Count()
		}
	}
	if count == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d pending edits. :w to commit, :q! to discard, <leader>cU to discard interactively",
		ErrQuitBlockedByPending, count)
}

func (h *PendingDiscardHelper) emitToast(msg string) {
	if h == nil || h.toast == nil {
		return
	}
	h.toast.Show(msg, pendingDiscardToastTTL)
}
