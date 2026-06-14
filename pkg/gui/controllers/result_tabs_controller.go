package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ResultTabsManager is the narrow surface ResultTabsController dispatches
// to. The concrete satisfier is ui.ResultTabsHelper; the
// interface keeps the controller package free of the helpers/ui import
// cycle.
type ResultTabsManager interface {
	Jump(i int)
	Cycle(dir int)
	CloseActive() error
	PinActive() bool
	CancelActive() error
	// Page advances (+1) / rewinds (-1) the active tab's grid by one
	// page.
	Page(dir int)
	// ReadToEnd drains the active tab's stream to completion (with a
	// confirmation prompt above the configured warn threshold).
	ReadToEnd()

	// In-grid search surface. SearchPrompt opens the
	// bottom-anchored live search input; each keystroke drives the active
	// tab's grid SetSearch (firing the once-per-tab caveat toast when the
	// tab is incomplete). SearchNextMatch / SearchPrevMatch move the
	// cursor to the next / previous match; SearchClear drops the active
	// search; SearchActive reports whether the active tab currently has an
	// active search (used to gate the shared <esc> chord).
	SearchPrompt()
	SearchNextMatch()
	SearchPrevMatch()
	SearchClear()
	SearchActive() bool

	// SortPick opens the column-picker overlay for the active tab. On
	// submit, SetSort(col) fires; cycling through asc/desc/clear per the
	// AC. No-op when no tab is active or the picker dep is unwired.
	SortPick()

	// HideOverlay opens the <leader>gH hide-cols overlay for the active
	// tab. Persistence is gated on the tab's recorded ResultIdentity
	// (HasRowIdentity).
	HideOverlay()

	// PromptExport opens the <leader>oe export menu for the active tab.
	PromptExport()

	// ToggleViewMode flips the active tab's grid between ViewModeGrid
	// and ViewModeExpanded and persists the new value globally via
	// AppState.LastResultViewMode.
	ToggleViewMode()

	// JumpLastOrReadToEnd dispatches `G`: in expanded mode jumps the
	// cursor to the last loaded record; in grid mode triggers the
	// ReadToEnd drain (with the existing >1M-row warn).
	JumpLastOrReadToEnd()

	// Result-grid motion delegators. Dispatch is viewMode-aware inside
	// the helper / grid.View.
	CursorDown()
	CursorUp()
	CursorLeft()
	CursorRight()
	ColFirst()
	ColLast()
	JumpFirst()
	JumpLast()
	HalfPageDown()
	HalfPageUp()
	WrappedLineDown()
	WrappedLineUp()
	SelectRow()
	SelectBlock()
	ClearSelection()
	SelectionActive() bool

	// Active returns the currently-active result tab, or nil when no
	// tab exists. Wired by Z1 follow-up so handlers can
	// resolve the tab + its grid for FK navigation, pending-edit
	// dispatch, and jump-list glue. *ui.ResultTabsHelper satisfies it.
	Active() *ui.Tab
	// SwitchToTabByID activates the tab whose ID stringifies to tabID
	// and returns it. Returns nil for stale entries. Used by <c-o> /
	// <c-i> jump navigation.
	SwitchToTabByID(tabID string) *ui.Tab
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
type ResultTabsController struct {
	baseController
	mgr ResultTabsManager
}

// NewResultTabsController constructs the controller. mgr may be nil
// (test-only); handlers nil-check before dispatching.
func NewResultTabsController(c *common.Common, core CoreDeps, ui UIDeps, edit EditDeps, mgr ResultTabsManager) *ResultTabsController {
	return &ResultTabsController{baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui, EditDeps: edit}), mgr: mgr}
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
		// pagination + read-to-end chords.
		{"]p", commands.ResultPageNext, tr.Actions.ResultPageNext, types.RESULT_GRID},
		{"[p", commands.ResultPagePrev, tr.Actions.ResultPagePrev, types.RESULT_GRID},
		{"G", commands.ResultJumpLast, tr.Actions.ResultJumpLast, types.RESULT_GRID},
		// in-grid search chords.
		{"/", commands.ResultFilterPrompt, tr.Actions.ResultFilterPrompt, types.RESULT_GRID},
		{"n", commands.ResultFilterNext, tr.Actions.ResultFilterNext, types.RESULT_GRID},
		{"N", commands.ResultFilterPrev, tr.Actions.ResultFilterPrev, types.RESULT_GRID},
		{"<esc>", commands.ResultFilterClear, tr.Actions.ResultFilterClear, types.RESULT_GRID},
		// <leader>s sort picker.
		{"<leader>s", commands.ResultSortPick, tr.Actions.ResultSortPick, types.RESULT_GRID},
		// <leader>gH hide-cols overlay.
		{"<leader>gH", commands.ResultHideOverlay, tr.Actions.ResultHideOverlay, types.RESULT_GRID},
		// <leader>oe opens the export menu.
		{"<leader>oe", commands.ResultExportPrompt, tr.Actions.ResultExportPrompt, types.RESULT_GRID},
		// expanded view toggle + ]G force-ReadToEnd +
		// result-grid motion bindings (viewMode-aware via the helper).
		{"<leader>gx", commands.ResultViewToggle, tr.Actions.ResultViewToggle, types.RESULT_GRID},
		{"]G", commands.ResultReadToEndForce, tr.Actions.ResultReadToEndForce, types.RESULT_GRID},
		{"j", commands.ResultCursorDown, tr.Actions.ResultCursorDown, types.RESULT_GRID},
		{"k", commands.ResultCursorUp, tr.Actions.ResultCursorUp, types.RESULT_GRID},
		{"h", commands.ResultCursorLeft, tr.Actions.ResultCursorLeft, types.RESULT_GRID},
		{"l", commands.ResultCursorRight, tr.Actions.ResultCursorRight, types.RESULT_GRID},
		// vim column motions. 0/$ jump to the first/last
		// column; w/b are single-column aliases of l/h.
		{"0", commands.ResultColFirst, tr.Actions.ResultColFirst, types.RESULT_GRID},
		{"$", commands.ResultColLast, tr.Actions.ResultColLast, types.RESULT_GRID},
		{"w", commands.ResultCursorRight, tr.Actions.ResultCursorRight, types.RESULT_GRID},
		{"b", commands.ResultCursorLeft, tr.Actions.ResultCursorLeft, types.RESULT_GRID},
		{"gg", commands.ResultJumpFirst, tr.Actions.ResultJumpFirst, types.RESULT_GRID},
		{"<c-d>", commands.ResultHalfPageDown, tr.Actions.ResultHalfPageDown, types.RESULT_GRID},
		{"<c-u>", commands.ResultHalfPageUp, tr.Actions.ResultHalfPageUp, types.RESULT_GRID},
		{"J", commands.ResultWrappedLineDown, tr.Actions.ResultWrappedLineDown, types.RESULT_GRID},
		{"K", commands.ResultWrappedLineUp, tr.Actions.ResultWrappedLineUp, types.RESULT_GRID},
		{"V", commands.ResultSelectRow, tr.Actions.ResultSelectRow, types.RESULT_GRID},
		{"<c-v>", commands.ResultSelectBlock, tr.Actions.ResultSelectBlock, types.RESULT_GRID},
		// dbsavvy U4: clipboard yank. `y` cell, `yy` row (TSV).
		{"y", commands.ResultYankCell, tr.Actions.ResultYankCell, types.RESULT_GRID},
		{"yy", commands.ResultYankRow, tr.Actions.ResultYankRow, types.RESULT_GRID},
	}
	out := make([]*types.ChordBinding, 0, len(specs))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence: seq,
			// INSERT deliberately excluded: leader-
			// prefixed bindings registered under (ModeInsert, GLOBAL)
			// make the leader rune (<space>) a chord prefix when the
			// editor is in INSERT mode, buffering it until tlen and
			// producing the "select*" → "select *" reordering bug.
			Mode:        types.ModeNormal,
			Scope:       s.scope,
			ActionID:    s.actionID,
			Description: s.description,
			// flag the y/yy clipboard yank chords for the
			// status options bar.
			ShowInBar: s.actionID == commands.ResultYankCell || s.actionID == commands.ResultYankRow,
		})
	}
	// rail-switch chords (1..6 + <tab>) under RESULT_GRID so
	// the user can navigate back out of the result pane. Without these
	// the master editor (scope=RESULT_GRID) dispatches FellThrough for
	// every digit and Tab, leaving the user stranded on the active tab.
	out = append(out, railSwitchBindings(string(types.RESULT_GRID), tr)...)

	// Z1: inline-edit + jump-list chords on RESULT_GRID. These
	// are registered AFTER railSwitchBindings so the trie's most-specific
	// match wins for `<c-i>` (ResultJumpForward) over the bare `<tab>`
	// RailSwitchNext binding above. CellEditEnter (`i`) is published by
	// CellEditorController, so it is intentionally absent here.
	bwqSpecs := []bspec{
		{"gd", commands.FKJumpForward, "FK forward jump", types.RESULT_GRID},
		{"gD", commands.FKReverseMenu, "FK reverse menu", types.RESULT_GRID},
		{"<c-o>", commands.ResultJumpBack, "Result jump back", types.RESULT_GRID},
		{"<c-i>", commands.ResultJumpForward, "Result jump forward", types.RESULT_GRID},
		{"<leader>cu", commands.PendingDiscardAtCursor, "Discard pending edit at cursor", types.RESULT_GRID},
		{"<leader>cU", commands.PendingDiscardAll, "Discard all pending edits", types.RESULT_GRID},
		{"<leader>cw", commands.CommitDialogOpen, "Open commit dialog", types.RESULT_GRID},
	}
	for _, s := range bwqSpecs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
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
	// ]p / [p / G handlers.
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
			// in expanded mode G means "last record";
			// the helper dispatches based on the active grid's viewMode.
			if r.mgr != nil {
				r.mgr.JumpLastOrReadToEnd()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultReadToEndForce,
		Description: tr.Actions.ResultReadToEndForce,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			// ]G — always ReadToEnd, regardless of viewMode.
			if r.mgr != nil {
				r.mgr.ReadToEnd()
			}
			return nil
		},
	})
	// in-grid search handlers.
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterPrompt,
		Description: tr.Actions.ResultFilterPrompt,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.SearchPrompt()
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
				r.mgr.SearchNextMatch()
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
				r.mgr.SearchPrevMatch()
			}
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultFilterClear,
		Description: tr.Actions.ResultFilterClear,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr == nil {
				return nil
			}
			// Selection-clear short-circuits AHEAD of search-clear so an
			// active selection + <esc> clears the selection, not the search.
			if r.mgr.SelectionActive() {
				r.mgr.ClearSelection()
				return nil
			}
			if r.mgr.SearchActive() {
				r.mgr.SearchClear()
			}
			return nil
		},
	})
	// <leader>s sort picker handler.
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
	// <leader>gH hide-cols overlay handler.
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultHideOverlay,
		Description: tr.Actions.ResultHideOverlay,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.HideOverlay()
			}
			return nil
		},
	})
	// <leader>oe export menu handler.
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultExportPrompt,
		Description: tr.Actions.ResultExportPrompt,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr != nil {
				r.mgr.PromptExport()
			}
			return nil
		},
	})
	// expanded view toggle + motion handlers.
	r.registerMotionHandler(reg, commands.ResultViewToggle, tr.Actions.ResultViewToggle, func() { r.mgr.ToggleViewMode() })
	r.registerMotionHandler(reg, commands.ResultCursorDown, tr.Actions.ResultCursorDown, func() { r.mgr.CursorDown() })
	r.registerMotionHandler(reg, commands.ResultCursorUp, tr.Actions.ResultCursorUp, func() { r.mgr.CursorUp() })
	r.registerMotionHandler(reg, commands.ResultCursorLeft, tr.Actions.ResultCursorLeft, func() { r.mgr.CursorLeft() })
	r.registerMotionHandler(reg, commands.ResultCursorRight, tr.Actions.ResultCursorRight, func() { r.mgr.CursorRight() })
	r.registerMotionHandler(reg, commands.ResultColFirst, tr.Actions.ResultColFirst, func() { r.mgr.ColFirst() })
	r.registerMotionHandler(reg, commands.ResultColLast, tr.Actions.ResultColLast, func() { r.mgr.ColLast() })
	r.registerMotionHandler(reg, commands.ResultJumpFirst, tr.Actions.ResultJumpFirst, func() { r.mgr.JumpFirst() })
	r.registerMotionHandler(reg, commands.ResultJumpLast, tr.Actions.ResultJumpLast, func() { r.mgr.JumpLast() })
	r.registerMotionHandler(reg, commands.ResultHalfPageDown, tr.Actions.ResultHalfPageDown, func() { r.mgr.HalfPageDown() })
	r.registerMotionHandler(reg, commands.ResultHalfPageUp, tr.Actions.ResultHalfPageUp, func() { r.mgr.HalfPageUp() })
	r.registerMotionHandler(reg, commands.ResultWrappedLineDown, tr.Actions.ResultWrappedLineDown, func() { r.mgr.WrappedLineDown() })
	r.registerMotionHandler(reg, commands.ResultWrappedLineUp, tr.Actions.ResultWrappedLineUp, func() { r.mgr.WrappedLineUp() })
	r.registerMotionHandler(reg, commands.ResultSelectRow, tr.Actions.ResultSelectRow, func() { r.mgr.SelectRow() })
	r.registerMotionHandler(reg, commands.ResultSelectBlock, tr.Actions.ResultSelectBlock, func() { r.mgr.SelectBlock() })

	// dbsavvy U4: clipboard yank handlers (`y` cell / `yy` row).
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultYankCell,
		Description: tr.Actions.ResultYankCell,
		Tag:         "Result",
		Handler:     r.yankHandler(false),
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultYankRow,
		Description: tr.Actions.ResultYankRow,
		Tag:         "Result",
		Handler:     r.yankHandler(true),
	})

	// Z1: inline-edit + jump-list action handlers and their
	// chord registration.
	r.registerBwqHandlers(reg)
}

