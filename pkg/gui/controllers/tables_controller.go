package controllers

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TablesController owns TABLES rail bindings: j/k via the trait, and
// <CR> fires HelperBag.OnTableActivate, which the orchestrator wires to
// run a bounded "SELECT * FROM <table>" for the selected table and push
// the results panel onto the focus stack.
type TablesController struct {
	*ListControllerTrait[TablePicker]
}

// NewTablesController constructs the controller.
func NewTablesController(
	c *common.Common,
	core CoreDeps,
	nav NavDeps,
	cursor SideListCursor,
	picker TablePicker,
) *TablesController {
	base := newBase(c, HelperBag{CoreDeps: core, NavDeps: nav})
	ctrl := &TablesController{}
	confirm := func(_ commands.ExecCtx) error {
		if picker == nil || base.helpers.OnTableActivate == nil {
			return nil
		}
		t := picker.SelectedTable()
		if t == nil {
			return nil
		}
		err := base.helpers.OnTableActivate(t)
		return base.wrapErr("tables.confirm", err)
	}
	ctrl.ListControllerTrait = NewListControllerTrait(base, viewName(types.TABLES), cursor, picker, confirm)
	return ctrl
}

// RefreshRail is the `r` handler — reloads the TABLES rail for the
// currently-selected schema via HelperBag.Refresh. Nil-safe.
func (c *TablesController) RefreshRail(_ commands.ExecCtx) error {
	if c.helpers.Refresh == nil {
		return nil
	}
	schema := ""
	if c.helpers.Schemas != nil {
		schema = c.helpers.Schemas.SelectedSchemaName()
	}
	if schema == "" {
		return nil
	}
	return c.helpers.Refresh.RefreshTables(context.Background(), schema)
}

// GetKeybindings returns the tables rail bindings.
func (c *TablesController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := c.tr()
	view := viewName(types.TABLES)
	out := c.baseBindings()
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Code: 'r'}},
		Mode:        types.ModeNormal,
		Scope:       types.TABLES,
		ActionID:    listActionID(commands.RailRefresh, view),
		Description: tr.Actions.RefreshRail,
	})
	// dbsavvy-3vf.9: `i` opens the TABLE_INSPECT popup for the currently
	// selected table. Handler is registered in the orchestrator
	// (gui.go) because it needs the focus tree + connectInvoker.
	out = append(out, &types.ChordBinding{
		Sequence:    []types.ChordKey{{Code: 'i'}},
		Mode:        types.ModeNormal,
		Scope:       types.TABLES,
		ActionID:    commands.TableInspectOpen,
		Description: tr.Actions.TableInspectOpen,
		ShowInBar:   true,
	})
	// dbsavvy-ioaj: rail highlight+jump search. Single action IDs bound
	// on TABLES; the orchestrator handler resolves the focused rail from
	// ctx.Scope. j/k/gg/G/<CR> (baseBindings) and r/i are untouched.
	out = append(out,
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: '/'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLES,
			ActionID:    commands.RailSearchPrompt,
			Description: tr.Actions.RailSearchPrompt,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'n'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLES,
			ActionID:    commands.RailSearchNext,
			Description: tr.Actions.RailSearchNext,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: 'N'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLES,
			ActionID:    commands.RailSearchPrev,
			Description: tr.Actions.RailSearchPrev,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLES,
			ActionID:    commands.RailSearchClear,
			Description: tr.Actions.RailSearchClear,
		},
	)

	out = append(out, railSwitchBindings(view, tr)...)
	return out
}

// RegisterActions registers the `r` rail-refresh handler under the
// per-rail action ID.
func (c *TablesController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.RailRefresh, viewName(types.TABLES)),
		Description: "Refresh tables rail",
		Handler:     c.RefreshRail,
	})
}

// AttachToContext registers GetKeybindings.
func (c *TablesController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(c.GetKeybindings)
}
