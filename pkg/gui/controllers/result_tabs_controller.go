package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
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

	// HideOverlay opens the <leader>gH hide-cols overlay for the active
	// tab. Persistence is gated on the tab's recorded ResultIdentity
	// (HasRowIdentity). dbsavvy-uv0.6.
	HideOverlay()

	// PromptExport opens the <leader>oe export menu for the active tab.
	// dbsavvy-uv0.9.
	PromptExport()

	// ToggleViewMode flips the active tab's grid between ViewModeGrid
	// and ViewModeExpanded and persists the new value globally via
	// AppState.LastResultViewMode. dbsavvy-uv0.7.
	ToggleViewMode()

	// JumpLastOrReadToEnd dispatches `G`: in expanded mode jumps the
	// cursor to the last loaded record; in grid mode triggers the
	// ReadToEnd drain (with the existing >1M-row warn). dbsavvy-uv0.7.
	JumpLastOrReadToEnd()

	// Result-grid motion delegators. Dispatch is viewMode-aware inside
	// the helper / grid.View. dbsavvy-uv0.7.
	CursorDown()
	CursorUp()
	CursorLeft()
	CursorRight()
	JumpFirst()
	HalfPageDown()
	HalfPageUp()
	WrappedLineDown()
	WrappedLineUp()
	SelectRow()
	SelectBlock()
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
		// dbsavvy-uv0.6: <leader>gH hide-cols overlay.
		{"<leader>gH", commands.ResultHideOverlay, tr.Actions.ResultHideOverlay, types.RESULT_GRID},
		// dbsavvy-uv0.9: <leader>oe opens the export menu.
		{"<leader>oe", commands.ResultExportPrompt, tr.Actions.ResultExportPrompt, types.RESULT_GRID},
		// dbsavvy-uv0.7: expanded view toggle + ]G force-ReadToEnd +
		// result-grid motion bindings (viewMode-aware via the helper).
		{"<leader>gx", commands.ResultViewToggle, tr.Actions.ResultViewToggle, types.RESULT_GRID},
		{"]G", commands.ResultReadToEndForce, tr.Actions.ResultReadToEndForce, types.RESULT_GRID},
		{"j", commands.ResultCursorDown, tr.Actions.ResultCursorDown, types.RESULT_GRID},
		{"k", commands.ResultCursorUp, tr.Actions.ResultCursorUp, types.RESULT_GRID},
		{"h", commands.ResultCursorLeft, tr.Actions.ResultCursorLeft, types.RESULT_GRID},
		{"l", commands.ResultCursorRight, tr.Actions.ResultCursorRight, types.RESULT_GRID},
		{"gg", commands.ResultJumpFirst, tr.Actions.ResultJumpFirst, types.RESULT_GRID},
		{"<c-d>", commands.ResultHalfPageDown, tr.Actions.ResultHalfPageDown, types.RESULT_GRID},
		{"<c-u>", commands.ResultHalfPageUp, tr.Actions.ResultHalfPageUp, types.RESULT_GRID},
		{"J", commands.ResultWrappedLineDown, tr.Actions.ResultWrappedLineDown, types.RESULT_GRID},
		{"K", commands.ResultWrappedLineUp, tr.Actions.ResultWrappedLineUp, types.RESULT_GRID},
		{"V", commands.ResultSelectRow, tr.Actions.ResultSelectRow, types.RESULT_GRID},
		{"<c-v>", commands.ResultSelectBlock, tr.Actions.ResultSelectBlock, types.RESULT_GRID},
	}
	out := make([]*types.ChordBinding, 0, len(specs))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence: seq,
			// INSERT deliberately excluded (dbsavvy-1yb): leader-
			// prefixed bindings registered under (ModeInsert, GLOBAL)
			// make the leader rune (<space>) a chord prefix when the
			// editor is in INSERT mode, buffering it until tlen and
			// producing the "select*" → "select *" reordering bug.
			Mode:        types.ModeNormal,
			Scope:       s.scope,
			ActionID:    s.actionID,
			Description: s.description,
		})
	}
	// dbsavvy-usj: rail-switch chords (1..6 + <tab>) under RESULT_GRID so
	// the user can navigate back out of the result pane. Without these
	// the master editor (scope=RESULT_GRID) dispatches FellThrough for
	// every digit and Tab, leaving the user stranded on the active tab.
	out = append(out, railSwitchBindings(string(types.RESULT_GRID), tr)...)

	// dbsavvy-bwq.Z1: inline-edit + jump-list chords on RESULT_GRID. These
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
			// dbsavvy-uv0.7 AD-14: in expanded mode G means "last record";
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
	// dbsavvy-uv0.6: <leader>gH hide-cols overlay handler.
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
	// dbsavvy-uv0.9: <leader>oe export menu handler.
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
	// dbsavvy-uv0.7: expanded view toggle + motion handlers.
	r.registerMotionHandler(reg, commands.ResultViewToggle, tr.Actions.ResultViewToggle, func() { r.mgr.ToggleViewMode() })
	r.registerMotionHandler(reg, commands.ResultCursorDown, tr.Actions.ResultCursorDown, func() { r.mgr.CursorDown() })
	r.registerMotionHandler(reg, commands.ResultCursorUp, tr.Actions.ResultCursorUp, func() { r.mgr.CursorUp() })
	r.registerMotionHandler(reg, commands.ResultCursorLeft, tr.Actions.ResultCursorLeft, func() { r.mgr.CursorLeft() })
	r.registerMotionHandler(reg, commands.ResultCursorRight, tr.Actions.ResultCursorRight, func() { r.mgr.CursorRight() })
	r.registerMotionHandler(reg, commands.ResultJumpFirst, tr.Actions.ResultJumpFirst, func() { r.mgr.JumpFirst() })
	r.registerMotionHandler(reg, commands.ResultHalfPageDown, tr.Actions.ResultHalfPageDown, func() { r.mgr.HalfPageDown() })
	r.registerMotionHandler(reg, commands.ResultHalfPageUp, tr.Actions.ResultHalfPageUp, func() { r.mgr.HalfPageUp() })
	r.registerMotionHandler(reg, commands.ResultWrappedLineDown, tr.Actions.ResultWrappedLineDown, func() { r.mgr.WrappedLineDown() })
	r.registerMotionHandler(reg, commands.ResultWrappedLineUp, tr.Actions.ResultWrappedLineUp, func() { r.mgr.WrappedLineUp() })
	r.registerMotionHandler(reg, commands.ResultSelectRow, tr.Actions.ResultSelectRow, func() { r.mgr.SelectRow() })
	r.registerMotionHandler(reg, commands.ResultSelectBlock, tr.Actions.ResultSelectBlock, func() { r.mgr.SelectBlock() })

	// dbsavvy-bwq.Z1: inline-edit + jump-list action handlers. Several of
	// these are stub-toasts today because the underlying helper wiring
	// (FKCache, active-grid cursor picker) lands in a follow-up; the
	// chord registration itself is the Z1 deliverable.
	r.registerBwqHandlers(reg)
}

