package controllers

import (
	"context"
	"errors"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// SchemasController owns the SCHEMAS rail bindings: j/k navigation via
// the trait, H (hide), U (unhide), and <leader>H (toggle-show-hidden)
// via the multi-key chord trie (no oneshot dispatcher).
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
	// <CR> on SCHEMAS fires HelperBag.OnSchemaActivate with the
	// cursor-selected schema name, which the orchestrator wires to a
	// worker-goroutine LoadTables that populates the TABLES rail
	// (dbsavvy-04n). Empty selection or nil callback → no-op.
	onConfirm := func(_ commands.ExecCtx) error {
		if picker == nil || helpers.OnSchemaActivate == nil {
			return nil
		}
		name := picker.SelectedSchemaName()
		if name == "" {
			return nil
		}
		helpers.OnSchemaActivate(name)
		return nil
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.SCHEMAS), cursor, picker, onConfirm)
	return ctrl
}

// HideSchema is the `H` handler.
func (s *SchemasController) HideSchema(_ commands.ExecCtx) error {
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
// the unhide via a direct AppState mutation.
func (s *SchemasController) UnhideSchema(_ commands.ExecCtx) error {
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

// RefreshRail is the `r` handler — reloads the SCHEMAS rail through
// HelperBag.Refresh. Nil-safe.
func (s *SchemasController) RefreshRail(_ commands.ExecCtx) error {
	if s.helpers.Refresh == nil {
		return nil
	}
	return s.helpers.Refresh.RefreshSchemas(context.Background())
}

// ToggleShowHidden is the `<leader>H` handler.
func (s *SchemasController) ToggleShowHidden(_ commands.ExecCtx) error {
	if s.picker == nil {
		return nil
	}
	s.picker.ToggleShowHidden()
	return nil
}

// GetKeybindings returns the schemas rail bindings.
func (s *SchemasController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := s.tr()
	view := viewName(types.SCHEMAS)
	out := s.baseBindings()

	out = append(out,
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'H'}},
			Mode:        types.ModeNormal,
			Scope:       types.SCHEMAS,
			ActionID:    commands.SchemaHide,
			Description: tr.Actions.HideSchema,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'U'}},
			Mode:        types.ModeNormal,
			Scope:       types.SCHEMAS,
			ActionID:    commands.SchemaUnhide,
			Description: tr.Actions.UnhideSchema,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'r'}},
			Mode:        types.ModeNormal,
			Scope:       types.SCHEMAS,
			ActionID:    listActionID(commands.RailRefresh, view),
			Description: tr.Actions.RefreshRail,
		},
	)

	// <leader>H multi-key chord. SequenceFromShorthand emits a
	// KeyLeader placeholder which Build expands using cfg.Leader before
	// the trie insert.
	if seq, err := keys.SequenceFromShorthand("<leader>H"); err == nil {
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.SCHEMAS,
			ActionID:    commands.SchemaToggleShowHidden,
			Description: tr.Actions.ToggleShowHidden,
		})
	}

	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions registers the rail-specific action handlers this
// controller owns with reg.
func (s *SchemasController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.SchemaHide,
		Description: "Hide schema",
		Handler:     s.HideSchema,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SchemaUnhide,
		Description: "Unhide schema",
		Handler:     s.UnhideSchema,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.SchemaToggleShowHidden,
		Description: "Toggle show-hidden schemas",
		Handler:     s.ToggleShowHidden,
	})
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.RailRefresh, viewName(types.SCHEMAS)),
		Description: "Refresh schemas rail",
		Handler:     s.RefreshRail,
	})
}

// AttachToContext registers GetKeybindings on the supplied context.
func (s *SchemasController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(s.GetKeybindings)
}
