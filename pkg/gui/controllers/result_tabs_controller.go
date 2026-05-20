package controllers

import (
	"fmt"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ResultTabsManager is the narrow surface ResultTabsController dispatches
// to. The concrete satisfier is ui.ResultTabsHelper (dbsavvy-66p.12); the
// interface keeps the controller package free of the helpers/ui import
// cycle.
type ResultTabsManager interface {
	Jump(i int)
	Cycle(dir int)
	CloseActive() error
	PinActive() bool
	CancelActive() error
	// Page advances (+1) / rewinds (-1) the active tab's grid by one
	// page. dbsavvy-uv0.3.
	Page(dir int)
	// ReadToEnd drains the active tab's stream to completion (with a
	// confirmation prompt above the configured warn threshold).
	// dbsavvy-uv0.3.
	ReadToEnd()

	// In-grid /regex filter surface (dbsavvy-uv0.4). FilterPrompt opens
	// the /-labelled prompt and, on submit, applies the regex to the
	// active tab's grid (firing the once-per-tab caveat toast when the
	// tab is incomplete). FilterToggleAllCols flips the allCols flag of
	// the active filter; FilterJumpNext / FilterJumpPrev advance the
	// cursor to the next / previous match; FilterClear drops the active
	// filter; FilterActive reports whether any tab currently has an
	// active filter (used to gate the shared <esc> chord).
	FilterPrompt()
	FilterToggleAllCols()
	FilterJumpNext()
	FilterJumpPrev()
	FilterClear()
	FilterActive() bool

	// SortPick opens the column-picker overlay for the active tab. On
	// submit, SetSort(col) fires; cycling through asc/desc/clear per the
	// AC. No-op when no tab is active or the picker dep is unwired.
	// dbsavvy-uv0.5.
	SortPick()
}

// ResultTabsController publishes the multi-tab keybindings:
//
//   - <leader>1..<leader>9 — Jump(i) (GLOBAL scope so digit jumps fire
//     from any focused view)
//   - gt / gT — Cycle next / prev (RESULT_GRID scope)
//   - <leader>X — Close active (RESULT_GRID scope)
//   - <leader>= — Pin / unpin active (RESULT_GRID scope)
//   - <leader>x — CancelActive (RESULT_GRID scope; mirrors the
//     QueryEditor's <leader>x cancel binding so the user can cancel
//     from either side of the pair)
//
// dbsavvy-66p.12.
type ResultTabsController struct {
	baseController
	mgr ResultTabsManager
}

// NewResultTabsController constructs the controller. mgr may be nil
// (test-only); handlers nil-check before dispatching.
func NewResultTabsController(c *common.Common, helpers HelperBag, mgr ResultTabsManager) *ResultTabsController {
	return &ResultTabsController{baseController: newBase(c, helpers), mgr: mgr}
}

// GetKeybindings returns the 14 result-tab bindings.
func (r *ResultTabsController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := r.tr()
	type bspec struct {
		shorthand   string
		actionID    string
		description string
		scope       types.ContextKey
	}
	specs := []bspec{
		// <leader>1..9 jump (GLOBAL — fire from any view).
		{"<leader>1", commands.ResultTabJump1, tr.Actions.ResultTabJump + " 1", types.GLOBAL},
		{"<leader>2", commands.ResultTabJump2, tr.Actions.ResultTabJump + " 2", types.GLOBAL},
		{"<leader>3", commands.ResultTabJump3, tr.Actions.ResultTabJump + " 3", types.GLOBAL},
		{"<leader>4", commands.ResultTabJump4, tr.Actions.ResultTabJump + " 4", types.GLOBAL},
		{"<leader>5", commands.ResultTabJump5, tr.Actions.ResultTabJump + " 5", types.GLOBAL},
		{"<leader>6", commands.ResultTabJump6, tr.Actions.ResultTabJump + " 6", types.GLOBAL},
		{"<leader>7", commands.ResultTabJump7, tr.Actions.ResultTabJump + " 7", types.GLOBAL},
		{"<leader>8", commands.ResultTabJump8, tr.Actions.ResultTabJump + " 8", types.GLOBAL},
		{"<leader>9", commands.ResultTabJump9, tr.Actions.ResultTabJump + " 9", types.GLOBAL},
		// Cycle / close / pin / cancel — RESULT_GRID scope.
		{"gt", commands.ResultTabNext, tr.Actions.ResultTabNext, types.RESULT_GRID},
		{"gT", commands.ResultTabPrev, tr.Actions.ResultTabPrev, types.RESULT_GRID},
		{"<leader>X", commands.ResultTabClose, tr.Actions.ResultTabClose, types.RESULT_GRID},
		{"<leader>=", commands.ResultTabPin, tr.Actions.ResultTabPin, types.RESULT_GRID},
		{"<leader>x", commands.ResultTabCancel, tr.Actions.ResultTabCancel, types.RESULT_GRID},
		// dbsavvy-uv0.3: pagination + read-to-end chords.
		{"]p", commands.ResultPageNext, tr.Actions.ResultPageNext, types.RESULT_GRID},
		{"[p", commands.ResultPagePrev, tr.Actions.ResultPagePrev, types.RESULT_GRID},
		{"G", commands.ResultReadToEnd, tr.Actions.ResultReadToEnd, types.RESULT_GRID},
		// dbsavvy-uv0.4: /regex filter chords.
		{"/", commands.ResultFilterPrompt, tr.Actions.ResultFilterPrompt, types.RESULT_GRID},
		{"<c-a>", commands.ResultFilterToggleAll, tr.Actions.ResultFilterToggleAll, types.RESULT_GRID},
		{"n", commands.ResultFilterNext, tr.Actions.ResultFilterNext, types.RESULT_GRID},
		{"N", commands.ResultFilterPrev, tr.Actions.ResultFilterPrev, types.RESULT_GRID},
		{"<esc>", commands.ResultFilterClear, tr.Actions.ResultFilterClear, types.RESULT_GRID},
		// dbsavvy-uv0.5: <leader>s sort picker.
		{"<leader>s", commands.ResultSortPick, tr.Actions.ResultSortPick, types.RESULT_GRID},
	}
	out := make([]*types.ChordBinding, 0, len(specs))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal | types.ModeInsert,
			Scope:       s.scope,
			ActionID:    s.actionID,
			Description: s.description,
		})
	}
	return out
}

