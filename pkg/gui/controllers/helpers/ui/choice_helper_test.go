package ui_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func newSelectionCtx() *guicontext.SelectionContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.SELECTION,
		ViewName: string(types.SELECTION),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewSelectionContext(base, guicontext.Deps{})
}

func TestChoosePushesTemporaryPopup(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewChoiceHelper(tree, newSelectionCtx())

	if err := h.Choose("driver?", []string{"postgres", "mysql"}, func(int) error { return nil }, nil); err != nil {
		t.Fatalf("Choose: %v", err)
	}
	if !h.Active() {
		t.Fatal("Active = false after Choose; want true")
	}
	top := tree.Current()
	if top == nil || top.GetKey() != types.SELECTION {
		t.Fatalf("top = %v; want SELECTION", top)
	}
	if top.GetKind() != types.TEMPORARY_POPUP {
		t.Fatalf("kind = %v; want TEMPORARY_POPUP", top.GetKind())
	}
}

func TestChooseSubmitDeliversIndex(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewChoiceHelper(tree, newSelectionCtx())
	got := -1
	_ = h.Choose("driver?", []string{"postgres", "mysql", "sqlite"},
		func(i int) error { got = i; return nil }, nil)
	if err := h.Submit(1); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got != 1 {
		t.Fatalf("submit idx = %d; want 1", got)
	}
	if h.Active() {
		t.Fatal("Active = true after Submit")
	}
}

func TestChooseSubmitOutOfRangeReturnsError(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewChoiceHelper(tree, newSelectionCtx())
	called := 0
	_ = h.Choose("?", []string{"a", "b"}, func(int) error { called++; return nil }, nil)

	if err := h.Submit(-1); err == nil {
		t.Fatal("Submit(-1): want error, got nil")
	}
	if err := h.Submit(2); err == nil {
		t.Fatal("Submit(len): want error, got nil")
	}
	if called != 0 {
		t.Fatalf("onSubmit called %d times for out-of-range Submit; want 0", called)
	}
	if !h.Active() {
		t.Fatal("Active = false after out-of-range Submit; want unchanged (true)")
	}
}

func TestChooseCancelInvokesCancelCb(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewChoiceHelper(tree, newSelectionCtx())
	cancelled := 0
	_ = h.Choose("?", []string{"x"}, nil, func() error { cancelled++; return nil })
	if err := h.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled = %d; want 1", cancelled)
	}
	if h.Active() {
		t.Fatal("Active = true after Cancel")
	}
}

func TestChoiceSetCursorClamps(t *testing.T) {
	h := ui.NewChoiceHelper(nil, nil)
	_ = h.Choose("?", []string{"a", "b", "c"}, nil, nil)

	h.SetCursor(-5)
	if got := h.Cursor(); got != 0 {
		t.Errorf("SetCursor(-5) -> Cursor = %d, want 0", got)
	}
	h.SetCursor(99)
	if got := h.Cursor(); got != 2 {
		t.Errorf("SetCursor(99) -> Cursor = %d, want 2", got)
	}
	h.SetCursor(1)
	if got := h.Cursor(); got != 1 {
		t.Errorf("SetCursor(1) -> Cursor = %d, want 1", got)
	}
	if h.Selected() != 1 {
		t.Errorf("Selected = %d, want 1", h.Selected())
	}
}

func TestChoiceSetCursorEmptyChoicesStaysZero(t *testing.T) {
	h := ui.NewChoiceHelper(nil, nil)
	_ = h.Choose("?", nil, nil, nil)
	h.SetCursor(3)
	if got := h.Cursor(); got != 0 {
		t.Fatalf("Cursor = %d with empty choices, want 0", got)
	}
}

func TestChoiceLabelAndChoicesAccessors(t *testing.T) {
	h := ui.NewChoiceHelper(nil, nil)
	_ = h.Choose("the label", []string{"x", "y"}, nil, nil)
	if h.Label() != "the label" {
		t.Errorf("Label = %q", h.Label())
	}
	got := h.Choices()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("Choices = %v, want [x y]", got)
	}
	if h.Cursor() != 0 {
		t.Errorf("Cursor at Choose = %d, want 0", h.Cursor())
	}
}

func TestChooseResetsCursor(t *testing.T) {
	h := ui.NewChoiceHelper(nil, nil)
	_ = h.Choose("?", []string{"a", "b", "c"}, nil, nil)
	h.SetCursor(2)
	if h.Cursor() != 2 {
		t.Fatalf("setup: Cursor = %d, want 2", h.Cursor())
	}
	_ = h.Choose("?", []string{"d", "e"}, nil, nil)
	if h.Cursor() != 0 {
		t.Fatalf("second Choose: Cursor = %d, want 0", h.Cursor())
	}
}
