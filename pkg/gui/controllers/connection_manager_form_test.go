package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
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

	// Move focus to read_only (index 3: name, driver, dsn, read_only).
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldNext)
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldNext)
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldNext)
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
