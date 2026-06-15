package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// capturePrompt is a minimal PromptHelper fake: Prompt records the seed +
// callbacks; SubmitWith / CancelWith replay them (mirroring the real
// pop-then-callback flow).
type capturePrompt struct {
	label    string
	initial  string
	onSubmit func(string) error
	onCancel func() error
	active   bool
}

func (p *capturePrompt) Prompt(label, initial string, onSubmit func(string) error, onCancel func() error) error {
	p.label = label
	p.initial = initial
	p.onSubmit = onSubmit
	p.onCancel = onCancel
	p.active = true
	return nil
}

func (p *capturePrompt) Submit(value string) error {
	p.active = false
	if p.onSubmit == nil {
		return nil
	}
	return p.onSubmit(value)
}

func (p *capturePrompt) Cancel() error {
	p.active = false
	if p.onCancel == nil {
		return nil
	}
	return p.onCancel()
}

func (p *capturePrompt) SetResetHandler(func(initial string)) {}

func newFormCtx() *guicontext.ConnectionManagerContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.CONNECTION_MANAGER,
		ViewName: string(types.CONNECTION_MANAGER),
		Kind:     types.MAIN_CONTEXT,
	})
	return guicontext.NewConnectionManagerContext(base, types.ContextTreeDeps{})
}

func driverNames() []string { return []string{"postgres", "mysql"} }

func newFormController(ctx *guicontext.ConnectionManagerContext, prompt controllers.PromptHelper, save func(models.Connection, bool, string) error) *controllers.ConnectionManagerController {
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, func() {})
	ctrl.SetDeps(controllers.ConnectionManagerDeps{
		Ctx:              ctx,
		Prompt:           prompt,
		DriversFn:        driverNames,
		ExistingNames:    func() []string { return []string{"beta"} },
		OnSaveConnection: save,
	})
	return ctrl
}

func dispatchCM(t *testing.T, ctrl *controllers.ConnectionManagerController, id string) {
	t.Helper()
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, ok := reg.Get(id)
	if !ok || cmd == nil {
		t.Fatalf("action %q not registered", id)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("action %q: %v", id, err)
	}
}

// TestController_AddOpensBlankForm asserts `a` opens a blank add form.
func TestController_AddOpensBlankForm(t *testing.T) {
	ctx := newFormCtx()
	ctrl := newFormController(ctx, &capturePrompt{}, nil)
	dispatchCM(t, ctrl, commands.ConnectionManagerAdd)
	if ctx.Mode() != guicontext.ModeForm {
		t.Fatalf("mode after add = %v, want ModeForm", ctx.Mode())
	}
}

// TestController_EditSeedsFromSelectedRow asserts `e` seeds the form from the
// selected list row (isEdit). Empty-list `e` is a no-op.
func TestController_EditSeedsFromSelectedRow(t *testing.T) {
	ctx := newFormCtx()
	ctrl := newFormController(ctx, &capturePrompt{}, nil)

	// Empty list: e is a no-op (stays ModeList).
	dispatchCM(t, ctrl, commands.ConnectionManagerEdit)
	if ctx.Mode() != guicontext.ModeList {
		t.Fatalf("e on empty list changed mode to %v", ctx.Mode())
	}

	ctx.SetItems([]any{&models.Connection{Name: "beta", DSN: "postgres://u@h/db"}})
	dispatchCM(t, ctrl, commands.ConnectionManagerEdit)
	if ctx.Mode() != guicontext.ModeForm {
		t.Fatalf("mode after edit = %v, want ModeForm", ctx.Mode())
	}
}

// TestController_EditBoundToIAndFieldEditUnbound locks the rebind:
// `i` is the single edit key (→ ConnectionManagerEdit), `e` is no longer bound,
// and ConnectionManagerFieldEdit has no key of its own (reached via routing).
func TestController_EditBoundToIAndFieldEditUnbound(t *testing.T) {
	ctrl := newFormController(newFormCtx(), &capturePrompt{}, nil)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})

	iCount := 0
	var iAction, eAction string
	fieldEditBound := false
	for _, b := range bindings {
		if len(b.Sequence) != 1 {
			continue
		}
		switch b.Sequence[0].Code {
		case 'i':
			iCount++
			iAction = b.ActionID
		case 'e':
			eAction = b.ActionID
		}
		if b.ActionID == commands.ConnectionManagerFieldEdit {
			fieldEditBound = true
		}
	}
	if iCount != 1 {
		t.Fatalf("`i` bindings = %d, want exactly 1", iCount)
	}
	if iAction != commands.ConnectionManagerEdit {
		t.Errorf("`i` → %q, want ConnectionManagerEdit", iAction)
	}
	if eAction != "" {
		t.Errorf("`e` still bound to %q, want unbound", eAction)
	}
	if fieldEditBound {
		t.Error("ConnectionManagerFieldEdit still has a key binding, want unbound (reached via routing)")
	}
}

// TestController_EditKeyRoutesToFieldEditInFormMode asserts the single `i`
// binding (ConnectionManagerEdit) edits the focused field when the modal is
// already in form mode, instead of no-opping.
func TestController_EditKeyRoutesToFieldEditInFormMode(t *testing.T) {
	ctx := newFormCtx()
	prompt := &capturePrompt{}
	ctrl := newFormController(ctx, prompt, nil)

	dispatchCM(t, ctrl, commands.ConnectionManagerAdd)
	if ctx.Mode() != guicontext.ModeForm {
		t.Fatalf("precondition mode = %v, want ModeForm", ctx.Mode())
	}
	dispatchCM(t, ctrl, commands.ConnectionManagerEdit)
	if !prompt.active {
		t.Fatal("Edit in form mode did not open the field prompt (routing failed)")
	}
	if prompt.label != "name" {
		t.Errorf("prompt label = %q, want name", prompt.label)
	}
}

