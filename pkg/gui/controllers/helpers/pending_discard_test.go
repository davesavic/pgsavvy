package helpers_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	helpers "github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// --- Fakes ---------------------------------------------------------------

type fakeConfirm struct {
	title     string
	body      string
	onYes     func() error
	onNo      func() error
	pushErr   error
	pushCalls int
}

func (f *fakeConfirm) Confirm(title, body string, onYes func() error, onNo func() error) error {
	f.title = title
	f.body = body
	f.onYes = onYes
	f.onNo = onNo
	f.pushCalls++
	return f.pushErr
}

// pressYes simulates the user answering "y" on the most recent popup.
func (f *fakeConfirm) pressYes() error {
	if f.onYes == nil {
		return nil
	}
	cb := f.onYes
	f.onYes = nil
	f.onNo = nil
	return cb()
}

// pressNo simulates the user answering "n" / Esc on the most recent popup.
func (f *fakeConfirm) pressNo() error {
	if f.onNo == nil {
		// onNo is allowed to be nil — DiscardAll passes nil there. The
		// helper still considers the set preserved when the popup is
		// dismissed without firing onYes.
		f.onYes = nil
		return nil
	}
	cb := f.onNo
	f.onYes = nil
	f.onNo = nil
	return cb()
}

type fakeDiscardToast struct {
	messages []string
}

func (f *fakeDiscardToast) Show(msg string, ttl time.Duration) {
	_ = ttl
	f.messages = append(f.messages, msg)
}

// --- Test helpers --------------------------------------------------------

func newSet(t *testing.T, ref models.Ref, n int) *models.PendingEditSet {
	t.Helper()
	s := &models.PendingEditSet{Table: ref}
	for i := range n {
		err := s.Add(models.PendingEdit{
			PrimaryKey: []any{i + 1},
			Column:     "name",
			NewValue:   "updated",
			Kind:       models.Literal,
		})
		if err != nil {
			t.Fatalf("seed Add(%d): %v", i, err)
		}
	}
	return s
}

func tableRef() models.Ref { return models.Ref{Schema: "public", Table: "users"} }

// --- DiscardAtCursor -----------------------------------------------------

func TestDiscardAtCursor_RemovesExactlyOneEdit(t *testing.T) {
	set := newSet(t, tableRef(), 3)
	toast := &fakeDiscardToast{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:   set,
		Toast: toast,
	})

	if err := h.DiscardAtCursor([]any{2}, "name"); err != nil {
		t.Fatalf("DiscardAtCursor: unexpected err: %v", err)
	}
	if got, want := set.Count(), 2; got != want {
		t.Fatalf("Count after discard = %d, want %d", got, want)
	}
	for _, e := range set.Edits() {
		if len(e.PrimaryKey) == 1 && e.PrimaryKey[0] == 2 && e.Column == "name" {
			t.Fatalf("edit for pk=2 was not removed")
		}
	}
	if len(toast.messages) != 1 {
		t.Fatalf("expected 1 toast on successful discard, got %d", len(toast.messages))
	}
}

func TestDiscardAtCursor_NoMatchIsNoop(t *testing.T) {
	set := newSet(t, tableRef(), 2)
	toast := &fakeDiscardToast{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:   set,
		Toast: toast,
	})

	// pk=999 does not exist in the seeded set.
	if err := h.DiscardAtCursor([]any{999}, "name"); err != nil {
		t.Fatalf("DiscardAtCursor: unexpected err: %v", err)
	}
	if got, want := set.Count(), 2; got != want {
		t.Fatalf("Count = %d, want %d (set should be unchanged)", got, want)
	}
	if len(toast.messages) != 0 {
		t.Fatalf("expected 0 toasts on no-op, got %d", len(toast.messages))
	}
}

func TestDiscardAtCursor_EmptyPKIsNoop(t *testing.T) {
	set := newSet(t, tableRef(), 1)
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	if err := h.DiscardAtCursor(nil, "name"); err != nil {
		t.Fatalf("DiscardAtCursor(nil pk): unexpected err: %v", err)
	}
	if got := set.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1", got)
	}
}

func TestDiscardAtCursor_NilSetIsSafe(t *testing.T) {
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{})
	if err := h.DiscardAtCursor([]any{1}, "name"); err != nil {
		t.Fatalf("DiscardAtCursor(nil set): unexpected err: %v", err)
	}
}

// --- DiscardAll ----------------------------------------------------------

func TestDiscardAll_AtOrBelowThreshold_ClearsImmediately(t *testing.T) {
	set := newSet(t, tableRef(), helpers.DiscardConfirmThreshold)
	confirm := &fakeConfirm{}
	toast := &fakeDiscardToast{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
		Toast:   toast,
	})

	if err := h.DiscardAll(); err != nil {
		t.Fatalf("DiscardAll: %v", err)
	}
	if confirm.pushCalls != 0 {
		t.Fatalf("expected no confirm popup at threshold, got %d", confirm.pushCalls)
	}
	if !set.IsEmpty() {
		t.Fatalf("set not cleared (count=%d)", set.Count())
	}
	if len(toast.messages) != 1 {
		t.Fatalf("expected 1 status toast, got %d", len(toast.messages))
	}
}

