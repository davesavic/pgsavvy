package controllers

import (
	"fmt"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// SettingsController owns the SETTINGS modal's keybindings (6-tabbed
// configuration editor):
//
//   - [ / ] cycle tabs
//   - j/k move field focus
//   - i edits the focused text field (PROMPT popup), or rebinds the focused
//     keybinding row on the Keys tab
//   - space toggles the focused toggle field
//   - Enter validates + saves the edited config
//   - Esc closes the modal
//   - a/d add / delete keybindings on the Keys tab (tab 5)
//
// The injected callbacks (close + the SettingsDeps closures) keep
// this controller compilable and unit-testable without the focus-stack /
// config-write handles.
type SettingsController struct {
	baseController

	deps SettingsDeps
}

// SettingsDeps bundles the closures the orchestrator wires.
// Every field is optional; nil fields make their handler a no-op.
type SettingsDeps struct {
	Ctx       *context.SettingsContext
	Prompt    PromptHelper
	Confirm   ConfirmHelper
	ShowToast func(string)

	// StackDepth returns the current focus-stack depth. Nil-safe:
	// defaults to 1. Unused currently but reserved for future
	// quit-or-close semantics.
	StackDepth func() int

	// OnSaveConfig is the save seam. Invoked with the validated config
	// after Enter passes ValidateUserConfig. A nil callback makes
	// Confirm a no-op.
	OnSaveConfig func(*config.UserConfig) error

	// ValidationDeps supplies the action/scope predicates for
	// config.ValidateUserConfig. A zero value is acceptable; nil
	// predicates are treated as "always returns false".
	ValidationDeps config.ValidationDeps

	// Close pops the settings modal off the focus stack on Esc. A
	// nil callback makes Close a no-op.
	Close func()

	// IsPromptActive returns true when a prompt popup is on top of
	// the focus stack. Used by GetDisabled on Enter to prevent the
	// settings.confirm binding from firing while the user is editing
	// a field through the PROMPT popup.
	IsPromptActive func() bool
}

// NewSettingsController constructs the controller.
func NewSettingsController(c *common.Common, core CoreDeps, ui UIDeps) *SettingsController {
	return &SettingsController{
		baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui}),
	}
}

// SetDeps wires the late-bound dependencies. Called by the orchestrator
// once the settings context + helpers exist.
func (s *SettingsController) SetDeps(d SettingsDeps) { s.deps = d }

// GetKeybindings returns the SETTINGS-scope bindings.
func (s *SettingsController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := s.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: '['}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsPrevTab,
			Description: tr.Actions.TableInspectPrevTab,
		},
		{
			Sequence:    []types.ChordKey{{Code: ']'}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsNextTab,
			Description: tr.Actions.TableInspectNextTab,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsNextTab,
			Description: tr.Actions.TableInspectNextTab,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsFieldDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsFieldUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'i'}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsFieldEdit,
			Description: tr.Actions.EditConnection,
		},
		{
			Sequence:    []types.ChordKey{{Code: ' '}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsFieldToggle,
			Description: tr.Actions.ToggleField,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsConfirm,
			Description: tr.Actions.Confirm,
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsClose,
			Description: tr.Actions.Cancel,
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'a'}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsKeybindingAdd,
			Description: "Add keybinding",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd'}},
			Mode:        types.ModeNormal,
			Scope:       types.SETTINGS,
			ActionID:    commands.SettingsKeybindingDelete,
			Description: "Delete keybinding",
		},
	}
}

// RegisterActions registers the in-modal handlers.
func (s *SettingsController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsNextTab,
		Description: "Settings next tab",
		Handler:     s.NextTab,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsPrevTab,
		Description: "Settings prev tab",
		Handler:     s.PrevTab,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsFieldUp,
		Description: "Settings field up",
		Handler:     s.FieldUp,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsFieldDown,
		Description: "Settings field down",
		Handler:     s.FieldDown,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsFieldEdit,
		Description: "Settings field edit",
		Handler:     s.FieldEdit,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsFieldToggle,
		Description: "Settings field toggle",
		Handler:     s.FieldToggle,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsConfirm,
		Description: "Settings confirm (save)",
		Handler:     s.Confirm,
		GetDisabled: func(_ commands.ExecCtx) (string, bool) {
			if s.deps.IsPromptActive != nil && s.deps.IsPromptActive() {
				return "prompt is active", true
			}
			return "", false
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsClose,
		Description: "Settings close",
		Handler:     s.Close,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsKeybindingAdd,
		Description: "Settings add keybinding",
		Handler:     s.KeybindingAdd,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SettingsKeybindingDelete,
		Description: "Settings delete keybinding",
		Handler:     s.KeybindingDelete,
	})
}

// AttachToContext registers GetKeybindings on the SETTINGS context.
func (s *SettingsController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(s.GetKeybindings)
}

// NextTab advances the active tab with wrap-around.
func (s *SettingsController) NextTab(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}
	s.deps.Ctx.NextTab()
	s.deps.Ctx.ClearError()
	return nil
}

// PrevTab rewinds the active tab with wrap-around.
func (s *SettingsController) PrevTab(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}
	s.deps.Ctx.PrevTab()
	s.deps.Ctx.ClearError()
	return nil
}

// FieldUp moves field focus up (-1), clamped.
func (s *SettingsController) FieldUp(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}
	s.deps.Ctx.SetFocusField(s.deps.Ctx.GetFocusField() - 1)
	return nil
}

// FieldDown moves field focus down (+1), clamped.
func (s *SettingsController) FieldDown(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}
	s.deps.Ctx.SetFocusField(s.deps.Ctx.GetFocusField() + 1)
	return nil
}