// TestController_FieldEditTextOpensPromptAndStores asserts `i` on a text field
// opens the prompt and a valid submit stores the value (AC2 popup-return flow).
func TestController_FieldEditTextOpensPromptAndStores(t *testing.T) {
	ctx := newFormCtx()
	prompt := &capturePrompt{}
	ctrl := newFormController(ctx, prompt, nil)
	dispatchCM(t, ctrl, commands.ConnectionManagerAdd)

	// Name is the first focusable field.
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldEdit)
	if !prompt.active {
		t.Fatal("prompt not opened for name field")
	}
	if prompt.label != "name" {
		t.Errorf("prompt label = %q, want name", prompt.label)
	}
	if err := prompt.Submit("gamma"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if ctx.Mode() != guicontext.ModeForm {
		t.Fatalf("mode after submit = %v, want ModeForm (returns to form)", ctx.Mode())
	}
	if ctx.FormFocusedValue() != "gamma" {
		t.Errorf("name after submit = %q, want gamma", ctx.FormFocusedValue())
	}
}

// TestController_FieldEditRejectsDuplicateName asserts a popup submit of a
// duplicate name does NOT store the value (validation, AC3).
func TestController_FieldEditRejectsDuplicateName(t *testing.T) {
	ctx := newFormCtx()
	prompt := &capturePrompt{}
	ctrl := newFormController(ctx, prompt, nil)
	dispatchCM(t, ctrl, commands.ConnectionManagerAdd)
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldEdit)
	if err := prompt.Submit("beta"); err != nil { // "beta" is in ExistingNames
		t.Fatalf("submit: %v", err)
	}
	if ctx.FormFocusedValue() == "beta" {
		t.Error("duplicate name stored despite validation failure")
	}
}

// TestController_ToggleFlipsBool asserts space on a toggle field flips it; the
// flip survives into the saved connection.
func TestController_ToggleFlipsBool(t *testing.T) {
	ctx := newFormCtx()
	var saved *models.Connection
	ctrl := newFormController(ctx, &capturePrompt{}, func(c models.Connection, _ bool, _ string) error {
		saved = &c
		return nil
	})
	// Seed a valid edit form (name + DSN present) so Confirm saves.
	ctx.SetItems([]any{&models.Connection{Name: "beta", DSN: "postgres://u@h/db"}})
	dispatchCM(t, ctrl, commands.ConnectionManagerEdit)

	// Move focus to read_only (index 7: name, driver, host, port, user,
	// database, sslmode, read_only).
	for range 7 {
		dispatchCM(t, ctrl, commands.ConnectionManagerFieldNext)
	}
	dispatchCM(t, ctrl, commands.ConnectionManagerToggle)
	dispatchCM(t, ctrl, commands.ConnectionManagerConfirm)

	if saved == nil {
		t.Fatal("save not invoked")
	}
	if !saved.ReadOnly {
		t.Error("read_only toggle did not flip in saved connection")
	}
}

// TestController_ConfirmSavesValidFormAndReturnsToList asserts Enter on a valid
// form invokes the save callback and returns to ModeList (AC4 seam).
func TestController_ConfirmSavesValidFormAndReturnsToList(t *testing.T) {
	ctx := newFormCtx()
	var saved *models.Connection
	var savedEdit bool
	ctrl := newFormController(ctx, &capturePrompt{}, func(c models.Connection, isEdit bool, _ string) error {
		saved = &c
		savedEdit = isEdit
		return nil
	})
	ctx.SetItems([]any{&models.Connection{Name: "beta", DSN: "postgres://u@h/db"}})
	dispatchCM(t, ctrl, commands.ConnectionManagerEdit)

	dispatchCM(t, ctrl, commands.ConnectionManagerConfirm)
	if saved == nil {
		t.Fatal("save callback not invoked")
	}
	if !savedEdit {
		t.Error("isEdit = false, want true")
	}
	if ctx.Mode() != guicontext.ModeList {
		t.Fatalf("mode after save = %v, want ModeList", ctx.Mode())
	}
}

// TestController_ConfirmInvalidFormStaysInForm asserts Enter on an invalid form
// does NOT save and stays in ModeForm (AC3 + AC4).
func TestController_ConfirmInvalidFormStaysInForm(t *testing.T) {
	ctx := newFormCtx()
	saves := 0
	ctrl := newFormController(ctx, &capturePrompt{}, func(models.Connection, bool, string) error {
		saves++
		return nil
	})
	dispatchCM(t, ctrl, commands.ConnectionManagerAdd) // blank name → invalid
	dispatchCM(t, ctrl, commands.ConnectionManagerConfirm)
	if saves != 0 {
		t.Errorf("save invoked %d times on invalid form, want 0", saves)
	}
	if ctx.Mode() != guicontext.ModeForm {
		t.Fatalf("mode after invalid save = %v, want ModeForm", ctx.Mode())
	}
}

// TestController_EscCancelsFormBackToList asserts Esc in form mode returns to
// the list without saving (AC2).
func TestController_EscCancelsFormBackToList(t *testing.T) {
	ctx := newFormCtx()
	saves := 0
	ctrl := newFormController(ctx, &capturePrompt{}, func(models.Connection, bool, string) error {
		saves++
		return nil
	})
	dispatchCM(t, ctrl, commands.ConnectionManagerAdd)
	dispatchCM(t, ctrl, commands.ConnectionManagerClose)
	if ctx.Mode() != guicontext.ModeList {
		t.Fatalf("mode after esc = %v, want ModeList", ctx.Mode())
	}
	if saves != 0 {
		t.Errorf("save invoked on cancel")
	}
}