// registerBwqHandlers wires the Z1 action handlers for
// the new RESULT_GRID chords. Split out so the main RegisterActions body
// stays readable. Each handler nil-checks its helper / collaborator and
// surfaces a toast if one is absent; in production those collaborators are
// wired, so the guard toasts do not fire.
func (r *ResultTabsController) registerBwqHandlers(reg *commands.Registry) {
	_ = reg.Register(&commands.Command{
		ID:          commands.FKJumpForward,
		Description: "FK forward jump (gd)",
		Tag:         "Row",
		Handler:     r.fkForwardHandler,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.FKReverseMenu,
		Description: "FK reverse menu (gD)",
		Tag:         "Row",
		Handler:     r.fkReverseHandler,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultJumpBack,
		Description: "Result jump back (<c-o>)",
		Tag:         "Result",
		Handler:     r.jumpBackHandler,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.ResultJumpForward,
		Description: "Result jump forward (<c-i>)",
		Tag:         "Result",
		Handler:     r.jumpForwardHandler,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.PendingDiscardAtCursor,
		Description: "Discard pending edit at cursor (<leader>cu)",
		Tag:         "Edit",
		Handler:     r.pendingDiscardAtCursorHandler,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.PendingDiscardAll,
		Description: "Discard all pending edits (<leader>cU)",
		Tag:         "Edit",
		Handler:     r.pendingDiscardAllHandler,
	})
}