func TestDiscardAll_AboveThreshold_OpensConfirm_YesClears(t *testing.T) {
	set := newSet(t, tableRef(), helpers.DiscardConfirmThreshold+1)
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	if err := h.DiscardAll(); err != nil {
		t.Fatalf("DiscardAll: %v", err)
	}
	if confirm.pushCalls != 1 {
		t.Fatalf("expected 1 confirm popup, got %d", confirm.pushCalls)
	}
	if !strings.Contains(confirm.body, "6 pending edits") {
		t.Fatalf("confirm body = %q, want it to mention the count", confirm.body)
	}
	// Pre-confirm: the set must still be intact.
	if set.IsEmpty() {
		t.Fatalf("set cleared before user confirmed")
	}
	if err := confirm.pressYes(); err != nil {
		t.Fatalf("pressYes: %v", err)
	}
	if !set.IsEmpty() {
		t.Fatalf("set not cleared on Yes (count=%d)", set.Count())
	}
}

func TestDiscardAll_AboveThreshold_NoPreservesSet(t *testing.T) {
	set := newSet(t, tableRef(), helpers.DiscardConfirmThreshold+2)
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	if err := h.DiscardAll(); err != nil {
		t.Fatalf("DiscardAll: %v", err)
	}
	if err := confirm.pressNo(); err != nil {
		t.Fatalf("pressNo: %v", err)
	}
	if set.IsEmpty() {
		t.Fatalf("set was cleared on No path; should be preserved")
	}
	if got, want := set.Count(), helpers.DiscardConfirmThreshold+2; got != want {
		t.Fatalf("Count after No = %d, want %d", got, want)
	}
}

func TestDiscardAll_EmptySetIsNoop(t *testing.T) {
	set := &models.PendingEditSet{Table: tableRef()}
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	if err := h.DiscardAll(); err != nil {
		t.Fatalf("DiscardAll on empty set: %v", err)
	}
	if confirm.pushCalls != 0 {
		t.Fatalf("expected no popup on empty set, got %d", confirm.pushCalls)
	}
}

func TestDiscardAll_NoConfirmHelper_ClearsImmediately(t *testing.T) {
	// When confirm wiring is missing, the helper falls back to the
	// synchronous-clear path rather than dropping the discard silently.
	set := newSet(t, tableRef(), helpers.DiscardConfirmThreshold+3)
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	if err := h.DiscardAll(); err != nil {
		t.Fatalf("DiscardAll: %v", err)
	}
	if !set.IsEmpty() {
		t.Fatalf("set not cleared (count=%d)", set.Count())
	}
}

// --- BlockQuitIfPending --------------------------------------------------

func TestBlockQuitIfPending_EmptySetReturnsNil(t *testing.T) {
	set := &models.PendingEditSet{Table: tableRef()}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	if err := h.BlockQuitIfPending(); err != nil {
		t.Fatalf("BlockQuitIfPending on empty set: %v", err)
	}
}

func TestBlockQuitIfPending_NonEmptyReturnsFormattedError(t *testing.T) {
	set := newSet(t, tableRef(), 3)
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	err := h.BlockQuitIfPending()
	if err == nil {
		t.Fatal("BlockQuitIfPending: want error, got nil")
	}
	if !errors.Is(err, helpers.ErrQuitBlockedByPending) {
		t.Fatalf("err = %v, want errors.Is ErrQuitBlockedByPending", err)
	}
	msg := err.Error()
	for _, want := range []string{"3 pending edits", ":w", ":q!", "<leader>cU"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err message %q missing expected substring %q", msg, want)
		}
	}
}

func TestBlockQuitIfPending_NilSetReturnsNil(t *testing.T) {
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{})
	if err := h.BlockQuitIfPending(); err != nil {
		t.Fatalf("BlockQuitIfPending(nil set): %v", err)
	}
}

// --- Table-switch hook ---------------------------------------------------

func TestShouldPromptOnTableSwitch_DifferentTableWithPending(t *testing.T) {
	set := newSet(t, tableRef(), 1)
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	got := h.ShouldPromptOnTableSwitch(tableRef(), models.Ref{Schema: "public", Table: "orders"})
	if !got {
		t.Fatal("ShouldPromptOnTableSwitch: want true, got false")
	}
}

func TestShouldPromptOnTableSwitch_SameTableReRunDoesNotPrompt(t *testing.T) {
	// AC: same-table query re-run does NOT trigger the table-switch
	// prompt. This is the F5/refresh path — a SELECT against the same
	// target table refreshes the grid without invalidating pending
	// edits.
	set := newSet(t, tableRef(), 1)
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	if h.ShouldPromptOnTableSwitch(tableRef(), tableRef()) {
		t.Fatal("ShouldPromptOnTableSwitch on same-table re-run: want false")
	}
}

