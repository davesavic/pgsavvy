package controllers_test

import (
	"errors"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakePromptHelper records Submit / Cancel calls and exposes a
// Trigger(initial) helper so tests can simulate a fresh helper.Prompt
// invocation (which fires the reset callback the controller registers
// during construction).
type fakePromptHelper struct {
	submitted []string
	cancelled int
	submitErr error
	cancelErr error
	reset     func(initial string)
}

func (f *fakePromptHelper) Prompt(_ string, initial string, _ func(string) error, _ func() error) error {
	if f.reset != nil {
		f.reset(initial)
	}
	return nil
}

func (f *fakePromptHelper) Submit(value string) error {
	f.submitted = append(f.submitted, value)
	return f.submitErr
}

func (f *fakePromptHelper) Cancel() error {
	f.cancelled++
	return f.cancelErr
}

func (f *fakePromptHelper) SetResetHandler(fn func(initial string)) {
	f.reset = fn
}

// newPromptBag returns a HelperBag wired with a fakePromptHelper.
func newPromptBag() (*fakePromptHelper, controllers.HelperBag) {
	h := &fakePromptHelper{}
	return h, controllers.HelperBag{Prompt: h}
}

// dispatch resolves the binding for action ID through reg and invokes
// it with a zero ExecCtx.
func dispatch(t *testing.T, reg *commands.Registry, id string) error {
	t.Helper()
	cmd, ok := reg.Get(id)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("no handler registered for %q", id)
	}
	return cmd.Handler(commands.ExecCtx{})
}

func TestPromptControllerHasRequiredBindings(t *testing.T) {
	_, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	if len(kbs) < 4 {
		t.Fatalf("expected ≥4 bindings, got %d", len(kbs))
	}

	hasEnter, hasEsc, hasBs, hasRune := false, false, false, false
	for _, kb := range kbs {
		if kb.Scope != types.PROMPT {
			t.Errorf("binding scope = %q, want PROMPT", kb.Scope)
		}
		if isSpecial(kb, types.KeyEnter) {
			hasEnter = true
		}
		if isSpecial(kb, types.KeyEsc) {
			hasEsc = true
		}
		if isSpecial(kb, types.KeyBs) {
			hasBs = true
		}
		if isRune(kb, 'a') {
			hasRune = true
		}
	}
	if !hasEnter || !hasEsc || !hasBs || !hasRune {
		t.Fatalf("missing bindings: enter=%v esc=%v bs=%v rune-a=%v",
			hasEnter, hasEsc, hasBs, hasRune)
	}
}

func TestPromptControllerSubmitDeliversTypedValue(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Simulate helper.Prompt with initial "" then type "hi".
	_ = h.Prompt("name?", "", nil, nil)
	if err := ctrl.InsertRune('h'); err != nil {
		t.Fatalf("InsertRune h: %v", err)
	}
	if err := ctrl.InsertRune('i'); err != nil {
		t.Fatalf("InsertRune i: %v", err)
	}

	if err := dispatch(t, reg, commands.PromptSubmit); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := h.submitted; len(got) != 1 || got[0] != "hi" {
		t.Fatalf("submitted = %v, want [hi]", got)
	}
	if ctrl.Buffer() != "" {
		t.Fatalf("buffer = %q after submit, want empty", ctrl.Buffer())
	}
}

func TestPromptControllerCancelInvokesHelperOnce(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Prompt("?", "x", nil, nil)
	_ = ctrl.InsertRune('y')

	if err := dispatch(t, reg, commands.PromptCancel); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if h.cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", h.cancelled)
	}
	if ctrl.Buffer() != "" {
		t.Fatalf("buffer = %q after cancel, want empty", ctrl.Buffer())
	}
}

func TestPromptControllerBackspaceOnEmptyIsNoOp(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	if err := dispatch(t, reg, commands.PromptBackspace); err != nil {
		t.Fatalf("backspace: %v", err)
	}
	if len(h.submitted) != 0 || h.cancelled != 0 {
		t.Fatalf("backspace fired callbacks: submitted=%v cancelled=%d", h.submitted, h.cancelled)
	}
	if ctrl.Buffer() != "" {
		t.Fatalf("buffer = %q after empty backspace, want empty", ctrl.Buffer())
	}
}

func TestPromptControllerBackspaceRemovesLastRune(t *testing.T) {
	_, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	_ = ctrl.InsertRune('a')
	_ = ctrl.InsertRune('b')
	_ = ctrl.InsertRune('c')
	if err := dispatch(t, reg, commands.PromptBackspace); err != nil {
		t.Fatalf("backspace: %v", err)
	}
	if got := ctrl.Buffer(); got != "ab" {
		t.Fatalf("buffer = %q, want ab", got)
	}
}

func TestPromptControllerEmptyBufferSubmitsEmptyString(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Prompt("?", "", nil, nil)

	if err := dispatch(t, reg, commands.PromptSubmit); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(h.submitted) != 1 || h.submitted[0] != "" {
		t.Fatalf("submitted = %v, want [\"\"]", h.submitted)
	}
}

func TestPromptControllerInsertNonASCIIRune(t *testing.T) {
	_, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)

	if err := ctrl.InsertRune('ñ'); err != nil {
		t.Fatalf("InsertRune: %v", err)
	}
	if got := ctrl.Buffer(); got != "ñ" {
		t.Fatalf("buffer = %q, want ñ", got)
	}
}

func TestPromptControllerSequentialPromptsReseedFromInitial(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// First prompt: initial "alpha", user types "X", submits.
	_ = h.Prompt("first?", "alpha", nil, nil)
	if got := ctrl.Buffer(); got != "alpha" {
		t.Fatalf("after first Prompt buffer = %q, want alpha", got)
	}
	_ = ctrl.InsertRune('X')
	if err := dispatch(t, reg, commands.PromptSubmit); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := h.submitted[0]; got != "alphaX" {
		t.Fatalf("first submit = %q, want alphaX", got)
	}

	// Second prompt: initial "beta" — buffer must start clean from
	// "beta", NOT inherit the prior buffer.
	_ = h.Prompt("second?", "beta", nil, nil)
	if got := ctrl.Buffer(); got != "beta" {
		t.Fatalf("after second Prompt buffer = %q, want beta (re-seeded)", got)
	}
}

func TestPromptControllerSubmitPropagatesHelperError(t *testing.T) {
	h, bag := newPromptBag()
	h.submitErr = errors.New("boom")
	ctrl := controllers.NewPromptController(nil, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	if err := dispatch(t, reg, commands.PromptSubmit); err == nil {
		t.Fatal("submit: want error, got nil")
	}
}

func TestPromptControllerNilHelperHandlersAreNoOp(t *testing.T) {
	// Constructing with a HelperBag that has no Prompt must not panic.
	ctrl := controllers.NewPromptController(nil, controllers.HelperBag{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	if err := dispatch(t, reg, commands.PromptSubmit); err != nil {
		t.Fatalf("submit nil helper: %v", err)
	}
	if err := dispatch(t, reg, commands.PromptCancel); err != nil {
		t.Fatalf("cancel nil helper: %v", err)
	}
	if err := dispatch(t, reg, commands.PromptBackspace); err != nil {
		t.Fatalf("backspace nil helper: %v", err)
	}
}