// bwqToastTTL bounds the informational toasts the Z1 stub handlers emit
// for unwired collaborators / no-op outcomes. Matches the surrounding
// short-lived toast TTLs used elsewhere in this controller.
const bwqToastTTL = 2 * time.Second

// fkForwardHandler dispatches `gd`. Resolves the active result tab,
// reads the grid cursor, and hands a CurrentTab adapter (wrapping the
// tab + its grid) to FKForwardHelper.Jump. The helper's guards surface
// as toasts so the chord is always consumed.
func (r *ResultTabsController) fkForwardHandler(_ commands.ExecCtx) error {
	h := r.helpers.FKForward
	if h == nil {
		r.toast("fk forward: helper not wired")
		return nil
	}
	if r.mgr == nil {
		r.toast("fk forward: result tabs not wired")
		return nil
	}
	tab := r.mgr.Active()
	if tab == nil {
		r.toast("fk forward: no active result tab")
		return nil
	}
	grid := tab.Grid()
	if grid == nil {
		r.toast("fk forward: active tab has no grid")
		return nil
	}
	row, col := grid.CursorPosition()
	if err := h.Jump(context.Background(), &fkCurrentTabAdapter{tab: tab}, row, col); err != nil {
		r.toast(err.Error())
	}
	return nil
}