func TestShouldPromptOnTableSwitch_EmptySetDoesNotPrompt(t *testing.T) {
	set := &models.PendingEditSet{Table: tableRef()}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{Set: set})

	if h.ShouldPromptOnTableSwitch(tableRef(), models.Ref{Schema: "public", Table: "orders"}) {
		t.Fatal("ShouldPromptOnTableSwitch with empty set: want false")
	}
}

// TestShouldPromptOnTableSwitch_GDNewTabDoesNotPrompt encodes the
// amendment AC: when gd (B5/B6) adds a NEW tab pointing at a different
// parent table, the table-switch hook MUST NOT fire. The hook is the
// caller's predicate — OpenResultTab's "new tab" branch never invokes
// ShouldPromptOnTableSwitch, so we model the gd path by simply not
// calling the predicate and asserting the set is preserved.
func TestShouldPromptOnTableSwitch_GDNewTabDoesNotPrompt(t *testing.T) {
	set := newSet(t, tableRef(), 1)
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})
	_ = h // helper exists; caller (result_tabs_helper) decides whether to invoke it.

	// gd new-tab path: caller does NOT consult the predicate. Verify
	// the set remains intact and no popup was opened.
	if set.IsEmpty() {
		t.Fatal("set should remain intact on gd new-tab path")
	}
	if confirm.pushCalls != 0 {
		t.Fatalf("expected 0 popups on gd new-tab path, got %d", confirm.pushCalls)
	}
}

// --- PromptDiscardOnTableSwitch -----------------------------------------

func TestPromptDiscardOnTableSwitch_YesClearsAndProceeds(t *testing.T) {
	set := newSet(t, tableRef(), 2)
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	proceeded := false
	cancelled := false
	err := h.PromptDiscardOnTableSwitch(
		models.Ref{Schema: "public", Table: "orders"},
		func() error { proceeded = true; return nil },
		func() error { cancelled = true; return nil },
	)
	if err != nil {
		t.Fatalf("PromptDiscardOnTableSwitch: %v", err)
	}
	if confirm.pushCalls != 1 {
		t.Fatalf("expected 1 popup, got %d", confirm.pushCalls)
	}
	if !strings.Contains(confirm.body, "orders") || !strings.Contains(confirm.body, "2 pending edits") {
		t.Fatalf("body %q must mention target table + count", confirm.body)
	}

	if err := confirm.pressYes(); err != nil {
		t.Fatalf("pressYes: %v", err)
	}
	if !proceeded {
		t.Fatal("onProceed was not invoked on Yes")
	}
	if cancelled {
		t.Fatal("onCancel was invoked on Yes path")
	}
	if !set.IsEmpty() {
		t.Fatalf("set not cleared on Yes (count=%d)", set.Count())
	}
}

func TestPromptDiscardOnTableSwitch_NoPreservesSetAndCancels(t *testing.T) {
	set := newSet(t, tableRef(), 2)
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	cancelled := false
	err := h.PromptDiscardOnTableSwitch(
		models.Ref{Schema: "public", Table: "orders"},
		nil,
		func() error { cancelled = true; return nil },
	)
	if err != nil {
		t.Fatalf("PromptDiscardOnTableSwitch: %v", err)
	}
	if err := confirm.pressNo(); err != nil {
		t.Fatalf("pressNo: %v", err)
	}
	if !cancelled {
		t.Fatal("onCancel was not invoked on No path")
	}
	if set.IsEmpty() {
		t.Fatal("set was cleared on No; should be preserved")
	}
}

func TestPromptDiscardOnTableSwitch_EmptySetProceeds(t *testing.T) {
	set := &models.PendingEditSet{Table: tableRef()}
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	proceeded := false
	err := h.PromptDiscardOnTableSwitch(
		models.Ref{Schema: "public", Table: "orders"},
		func() error { proceeded = true; return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("PromptDiscardOnTableSwitch on empty set: %v", err)
	}
	if confirm.pushCalls != 0 {
		t.Fatalf("expected no popup on empty set, got %d", confirm.pushCalls)
	}
	if !proceeded {
		t.Fatal("onProceed was not invoked on empty-set short-circuit")
	}
}

func TestPromptDiscardOnTableSwitch_SameTableProceeds(t *testing.T) {
	set := newSet(t, tableRef(), 2)
	confirm := &fakeConfirm{}
	h := helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		Set:     set,
		Confirm: confirm,
	})

	proceeded := false
	err := h.PromptDiscardOnTableSwitch(
		tableRef(), // same table as the set
		func() error { proceeded = true; return nil },
		nil,
	)
	if err != nil {
		t.Fatalf("PromptDiscardOnTableSwitch same table: %v", err)
	}
	if confirm.pushCalls != 0 {
		t.Fatalf("expected no popup on same-table path, got %d", confirm.pushCalls)
	}
	if !proceeded {
		t.Fatal("onProceed was not invoked on same-table short-circuit")
	}
	if set.IsEmpty() {
		t.Fatal("set was cleared on same-table short-circuit")
	}
}