// FieldEdit handles i. On the Keys tab it opens the PROMPT popup to rebind
// the focused keybinding row. On a text field it opens the PROMPT popup
// seeded with the current value. On a toggle field it is a no-op (space
// handles toggles).
func (s *SettingsController) FieldEdit(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}

	if s.deps.Ctx.ActiveTab() == 5 {
		return s.editKeybinding()
	}

	f := s.deps.Ctx.GetFocusedField()
	if f == nil {
		return nil
	}

	if f.Kind() != context.SettingsFieldText {
		return nil
	}

	if s.deps.Prompt == nil {
		return nil
	}

	label := f.Label()
	initial := s.deps.Ctx.GetFocusedFieldValue()

	return s.deps.Prompt.Prompt(label, initial,
		func(value string) error {
			s.deps.Ctx.SetFocusedFieldValue(value)
			return nil
		},
		func() error { return nil },
	)
}

// FieldToggle handles space → flips the focused toggle field. No-op on
// text fields and the Keys tab.
func (s *SettingsController) FieldToggle(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}
	if s.deps.Ctx.ActiveTab() == 5 {
		return nil
	}
	f := s.deps.Ctx.GetFocusedField()
	if f == nil || f.Kind() != context.SettingsFieldToggle {
		return nil
	}
	s.deps.Ctx.ToggleFocused()
	return nil
}

// Confirm validates the edited config and, on success, invokes the save
// callback. On failure it stamps an inline error on the active tab.
func (s *SettingsController) Confirm(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil {
		return nil
	}

	cfg := s.deps.Ctx.GetEditedConfig()
	if cfg == nil {
		return nil
	}

	_, errs := config.ValidateUserConfig(cfg, s.deps.ValidationDeps)
	if len(errs) > 0 {
		msg := errs[0].Error()
		for i := 1; i < len(errs); i++ {
			msg += "; " + errs[i].Error()
		}
		s.deps.Ctx.SetError(msg)
		if s.deps.ShowToast != nil {
			s.deps.ShowToast("Validation failed: " + msg)
		}
		return nil
	}

	if s.deps.OnSaveConfig != nil {
		if err := s.deps.OnSaveConfig(cfg); err != nil {
			s.deps.Ctx.SetError(err.Error())
			return nil
		}
	}

	if s.deps.ShowToast != nil {
		s.deps.ShowToast("Settings saved")
	}

	if s.deps.Close != nil {
		s.deps.Close()
	}
	return nil
}

// Close pops the settings modal off the focus stack.
func (s *SettingsController) Close(_ commands.ExecCtx) error {
	if s.deps.Close != nil {
		s.deps.Close()
	}
	return nil
}

// KeybindingAdd walks the user through 5 sequential prompts (mode,
// scope, key, action, description) and appends the new KeybindingConfig
// to the edited config's Keybindings slice.
func (s *SettingsController) KeybindingAdd(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil || s.deps.Prompt == nil {
		return nil
	}

	ctx := s.deps.Ctx
	prompt := s.deps.Prompt

	mode := "n"
	scope := "global"
	key := ""
	action := ""
	desc := ""

	return prompt.Prompt("Mode", mode,
		func(v string) error {
			mode = v
			return prompt.Prompt("Scope", scope,
				func(v string) error {
					scope = v
					return prompt.Prompt("Key", key,
						func(v string) error {
							key = v
							return prompt.Prompt("Action", action,
								func(v string) error {
									action = v
									return prompt.Prompt("Description", desc,
										func(v string) error {
											desc = v
											cfg := ctx.GetEditedConfig()
											if cfg == nil {
												return nil
											}
											cfg.Keybindings = append(cfg.Keybindings, config.KeybindingConfig{
												Mode:        mode,
												Scope:       scope,
												Key:         key,
												Action:      action,
												Description: desc,
											})
											ctx.RebuildFormStates()
											return nil
										},
										func() error { return nil },
									)
								},
								func() error { return nil },
							)
						},
						func() error { return nil },
					)
				},
				func() error { return nil },
			)
		},
		func() error { return nil },
	)
}

// editKeybinding opens the PROMPT popup seeded with the focused Keys-tab
// row's current key. On submit it rebinds the row, creating or updating a
// user override entry.
func (s *SettingsController) editKeybinding() error {
	if s.deps.Prompt == nil {
		return nil
	}
	row, ok := s.deps.Ctx.FocusedKeyRow()
	if !ok {
		return nil
	}
	return s.deps.Prompt.Prompt(fmt.Sprintf("Key for %s", row.Action), row.Key,
		func(v string) error {
			s.deps.Ctx.EditFocusedKeybinding(v)
			return nil
		},
		func() error { return nil },
	)
}

// KeybindingDelete opens a confirmation popup and, on confirm, removes the
// selected keybinding's user override (reverting it to its shipped default).
// No-op when not on the Keys tab or when no row is focused. Shipped defaults
// with no override cannot be deleted — the user is nudged to rebind instead.
func (s *SettingsController) KeybindingDelete(_ commands.ExecCtx) error {
	if s.deps.Ctx == nil || s.deps.Ctx.ActiveTab() != 5 {
		return nil
	}

	row, ok := s.deps.Ctx.FocusedKeyRow()
	if !ok {
		return nil
	}

	if !row.IsOverride {
		if s.deps.ShowToast != nil {
			s.deps.ShowToast("Cannot delete a built-in keybinding; press i to rebind it")
		}
		return nil
	}

	if s.deps.Confirm == nil {
		return nil
	}

	body := fmt.Sprintf("Delete keybinding \"%s\" → %s?", row.Key, row.Action)
	return s.deps.Confirm.Confirm(
		"Delete keybinding",
		body,
		func() error {
			s.deps.Ctx.DeleteFocusedKeybinding()
			return nil
		},
		nil,
	)
}