// fkCurrentTabAdapter satisfies helpers.CurrentTab by delegating to a
// *ui.Tab and its grid. Kept in the controllers package (rather than
// on ui.Tab itself) so ui.Tab stays focused on tab lifecycle and the
// FK-specific projection (BaseTable split, row-values copy semantics)
// lives next to its consumer.
type fkCurrentTabAdapter struct {
	tab *ui.Tab
}

func (a *fkCurrentTabAdapter) Slot() int { return a.tab.Slot() }
func (a *fkCurrentTabAdapter) ID() int64 { return a.tab.ID() }

// BaseTable splits the tab's ResultIdentity.BaseTable on the first dot
// into (schema, table). A bare identifier (no dot) returns ("", id).
func (a *fkCurrentTabAdapter) BaseTable() (string, string) {
	_, ri := a.tab.Identity()
	bt := ri.BaseTable
	if before, after, ok := strings.Cut(bt, "."); ok {
		return before, after
	}
	return "", bt
}

func (a *fkCurrentTabAdapter) ColumnNames() []string {
	g := a.tab.Grid()
	if g == nil {
		return nil
	}
	cols := g.Columns()
	out := make([]string, len(cols))
	for i := range cols {
		out[i] = cols[i].Name
	}
	return out
}

// RowValues returns a defensive copy of row's values so the caller
// (which captures the slice on a JumpEntry / FK query) doesn't share
// backing storage with the grid's AppendRows buffer.
func (a *fkCurrentTabAdapter) RowValues(row int) ([]any, bool) {
	g := a.tab.Grid()
	if g == nil {
		return nil, false
	}
	rows := g.AllRows()
	if row < 0 || row >= len(rows) {
		return nil, false
	}
	src := rows[row].Values
	out := make([]any, len(src))
	copy(out, src)
	return out, true
}

