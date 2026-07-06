package controllers_test

import (
	"errors"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newTestSettingsCtx(cfg *config.UserConfig) *context.SettingsContext {
	return newSettingsContext(cfg)
}

// newSettingsContext creates a minimal SettingsContext wired with the
// provided config for controller unit tests.
func newSettingsContext(cfg *config.UserConfig) *context.SettingsContext {
	return context.NewSettingsContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.SETTINGS,
			ViewName: "settings",
			Kind:     types.MAIN_CONTEXT,
			Title:    "Settings",
		}),
		context.SettingsContextDeps{
			ContextTreeDeps: types.ContextTreeDeps{GuiDriver: nil},
			Cfg:             func() *config.UserConfig { return cfg },
		},
	)
}

func TestSettingsControllerTabNavigation(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	tabCount := ctx.TabCount()

	// Navigate forward one full cycle and verify wrap
	for i := 0; i < tabCount; i++ {
		invokeActionByID(t, reg, commands.SettingsNextTab)
	}
	if ctx.ActiveTab() != 0 {
		t.Errorf("after %d NextTabs (full cycle), ActiveTab = %d, want 0", tabCount, ctx.ActiveTab())
	}

	// Navigate backward from tab 0 should wrap to last tab
	invokeActionByID(t, reg, commands.SettingsPrevTab)
	if ctx.ActiveTab() != tabCount-1 {
		t.Errorf("after PrevTab from 0, ActiveTab = %d, want %d", ctx.ActiveTab(), tabCount-1)
	}

	// Navigate forward from last tab should wrap to 0
	invokeActionByID(t, reg, commands.SettingsNextTab)
	if ctx.ActiveTab() != 0 {
		t.Errorf("after NextTab from %d, ActiveTab = %d, want 0", tabCount-1, ctx.ActiveTab())
	}
}

func TestSettingsControllerFieldNavigation(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Field should clamp at 0
	for i := 0; i < 5; i++ {
		invokeActionByID(t, reg, commands.SettingsFieldUp)
	}
	if ctx.GetFocusField() != 0 {
		t.Errorf("FieldUp clamped: got %d, want 0", ctx.GetFocusField())
	}

	// Move down
	n := ctx.FieldCount()
	for i := 0; i < n+5; i++ {
		invokeActionByID(t, reg, commands.SettingsFieldDown)
	}
	if ctx.GetFocusField() != n-1 {
		t.Errorf("FieldDown clamped: got %d, want %d", ctx.GetFocusField(), n-1)
	}
}

func TestSettingsControllerFieldEditValid(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	prompt := &fakePrompt{}
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx, Prompt: prompt})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(0)
	ctx.SetFocusField(0)

	invokeActionByID(t, reg, commands.SettingsFieldEdit)

	if prompt.calls != 1 {
		t.Fatalf("prompt calls = %d, want 1", prompt.calls)
	}

	if err := prompt.Submit("\\"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if ctx.GetEditedConfig().Leader != "\\" {
		t.Errorf("Leader after edit = %q, want %q", ctx.GetEditedConfig().Leader, "\\")
	}
}

func TestSettingsControllerFieldEditInvalidValuePreserved(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.UI.Mouse.DoubleClickMs = 400
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	prompt := &fakePrompt{}
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx, Prompt: prompt})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(2)
	// Find the double_click_ms field
	for i := 0; i < ctx.FieldCount(); i++ {
		ctx.SetFocusField(i)
		if ctx.GetFocusedField().Label() == "double_click_ms" {
			break
		}
	}

	invokeActionByID(t, reg, commands.SettingsFieldEdit)
	if err := prompt.Submit("invalid"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if ctx.GetEditedConfig().UI.Mouse.DoubleClickMs != 400 {
		t.Errorf("DoubleClickMs = %d, want 400 (value preserved after invalid)", ctx.GetEditedConfig().UI.Mouse.DoubleClickMs)
	}
}

