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

// Confirm fires the controller-supplied callback. Nil callback → no-op.
func (l *ListControllerTrait[T]) Confirm(ctx commands.ExecCtx) error {
	if l.onConfirm == nil {
		return nil
	}
	return l.onConfirm(ctx)
}

// RegisterActions registers the trait's three actions (ListUp /
// ListDown / ListConfirm) with reg. The Controllers aggregate calls
// this on a single representative trait so the actions exist in the
// Registry without colliding across the five rails.
func (l *ListControllerTrait[T]) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ListUp,
		Description: "Move list cursor up",
		Handler:     l.Up,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ListDown,
		Description: "Move list cursor down",
		Handler:     l.Down,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ListConfirm,
		Description: "Activate list row",
		Handler:     l.Confirm,
	})
}

// baseBindings returns the j/k/<CR> bindings every side rail shares.
// Concrete controllers append rail-specific bindings (digit switch,
// H/U, a, etc.).
func (l *ListControllerTrait[T]) baseBindings() []*types.ChordBinding {
	tr := l.tr()
	scope := types.ContextKey(l.viewName)
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    commands.ListDown,
			Description: tr.Actions.Down,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    commands.ListUp,
			Description: tr.Actions.Up,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    commands.ListConfirm,
			Description: tr.Actions.Confirm,
		},
	}
}