// fkReverseHandler dispatches `gD`. Resolves the active tab + cursor
// row, looks up inbound FKs via the FK cache (routed through the active
// SQLSession), assembles ReverseEntry records bound to the row's PK,
// and opens the picker. Each guard surfaces an explanatory toast so
// the chord is always consumed.
func (r *ResultTabsController) fkReverseHandler(_ commands.ExecCtx) error {
	if r.mgr == nil {
		r.toast("fk reverse: result tabs not wired")
		return nil
	}
	tab := r.mgr.Active()
	if tab == nil {
		r.toast("fk reverse: no active result tab")
		return nil
	}
	grid := tab.Grid()
	if grid == nil {
		r.toast("fk reverse: active tab has no grid")
		return nil
	}
	row, col := grid.CursorPosition()
	adapter := &fkCurrentTabAdapter{tab: tab}
	schema, table := adapter.BaseTable()
	if table == "" {
		r.toast("fk reverse: active result has no base table")
		return nil
	}
	rowIdent := grid.RowIdentity()
	if len(rowIdent) == 0 {
		r.toast("fk reverse: active result has no row identity")
		return nil
	}
	values, ok := adapter.RowValues(row)
	if !ok {
		r.toast("fk reverse: row not yet loaded")
		return nil
	}
	pkValues := make([]any, 0, len(rowIdent))
	for _, idx := range rowIdent {
		if idx < 0 || idx >= len(values) {
			r.toast("fk reverse: row identity column out of range")
			return nil
		}
		pkValues = append(pkValues, values[idx])
	}

	if r.helpers.ReverseFKLookup == nil {
		r.toast("fk reverse: reverse lookup not wired")
		return nil
	}
	fks, err := r.helpers.ReverseFKLookup(context.Background(), schema, table)
	if err != nil {
		r.toast(fmt.Sprintf("fk reverse: %s", err.Error()))
		return nil
	}
	if len(fks) == 0 {
		r.toast("fk reverse: no inbound foreign keys")
		return nil
	}

	entries := make([]ReverseEntry, 0, len(fks))
	for _, fk := range fks {
		entries = append(entries, ReverseEntry{
			FK:        fk,
			Reltuples: -1,
			PKValues:  append([]any(nil), pkValues...),
		})
	}

	if r.helpers.OpenFKReversePicker == nil {
		r.toast("fk reverse: picker not wired")
		return nil
	}
	if !r.helpers.OpenFKReversePicker(entries, tab, row, col) {
		r.toast("fk reverse: picker open failed")
	}
	return nil
}