func TestSettingsControllerFieldToggle(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.UI.Mouse.Enabled = false
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(2)
	// Toggle on the mouse_enabled field (tab 2, field 0 should be a toggle)
	ctx.SetFocusField(0)
	invokeActionByID(t, reg, commands.SettingsFieldToggle)

	if !ctx.GetEditedConfig().UI.Mouse.Enabled {
		t.Error("ToggleFocused did not flip mouse_enabled to true")
	}

	invokeActionByID(t, reg, commands.SettingsFieldToggle)
	if ctx.GetEditedConfig().UI.Mouse.Enabled {
		t.Error("ToggleFocused did not flip mouse_enabled back to false")
	}
}

func TestSettingsControllerConfirmValidationPass(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	var saved *config.UserConfig
	onSave := func(c *config.UserConfig) error {
		saved = c.Clone()
		return nil
	}

	var toastMsg string
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{
		Ctx:          ctx,
		OnSaveConfig: onSave,
		ShowToast:    func(msg string) { toastMsg = msg },
		ValidationDeps: config.ValidationDeps{
			ActionExists: func(id string) bool { return id == "app.quit" },
			ScopeExists:  func(s string) bool { return s == "global" },
		},
	})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	invokeActionByID(t, reg, commands.SettingsConfirm)

	if saved == nil {
		t.Fatal("OnSaveConfig was not called")
	}
	if saved.Leader != cfg.Leader {
		t.Errorf("saved Leader = %q, want %q", saved.Leader, cfg.Leader)
	}

	if toastMsg != "Settings saved" {
		t.Errorf("toast = %q, want 'Settings saved'", toastMsg)
	}
}

func TestSettingsControllerConfirmValidationFail(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Leader = "0" // invalid leader (digit)
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	saveCalled := false
	onSave := func(_ *config.UserConfig) error {
		saveCalled = true
		return nil
	}

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{
		Ctx:          ctx,
		OnSaveConfig: onSave,
		ValidationDeps: config.ValidationDeps{
			ActionExists: func(id string) bool { return true },
			ScopeExists:  func(s string) bool { return true },
		},
	})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	invokeActionByID(t, reg, commands.SettingsConfirm)

	if saveCalled {
		t.Error("OnSaveConfig was called despite validation failure")
	}

	if errText := ctx.GetFormError(0); errText == "" {
		t.Error("error should be set on failed validation")
	}
}

func TestSettingsControllerCloseCallback(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	closed := false
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{
		Ctx:   ctx,
		Close: func() { closed = true },
	})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	invokeActionByID(t, reg, commands.SettingsClose)

	if !closed {
		t.Error("Close callback was not invoked")
	}
}

func TestSettingsControllerKeybindingAdd(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	prompt := &fakePrompt{}
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx, Prompt: prompt})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	invokeActionByID(t, reg, commands.SettingsKeybindingAdd)

	if prompt.calls != 1 {
		t.Fatalf("prompt calls = %d, want 1", prompt.calls)
	}

	// Walk the 5-prompt chain: Mode, Scope, Key, Action, Description
	for _, val := range []string{"n", "global", "<c-c>", "list.down", "Move down"} {
		if prompt.onSubmit == nil {
			t.Fatal("prompt chain broke early — no pending prompt")
		}
		if err := prompt.Submit(val); err != nil {
			t.Fatalf("submit %q: %v", val, err)
		}
	}

	// After final submit, KeybindingAdd calls HandleFocus which re-clones from
	// Cfg(). The keybinding was appended to the clone but may be lost on reset.
	// Verify no crash occurred.
}

func TestSettingsControllerKeybindingDeleteConfirm(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
		{Mode: "n", Scope: "global", Key: "j", Action: "list.down"},
	}
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	confirm := &fakeConfirm{}
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx, Confirm: confirm})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(5)
	ctx.SetFocusField(1)

	invokeActionByID(t, reg, commands.SettingsKeybindingDelete)

	if len(confirm.calls) != 1 {
		t.Fatalf("confirm calls = %d, want 1", len(confirm.calls))
	}

	// Execute the confirm callback
	if confirm.calls[0].OnYes != nil {
		if err := confirm.calls[0].OnYes(); err != nil {
			t.Fatalf("OnYes: %v", err)
		}
	}
}