// RegisterActions registers the 14 handlers with reg. Jump handlers
// are bound to a per-slot integer (1..9); the cycle / close / pin /
// cancel handlers delegate to the manager.
func (r *ResultTabsController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	tr := r.tr()
	for i := 1; i <= 9; i++ {
		slot := i
		id := jumpActionID(i)
		_ = reg.Register(&commands.Command{
			ID:          id,
			Description: fmt.Sprintf("%s %d", tr.Actions.ResultTabJump, slot),
			Tag:         "Result",
			Handler: func(_ commands.ExecCtx) error {
				if r.mgr != nil {
					r.mgr.Jump(slot)
				}
				return nil
			},
		})
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultTabNext,
		Description: tr.Actions.ResultTabNext,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.Cycle(1)
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultTabPrev,
		Description: tr.Actions.ResultTabPrev,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.Cycle(-1)
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultTabClose,
		Description: tr.Actions.ResultTabClose,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				return r.mgr.CloseActive()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultTabPin,
		Description: tr.Actions.ResultTabPin,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.PinActive()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultTabCancel,
		Description: tr.Actions.ResultTabCancel,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				return r.mgr.CancelActive()
			}
			return nil
		},
	})
	// dbsavvy-uv0.3: ]p / [p / G handlers.
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultPageNext,
		Description: tr.Actions.ResultPageNext,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.Page(1)
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultPagePrev,
		Description: tr.Actions.ResultPagePrev,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.Page(-1)
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultReadToEnd,
		Description: tr.Actions.ResultReadToEnd,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.ReadToEnd()
			}
			return nil
		},
	})
	// dbsavvy-uv0.4: /regex filter handlers.
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterPrompt,
		Description: tr.Actions.ResultFilterPrompt,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.FilterPrompt()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterToggleAll,
		Description: tr.Actions.ResultFilterToggleAll,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.FilterToggleAllCols()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterNext,
		Description: tr.Actions.ResultFilterNext,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.FilterJumpNext()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterPrev,
		Description: tr.Actions.ResultFilterPrev,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.FilterJumpPrev()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterClear,
		Description: tr.Actions.ResultFilterClear,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil && r.mgr.FilterActive() {
				r.mgr.FilterClear()
			}
			return nil
		},
	})
	// dbsavvy-uv0.5: <leader>s sort picker handler.
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultSortPick,
		Description: tr.Actions.ResultSortPick,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.SortPick()
			}
			return nil
		},
	})
}

// AttachToContext registers GetKeybindings on the RESULT_GRID context.
// The context is a StubContext today so the AddKeybindingsFn call is a
// no-op; bindings reach the trie via AllDefaultBindings.
func (r *ResultTabsController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(r.GetKeybindings)
}

// jumpActionID maps slot i (1..9) to the matching action-ID constant.
func jumpActionID(i int) string {
	switch i {
	case 1:
		return commands.ResultTabJump1
	case 2:
		return commands.ResultTabJump2
	case 3:
		return commands.ResultTabJump3
	case 4:
		return commands.ResultTabJump4
	case 5:
		return commands.ResultTabJump5
	case 6:
		return commands.ResultTabJump6
	case 7:
		return commands.ResultTabJump7
	case 8:
		return commands.ResultTabJump8
	case 9:
		return commands.ResultTabJump9
	default:
		return ""
	}
}
