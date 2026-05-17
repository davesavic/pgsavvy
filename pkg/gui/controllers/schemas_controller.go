package controllers

import (
	"errors"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// SchemasController owns the SCHEMAS rail bindings: j/k navigation
// via the trait, H (hide), U (unhide), and <leader>H (toggle-show-
// hidden) via the OneshotArmer interface.
type SchemasController struct {
	*ListControllerTrait[SchemaPicker]
}

// NewSchemasController constructs the controller. cursor and picker
// typically point at the same *context.SchemasContext value.
func NewSchemasController(
	c *common.Common,
	helpers HelperBag,
	cursor SideListCursor,
	picker SchemaPicker,
) *SchemasController {
	base := newBase(c, helpers)
	ctrl := &SchemasController{}
	// <CR> on SCHEMAS is a no-op in T7a — selecting a schema in the
	// rail drives the TABLES rail load, which is owned by the
	// downstream bootstrap (T10) via a context-switch closure.
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.SCHEMAS), cursor, picker, func() error { return nil })
	return ctrl
}

// HideSchema is the `H` handler.
func (s *SchemasController) HideSchema() error {
	if s.helpers.SchemasHelper == nil || s.helpers.ActiveConnection == nil {
		return nil
	}
	name := ""
	if p := s.picker; p != nil {
		name = p.SelectedSchemaName()
	}
	if name == "" {
		return nil
	}
	connID := s.helpers.ActiveConnection.ActiveConnectionID()
	if connID == "" {
		return nil
	}
	err := s.helpers.SchemasHelper.HideSchema(connID, name)
	return s.wrapErr("schemas.hide", err)
}

// UnhideSchema is the `U` handler. On ErrNeedsConfirmation it routes
// through the ConfirmHelper popup; the user's "Yes" callback re-issues
// the unhide via a direct AppState mutation (delegated to T7b's
// helper plumbing — here we simply invoke the SchemasHelper.UnhideSchema
// path; T7b's confirm-yes callback will be wired to it).
func (s *SchemasController) UnhideSchema() error {
	if s.helpers.SchemasHelper == nil || s.helpers.ActiveConnection == nil {
		return nil
	}
	name := ""
	if p := s.picker; p != nil {
		name = p.SelectedSchemaName()
	}
	if name == "" {
		return nil
	}
	connID := s.helpers.ActiveConnection.ActiveConnectionID()
	if connID == "" {
		return nil
	}

	builtin, profile := []string(nil), []string(nil)
	if s.helpers.HiddenPatterns != nil {
		builtin, profile = s.helpers.HiddenPatterns()
	}

	err := s.helpers.SchemasHelper.UnhideSchema(connID, name, builtin, profile)
	if errors.Is(err, data.ErrNeedsConfirmation) {
		// Route through ConfirmHelper. The user-approved path re-invokes
		// the helper with empty builtin/profile lists, which bypasses
		// the predicate.
		if s.helpers.Confirm == nil {
			return nil
		}
		tr := s.tr()
		return s.helpers.Confirm.Confirm(
			tr.UnhideConfirmationTitle,
			tr.UnhideConfirmationBody,
			func() error {
				return s.helpers.SchemasHelper.UnhideSchema(connID, name, nil, nil)
			},
			nil,
		)
	}
	return s.wrapErr("schemas.unhide", err)
}

// ToggleShowHidden is the `<leader>H` suffix handler armed by OneshotArmer.
func (s *SchemasController) ToggleShowHidden() error {
	if s.picker == nil {
		return nil
	}
	s.picker.ToggleShowHidden()
	return nil
}

// armLeader is the bare `<leader>` keystroke handler — it arms the
// oneshot dispatcher waiting for the `H` suffix.
func (s *SchemasController) armLeader() error {
	if s.helpers.OneShot == nil {
		return nil
	}
	suffixes := map[rune]func() error{
		'H': s.ToggleShowHidden,
	}
	err := s.helpers.OneShot.Arm(s.leader(), suffixes, string(types.SCHEMAS))
	return s.wrapErr("schemas.arm_leader", err)
}

// GetKeybindings returns the schemas rail bindings.
func (s *SchemasController) GetKeybindings(_ types.KeybindingsOpts) []*types.KeyBinding {
	tr := s.tr()
	view := viewName(types.SCHEMAS)
	out := s.baseBindings()

	out = append(out,
		&types.KeyBinding{
			ViewName:    view,
			Key:         gocui.NewKeyRune('H'),
			Mod:         gocui.ModNone,
			Handler:     s.HideSchema,
			Description: tr.Actions.HideSchema,
		},
		&types.KeyBinding{
			ViewName:    view,
			Key:         gocui.NewKeyRune('U'),
			Mod:         gocui.ModNone,
			Handler:     s.UnhideSchema,
			Description: tr.Actions.UnhideSchema,
		},
	)

	// <leader> binding. The OneshotArmer interface is supplied by T7b;
	// when absent (early boot / unit tests) the binding is still
	// registered but the arm() returns no-op.
	leaderKey := leaderKeyFromLabel(s.leader())
	out = append(out, &types.KeyBinding{
		ViewName:    view,
		Key:         leaderKey,
		Mod:         gocui.ModNone,
		Handler:     s.armLeader,
		Description: tr.Actions.ToggleShowHidden,
	})

	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// AttachToContext registers GetKeybindings on the supplied context.
func (s *SchemasController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(s.GetKeybindings)
}

// leaderKeyFromLabel maps a leader label string (e.g. "<space>") to
// the gocui.Key value the runtime will dispatch on. Only the two
// labels permitted by G1-C are honored here ("<space>" + the bare
// "<space>" fallback); any other label falls back to space because
// custom leader strings are an E5 (chord) feature.
func leaderKeyFromLabel(label string) types.Key {
	switch label {
	case "<space>", " ", "":
		return gocui.NewKeyRune(' ')
	}
	return gocui.NewKeyRune(' ')
}
