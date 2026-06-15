package controllers_test

import (
	"errors"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
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

// newPromptBag returns a UIDeps bundle wired with a fakePromptHelper.
func newPromptBag() (*fakePromptHelper, controllers.UIDeps) {
	h := &fakePromptHelper{}
	return h, controllers.UIDeps{Prompt: h}
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
	// The PROMPT view is editable: printable runes,
	// Backspace, arrow keys, and bracketed-paste are handled natively by
	// gocui.DefaultEditor via the master Editor's Passthrough branch.
	// The controller only owns <cr> (submit) and <esc> (cancel).
	_, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, bag)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	hasEnter, hasEsc := false, false
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
	}
	if !hasEnter || !hasEsc {
		t.Fatalf("missing bindings: enter=%v esc=%v", hasEnter, hasEsc)
	}
}

func TestPromptControllerSubmitDeliversTypedValue(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Simulate helper.Prompt with initial "hi"; in production the
	// editable view's TextArea would absorb the user's typed runes, but
	// in this unit test (no view) the test-mode buffer is seeded via
	// Reset, which Prompt invokes through SetResetHandler.
	_ = h.Prompt("name?", "hi", nil, nil)

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
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	_ = h.Prompt("?", "xy", nil, nil)

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

func TestPromptControllerEmptyBufferSubmitsEmptyString(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, bag)
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

func TestPromptControllerSequentialPromptsReseedFromInitial(t *testing.T) {
	h, bag := newPromptBag()
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// First prompt: initial "alphaX", submits.
	_ = h.Prompt("first?", "alphaX", nil, nil)
	if got := ctrl.Buffer(); got != "alphaX" {
		t.Fatalf("after first Prompt buffer = %q, want alphaX", got)
	}
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
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, bag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	if err := dispatch(t, reg, commands.PromptSubmit); err == nil {
		t.Fatal("submit: want error, got nil")
	}
}

func TestPromptControllerNilHelperHandlersAreNoOp(t *testing.T) {
	// Constructing with a HelperBag that has no Prompt must not panic.
	ctrl := controllers.NewPromptController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	if err := dispatch(t, reg, commands.PromptSubmit); err != nil {
		t.Fatalf("submit nil helper: %v", err)
	}
	if err := dispatch(t, reg, commands.PromptCancel); err != nil {
		t.Fatalf("cancel nil helper: %v", err)
	}
}