// jumpBackHandler dispatches `<c-o>` — pops the jump list back and
// navigates to (tab, row, col). Stale entries (tab no longer exists)
// surface a "stale" toast and consume the keystroke.
func (r *ResultTabsController) jumpBackHandler(_ commands.ExecCtx) error {
	jl := r.helpers.JumpList
	if jl == nil {
		r.toast("jump list: not wired")
		return nil
	}
	e, ok := jl.Back()
	if !ok {
		r.toast("no jump back")
		return nil
	}
	r.navigateToJumpEntry(e.TabID, e.Row, e.Col, "jump back")
	return nil
}

// jumpForwardHandler dispatches `<c-i>` — the forward counterpart to
// jumpBackHandler.
func (r *ResultTabsController) jumpForwardHandler(_ commands.ExecCtx) error {
	jl := r.helpers.JumpList
	if jl == nil {
		r.toast("jump list: not wired")
		return nil
	}
	e, ok := jl.Forward()
	if !ok {
		r.toast("no jump forward")
		return nil
	}
	r.navigateToJumpEntry(e.TabID, e.Row, e.Col, "jump forward")
	return nil
}

// navigateToJumpEntry switches to tabID and positions the grid cursor
// at (row, col). A nil tab (stale entry) is reported via toast prefixed
// with the chord label so the user knows which direction missed.
func (r *ResultTabsController) navigateToJumpEntry(tabID string, row, col int, label string) {
	if r.mgr == nil {
		r.toast(fmt.Sprintf("%s: result tabs not wired", label))
		return
	}
	tab := r.mgr.SwitchToTabByID(tabID)
	if tab == nil {
		r.toast(fmt.Sprintf("%s: tab no longer exists", label))
		return
	}
	g := tab.Grid()
	if g == nil {
		return
	}
	g.SetCursor(row, col)
}

// pendingDiscardAtCursorHandler dispatches `<leader>cu`. Resolves the
// active tab's per-table PendingEditSet via the registry, snapshots
// the cursor (pk, col), and removes the staged edit if present. A
// miss (no edit at that cell) is silent — pressing cu on a clean cell
// just no-ops.
func (r *ResultTabsController) pendingDiscardAtCursorHandler(_ commands.ExecCtx) error {
	if r.mgr == nil {
		r.toast("pending discard: result tabs not wired")
		return nil
	}
	tab := r.mgr.Active()
	if tab == nil {
		r.toast("pending discard: no active result tab")
		return nil
	}
	grid := tab.Grid()
	if grid == nil {
		r.toast("pending discard: active tab has no grid")
		return nil
	}
	if r.helpers.ActivePendingEditSet == nil {
		r.toast("pending discard: registry not wired")
		return nil
	}
	set := r.helpers.ActivePendingEditSet()
	if set == nil {
		r.toast("pending discard: tab has no editable target")
		return nil
	}
	row, col := grid.CursorPosition()
	cols := grid.Columns()
	if col < 0 || col >= len(cols) {
		r.toast("pending discard: cursor column out of range")
		return nil
	}
	colName := cols[col].Name
	rowIdent := grid.RowIdentity()
	if len(rowIdent) == 0 {
		r.toast("pending discard: no row identity on result")
		return nil
	}
	adapter := &fkCurrentTabAdapter{tab: tab}
	values, ok := adapter.RowValues(row)
	if !ok {
		r.toast("pending discard: row not yet loaded")
		return nil
	}
	pk := make([]any, 0, len(rowIdent))
	for _, idx := range rowIdent {
		if idx < 0 || idx >= len(values) {
			r.toast("pending discard: row identity out of range")
			return nil
		}
		pk = append(pk, values[idx])
	}
	if !set.HasEdit(pk, colName) {
		return nil
	}
	set.Remove(pk, colName)
	r.toast(fmt.Sprintf("discarded pending edit on %s", colName))
	return nil
}

