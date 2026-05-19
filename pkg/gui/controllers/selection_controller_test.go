package controllers_test

import (
	"errors"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeChoiceHelper records Submit/Cancel calls and exposes a tiny
// in-memory cursor + choices model so SelectionController can drive
// it the same way the production *ui.ChoiceHelper would.
type fakeChoiceHelper struct {
	choices   []string
	cursor    int
	submitted []int
	cancelled int
	submitErr error
	cancelErr error
}

func (f *fakeChoiceHelper) Choose(_ string, choices []string, _ func(int) error, _ func() error) error {
	f.choices = choices
	f.cursor = 0
	return nil
}

func (f *fakeChoiceHelper) Submit(idx int) error {
	if idx < 0 || idx >= len(f.choices) {
		return errors.New("out of range")
	}
	f.submitted = append(f.submitted, idx)
	return f.submitErr
}

func (f *fakeChoiceHelper) Cancel() error {
	f.cancelled++
	return f.cancelErr
}

func (f *fakeChoiceHelper) Cursor() int { return f.cursor }
func (f *fakeChoiceHelper) SetCursor(i int) { // mirrors ui.ChoiceHelper.SetCursor clamp
	n := len(f.choices)
	if n == 0 {
		f.cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i > n-1 {
		i = n - 1
	}
	f.cursor = i
}

// newSelectionBag returns a HelperBag wired with a fakeChoiceHelper.
func newSelectionBag() (*fakeChoiceHelper, controllers.HelperBag) {
	h := &fakeChoiceHelper{}
	return h, controllers.HelperBag{Choice: h}
}

func TestSelectionControllerHasRequiredBindings(t *testing.T) {
	_, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	if len(kbs) != 6 {
		t.Fatalf("expected 6 bindings, got %d", len(kbs))
	}
	hasUp, hasDown, hasK, hasJ, hasEnter, hasEsc := false, false, false, false, false, false
	for _, kb := range kbs {
		if kb.Scope != types.SELECTION {
			t.Errorf("binding scope = %q, want SELECTION", kb.Scope)
		}
		if isSpecial(kb, types.KeyUp) {
			hasUp = true
		}
		if isSpecial(kb, types.KeyDown) {
			hasDown = true
		}
		if isRune(kb, 'k') {
			hasK = true
		}
		if isRune(kb, 'j') {
			hasJ = true
		}
		if isSpecial(kb, types.KeyEnter) {
			hasEnter = true
		}
		if isSpecial(kb, types.KeyEsc) {
			hasEsc = true
		}
	}
	if !hasUp || !hasDown || !hasK || !hasJ || !hasEnter || !hasEsc {
		t.Fatalf("missing bindings: up=%v down=%v k=%v j=%v enter=%v esc=%v",
			hasUp, hasDown, hasK, hasJ, hasEnter, hasEsc)
	}
}

func TestSelectionControllerDownMovesCursor(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a", "b", "c"}, nil, nil)

	if err := dispatch(t, reg, commands.SelectionDown); err != nil {
		t.Fatalf("down: %v", err)
	}
	if h.Cursor() != 1 {
		t.Fatalf("cursor after down = %d, want 1", h.Cursor())
	}
}

func TestSelectionControllerUpMovesCursor(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a", "b", "c"}, nil, nil)
	h.SetCursor(2)

	if err := dispatch(t, reg, commands.SelectionUp); err != nil {
		t.Fatalf("up: %v", err)
	}
	if h.Cursor() != 1 {
		t.Fatalf("cursor after up = %d, want 1", h.Cursor())
	}
}

func TestSelectionControllerCursorClampsAtTop(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a", "b"}, nil, nil)
	// cursor starts at 0; Up must stay at 0.

	if err := dispatch(t, reg, commands.SelectionUp); err != nil {
		t.Fatalf("up: %v", err)
	}
	if h.Cursor() != 0 {
		t.Fatalf("cursor after up at top = %d, want 0 (clamped)", h.Cursor())
	}
}

func TestSelectionControllerCursorClampsAtBottom(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a", "b"}, nil, nil)
	h.SetCursor(1)

	if err := dispatch(t, reg, commands.SelectionDown); err != nil {
		t.Fatalf("down: %v", err)
	}
	if h.Cursor() != 1 {
		t.Fatalf("cursor after down at bottom = %d, want 1 (clamped)", h.Cursor())
	}
}

func TestSelectionControllerConfirmSubmitsCursor(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a", "b", "c"}, nil, nil)
	h.SetCursor(2)

	if err := dispatch(t, reg, commands.SelectionConfirm); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(h.submitted) != 1 || h.submitted[0] != 2 {
		t.Fatalf("submitted = %v, want [2]", h.submitted)
	}
}

func TestSelectionControllerCancelInvokesHelper(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a"}, nil, nil)

	if err := dispatch(t, reg, commands.SelectionCancel); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if h.cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", h.cancelled)
	}
}

func TestSelectionControllerJAliasMovesDown(t *testing.T) {
	h, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)
	_ = h.Choose("?", []string{"a", "b", "c"}, nil, nil)

	// Find the j binding; it must reference SelectionDown.
	var jBinding *types.ChordBinding
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'j') {
			jBinding = kb
			break
		}
	}
	if jBinding == nil {
		t.Fatal("no j binding")
	}
	if jBinding.ActionID != commands.SelectionDown {
		t.Fatalf("j ActionID = %q, want %q", jBinding.ActionID, commands.SelectionDown)
	}
}

func TestSelectionControllerKAliasMovesUp(t *testing.T) {
	_, bag := newSelectionBag()
	ctrl := controllers.NewSelectionController(nil, bag)

	var kBinding *types.ChordBinding
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'k') {
			kBinding = kb
			break
		}
	}
	if kBinding == nil {
		t.Fatal("no k binding")
	}
	if kBinding.ActionID != commands.SelectionUp {
		t.Fatalf("k ActionID = %q, want %q", kBinding.ActionID, commands.SelectionUp)
	}
}

func TestSelectionControllerConfirmPropagatesHelperError(t *testing.T) {
	h, bag := newSelectionBag()
	h.submitErr = errors.New("boom")
	ctrl := controllers.NewSelectionController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Choose("?", []string{"a"}, nil, nil)

	if err := dispatch(t, reg, commands.SelectionConfirm); err == nil {
		t.Fatal("confirm: want error, got nil")
	}
}

func TestSelectionControllerNilHelperHandlersAreNoOp(t *testing.T) {
	ctrl := controllers.NewSelectionController(nil, controllers.HelperBag{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	if err := dispatch(t, reg, commands.SelectionUp); err != nil {
		t.Fatalf("up nil helper: %v", err)
	}
	if err := dispatch(t, reg, commands.SelectionDown); err != nil {
		t.Fatalf("down nil helper: %v", err)
	}
	if err := dispatch(t, reg, commands.SelectionConfirm); err != nil {
		t.Fatalf("confirm nil helper: %v", err)
	}
	if err := dispatch(t, reg, commands.SelectionCancel); err != nil {
		t.Fatalf("cancel nil helper: %v", err)
	}
}