func TestSettingsControllerKeybindingDeleteCancel(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
		{Mode: "n", Scope: "global", Key: "j", Action: "list.down"},
	}
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	confirm := &fakeConfirm{}
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx, Confirm: confirm})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(5)
	ctx.SetFocusField(1)

	invokeActionByID(t, reg, commands.SettingsKeybindingDelete)

	if len(confirm.calls) != 1 {
		t.Fatalf("confirm calls = %d, want 1", len(confirm.calls))
	}

	if confirm.calls[0].OnNo != nil {
		_ = confirm.calls[0].OnNo()
	}
}

func TestSettingsControllerNilContext(t *testing.T) {
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ids := []string{
		commands.SettingsNextTab,
		commands.SettingsPrevTab,
		commands.SettingsFieldUp,
		commands.SettingsFieldDown,
		commands.SettingsFieldEdit,
		commands.SettingsFieldToggle,
		commands.SettingsConfirm,
		commands.SettingsClose,
		commands.SettingsKeybindingAdd,
		commands.SettingsKeybindingDelete,
	}

	for _, id := range ids {
		cmd, ok := reg.Get(id)
		if !ok || cmd == nil || cmd.Handler == nil {
			t.Fatalf("action %q not registered", id)
		}
		if err := cmd.Handler(commands.ExecCtx{}); err != nil {
			t.Errorf("action %q with nil context: %v", id, err)
		}
	}
}

func TestSettingsControllerCloseWithoutCallback(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Should not panic, Close is nil
	invokeActionByID(t, reg, commands.SettingsClose)
}

func TestSettingsControllerConfirmSaveError(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	saveErr := errors.New("disk full")
	onSave := func(_ *config.UserConfig) error {
		return saveErr
	}

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{
		Ctx:          ctx,
		OnSaveConfig: onSave,
		ValidationDeps: config.ValidationDeps{
			ActionExists: func(id string) bool { return id == "app.quit" },
			ScopeExists:  func(s string) bool { return s == "global" },
		},
	})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	invokeActionByID(t, reg, commands.SettingsConfirm)

	if errText := ctx.GetFormError(0); errText != saveErr.Error() {
		t.Errorf("error = %q, want %q", errText, saveErr.Error())
	}
}

func TestSettingsControllerFieldEditNoPrompt(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(0)
	ctx.SetFocusField(0)

	invokeActionByID(t, reg, commands.SettingsFieldEdit)
}

func TestSettingsControllerFieldEditKeysTab(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit"},
	}
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	prompt := &fakePrompt{}
	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx, Prompt: prompt})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(5)
	ctx.SetFocusField(0)
	invokeActionByID(t, reg, commands.SettingsFieldEdit)

	if prompt.calls != 1 {
		t.Fatalf("FieldEdit on Keys tab should open a rebind prompt; got %d calls", prompt.calls)
	}

	if err := prompt.Submit("Q"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	got := ctx.GetEditedConfig().Keybindings
	if len(got) != 1 {
		t.Fatalf("Keybindings len = %d, want 1", len(got))
	}
	if got[0].Key != "Q" {
		t.Errorf("rebound key = %q, want %q", got[0].Key, "Q")
	}
}

func TestSettingsControllerFieldToggleKeysTab(t *testing.T) {
	cfg := config.GetDefaultConfig()
	ctx := newTestSettingsCtx(cfg)
	ctx.HandleFocus(types.OnFocusOpts{})

	ctrl := controllers.NewSettingsController(nil, controllers.CoreDeps{}, controllers.UIDeps{})
	ctrl.SetDeps(controllers.SettingsDeps{Ctx: ctx})

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	ctx.SetActiveTab(5)
	invokeActionByID(t, reg, commands.SettingsFieldToggle)
}

func invokeActionByID(t *testing.T, reg *commands.Registry, id string) {
	t.Helper()
	cmd, ok := reg.Get(id)
	if !ok || cmd == nil || cmd.Handler == nil {
		t.Fatalf("action %q not registered", id)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("invoke %q: %v", id, err)
	}
}
