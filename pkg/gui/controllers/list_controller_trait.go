package controllers

import (
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// SideListCursor is the minimal cursor-management surface every side
// rail controller drives. SideListContext (from pkg/gui/context)
// satisfies it; tests inject an in-memory fake.
type SideListCursor interface {
	Cursor() int
	SetCursor(i int)
	Items() []any
}

// ListControllerTrait[T] is the generic side-rail trait that backs
// j/k cursor movement and <CR> activation. Concrete controllers
// embed a *ListControllerTrait[ConcretePicker] and provide a
// confirm callback that uses the type-asserted selected item.
//
// The generic parameter T is the picker type that knows how to map
// a SideListCursor's selected index to a domain-typed value (e.g.
// *models.Connection, *models.Table). It is NOT used to constrain
// the cursor implementation itself — cursor mechanics are uniform
// across all five side rails.
type ListControllerTrait[T any] struct {
	baseController

	viewName string
	cursor   SideListCursor

	// onConfirm is invoked by <CR>. May be nil (no-op binding).
	onConfirm commands.Handler

	// picker is exposed so concrete controllers can resolve the
	// cursor index to a domain entity inside their own handlers.
	picker T
}

// NewListControllerTrait constructs the trait. Concrete controllers
// pass their picker and a confirm callback.
func NewListControllerTrait[T any](
	base baseController,
	viewName string,
	cursor SideListCursor,
	picker T,
	onConfirm commands.Handler,
) *ListControllerTrait[T] {
	return &ListControllerTrait[T]{
		baseController: base,
		viewName:       viewName,
		cursor:         cursor,
		onConfirm:      onConfirm,
		picker:         picker,
	}
}

// Down moves the cursor by +1. Safe on empty lists (no-op).
func (l *ListControllerTrait[T]) Down(_ commands.ExecCtx) error {
	if l.cursor == nil {
		return nil
	}
	l.cursor.SetCursor(l.cursor.Cursor() + 1)
	return nil
}

// Up moves the cursor by -1. Safe on empty lists (no-op).
func (l *ListControllerTrait[T]) Up(_ commands.ExecCtx) error {
	if l.cursor == nil {
		return nil
	}
	l.cursor.SetCursor(l.cursor.Cursor() - 1)
	return nil
}

// First jumps the cursor to the first row. Safe on empty lists (no-op).
func (l *ListControllerTrait[T]) First(_ commands.ExecCtx) error {
	if l.cursor == nil {
		return nil
	}
	l.cursor.SetCursor(0)
	return nil
}

// Last jumps the cursor to the final row. Safe on empty lists (no-op).
func (l *ListControllerTrait[T]) Last(_ commands.ExecCtx) error {
	if l.cursor == nil {
		return nil
	}
	n := len(l.cursor.Items())
	if n == 0 {
		return nil
	}
	l.cursor.SetCursor(n - 1)
	return nil
}

// Confirm fires the controller-supplied callback. Nil callback → no-op.
func (l *ListControllerTrait[T]) Confirm(ctx commands.ExecCtx) error {
	if l.onConfirm == nil {
		return nil
	}
	return l.onConfirm(ctx)
}

// RegisterActions registers this trait's three actions (ListUp /
// ListDown / ListConfirm) with reg under per-rail IDs derived from
// viewName. Each rail owns its own handler so j/k/<CR> dispatched on
// rail X mutate rail X's cursor (dbsavvy-6m9). The aggregate
// Controllers.RegisterActions must invoke this on every rail's trait.
func (l *ListControllerTrait[T]) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.ListUp, l.viewName),
		Description: "Move list cursor up (" + l.viewName + ")",
		Handler:     l.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.ListDown, l.viewName),
		Description: "Move list cursor down (" + l.viewName + ")",
		Handler:     l.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.ListConfirm, l.viewName),
		Description: "Activate list row (" + l.viewName + ")",
		Handler:     l.Confirm,
	})
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.ListJumpFirst, l.viewName),
		Description: "Jump list cursor to first row (" + l.viewName + ")",
		Handler:     l.First,
	})
	_ = reg.Register(&commands.Command{
		ID:          listActionID(commands.ListJumpLast, l.viewName),
		Description: "Jump list cursor to last row (" + l.viewName + ")",
		Handler:     l.Last,
	})
}

// baseBindings returns the j/k/<CR> bindings every side rail shares.
// Each binding's ActionID is per-rail (see RegisterActions) so dispatch
// to the matching trait handler is unambiguous when multiple rails
// register the same chord sequence.
func (l *ListControllerTrait[T]) baseBindings() []*types.ChordBinding {
	tr := l.tr()
	scope := types.ContextKey(l.viewName)
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    listActionID(commands.ListDown, l.viewName),
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    listActionID(commands.ListUp, l.viewName),
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    listActionID(commands.ListConfirm, l.viewName),
			Description: tr.Actions.Confirm,
			ShowInBar:   true,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'g'}, {Code: 'g'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    listActionID(commands.ListJumpFirst, l.viewName),
			Description: tr.Actions.JumpFirst,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'G'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    listActionID(commands.ListJumpLast, l.viewName),
			Description: tr.Actions.JumpLast,
		},
	}
}

// listActionID composes the per-rail ActionID. The bare commands.ListUp
// / ListDown / ListConfirm constants act as namespace prefixes; the
// viewName suffix disambiguates per-rail dispatch.
func listActionID(prefix, viewName string) string {
	return prefix + ":" + viewName
}