// registerBwqHandlers wires the Z1 (dbsavvy-bwq.23) action handlers for
// the new RESULT_GRID chords. Split out so the main RegisterActions body
// stays readable. Each handler nil-checks the helper / collaborator and
// surfaces a toast describing the missing wire when called pre-wiring
// (the FK forward / reverse handlers exercise that path today).
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

// fkForwardHandler dispatches `gd`. The FK cache is wired post-Z1, so
// the FKForwardHelper.Jump call surfaces a descriptive error today; we
// toast the message and return nil so the chord is consumed.
func (r *ResultTabsController) fkForwardHandler(_ commands.ExecCtx) error {
	h := r.helpers.FKForward
	if h == nil {
		r.toast("fk forward: helper not wired")
		return nil
	}
	// CurrentTab + cursor (row, col) resolution lands in a follow-up;
	// today the helper's nil-cache guard fires before any tab lookup so
	// passing a nil CurrentTab is moot — Jump returns "cache not wired".
	var tab helpers.CurrentTab // nil — Jump nil-checks before deref
	if err := h.Jump(context.Background(), tab, 0, 0); err != nil {
		r.toast(err.Error())
	}
	return nil
}

// fkReverseHandler dispatches `gD`. Real wiring needs an FKCache to
// resolve inbound foreign keys; stub-toast today.
func (r *ResultTabsController) fkReverseHandler(_ commands.ExecCtx) error {
	r.toast("fk reverse: cache wiring pending follow-up")
	return nil
}

// jumpBackHandler dispatches `<c-o>` — pops the jump list. The actual
// "navigate to (tab, row, col)" glue is a follow-up; for Z1 we just call
// Back and surface the outcome via toast.
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
	r.toast(fmt.Sprintf("jumped back to row %d col %d", e.Row, e.Col))
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
	r.toast(fmt.Sprintf("jumped forward to row %d col %d", e.Row, e.Col))
	return nil
}

// pendingDiscardAtCursorHandler dispatches `<leader>cu`. Resolving the
// active grid's (pk, col) from the cursor lands in a follow-up; stub-
// toast today so the binding is observable but no edit is mutated.
func (r *ResultTabsController) pendingDiscardAtCursorHandler(_ commands.ExecCtx) error {
	r.toast("pending discard at cursor: not yet wired")
	return nil
}

// pendingDiscardAllHandler dispatches `<leader>cU` — clears every
// staged PendingEdit, prompting above the threshold via the helper.
func (r *ResultTabsController) pendingDiscardAllHandler(_ commands.ExecCtx) error {
	h := r.helpers.PendingDiscard
	if h == nil {
		r.toast("pending discard: not wired")
		return nil
	}
	return h.DiscardAll()
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
// dbsavvy-uv0.7.
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