// pendingDiscardAllHandler dispatches `<leader>cU` — clears every staged
// PendingEdit across ALL tables, prompting above the combined-count
// threshold. The helper resolves the registry snapshot itself.
func (r *ResultTabsController) pendingDiscardAllHandler(_ commands.ExecCtx) error {
	h := r.helpers.PendingDiscard
	if h == nil {
		r.toast("pending discard: not wired")
		return nil
	}
	return h.DiscardAll()
}

// yankHandler returns the dispatch handler for `y` (row=false → focused
// cell) / `yy` (row=true → focused row TSV). It resolves the active tab's
// grid, copies the sanitized display value via the grid's ClipboardWriter,
// and maps the typed clipboard errors to specific toasts. An empty grid is a
// silent no-op (no panic) per the AC.
func (r *ResultTabsController) yankHandler(row bool) commands.Handler {
	return func(_ commands.ExecCtx) error {
		if r.mgr == nil {
			return nil
		}
		tab := r.mgr.Active()
		if tab == nil {
			return nil
		}
		g := tab.Grid()
		if g == nil {
			return nil
		}
		var (
			value string
			ok    bool
			err   error
			what  string
		)
		if row {
			value, ok, err = g.YankRow()
			what = "row"
		} else {
			value, ok, err = g.YankCell()
			what = "cell"
		}
		if !ok {
			return nil
		}
		r.flashYank(g, row)
		switch {
		case errors.Is(err, grid.ErrClipboardTooLarge):
			r.toast("clipboard: value too large")
		case errors.Is(err, grid.ErrClipboardUnavailable):
			r.toast("clipboard unavailable")
		case err != nil:
			r.toast("clipboard: " + err.Error())
		default:
			r.toast(fmt.Sprintf("yanked %s (%d bytes)", what, len(value)))
		}
		return nil
	}
}

// flashYank arms the grid's transient post-yank highlight over the just-
// yanked cell (row=false) or row (row=true) and schedules its auto-clear
// after yankFlashTTL — the same TTL and yellow tint the SQL editor uses for
// its on_yank flash. The epoch returned by FlashYank*
// makes a later yank's clear supersede this one. A nil driver (test wiring)
// or empty grid (epoch 0) skips the scheduled clear.
func (r *ResultTabsController) flashYank(g *grid.View, row bool) {
	flash := g.FlashYankCell
	if row {
		flash = g.FlashYankRow
	}
	epoch := flash()
	drv := r.helpers.Driver
	if epoch == 0 || drv == nil {
		return
	}
	time.AfterFunc(yankFlashTTL, func() {
		drv.Update(func() error {
			g.ClearYankFlash(epoch)
			return nil
		})
	})
}

// toast surfaces msg via the helper bag's ToastHelper. No-op when the
// helper is nil — keeps the Z1 stub handlers safe under partial wiring.
func (r *ResultTabsController) toast(msg string) {
	if r.helpers.Toast == nil {
		return
	}
	r.helpers.Toast.Show(msg, bwqToastTTL)
}

// registerMotionHandler wires a no-arg manager call into the command
// registry, nil-checking the manager so unit tests that build the
// controller without a real helper don't crash on dispatch.
func (r *ResultTabsController) registerMotionHandler(reg *commands.Registry, id, desc string, fn func()) {
	_ = reg.Register(&commands.Command{
		ID:          id,
		Description: desc,
		Tag:         "Result",
		Handler: func(_ commands.ExecCtx) error {
			if r.mgr == nil {
				return nil
			}
			fn()
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
