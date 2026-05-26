package orchestrator

import (
	"errors"
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// limitThreshold is the smallest terminal dimension (in either axis)
// that supports a full layout. Below this the layout pass renders the
// LIMIT overlay only.
const limitThreshold = 10

// commandLineViewSetter is the duck-typed surface RunLayout uses to
// plumb the live *gocui.View into CommandLineContext each frame the
// COMMAND_LINE is on the focus stack. The orchestrator can't import
// pkg/gui/context (it already does), but this type-assertion avoids
// adding the SetView method to the wider IBaseContext surface.
type commandLineViewSetter interface {
	SetView(types.View)
}

// Layout satisfies gocui.Manager. The runtime invokes it on every
// frame; we delegate to RunLayout so the same code path is testable
// without a real *gocui.Gui.
func (g *Gui) Layout(ng *gocui.Gui) error {
	w, h := ng.Size()
	return g.RunLayout(w, h)
}

// RunLayout positions every live Context's view inside a terminal of
// the supplied dimensions, dispatching per-Kind. Side rails + extras
// are always tiled. Temporary popups + display contexts are created
// from the focus stack (bottom→top so the top of the stack ends up at
// the top of gocui's z-order); contexts no longer on the stack are
// DeleteView'd so empty popup rectangles don't punch holes through the
// screen under gocui.SupportOverlaps=false.
//
// Below the limit threshold this pass renders only the LIMIT overlay
// (D11 / terminal-too-small AC).
//
// Errors from SetView returning gocui.ErrUnknownView are tolerated:
// gocui surfaces that sentinel as "newly created" on first SetView,
// not as a fatal condition.
func (g *Gui) RunLayout(w, h int) error {
	if g.driver == nil {
		return nil
	}
	if w < limitThreshold || h < limitThreshold {
		return g.renderLimitOverlay(w, h)
	}

	// Clear the tcell back buffer at the start of every frame. gocui's
	// flush() in the lazygit fork does NOT clear (the line is commented
	// out at gocui.gui.go:1146), so DeleteView'd popup cells would
	// otherwise persist on screen. tcell does cell-level diffing in
	// Screen.Show(), so this is cheap and doesn't introduce flicker.
	// Nil-check guards tests where the tcell screen isn't initialised
	// (RecorderGuiDriver doesn't construct a tcell screen).
	if gocui.Screen != nil {
		gocui.Screen.Clear()
	}

	dims := ui.GetWindowDimensions(w, h)

	// Limit overlay is not active at this size; best-effort delete it
	// so it doesn't linger from a previous tiny-terminal frame.
	_ = g.driver.DeleteView(string(types.LIMIT))

	// Tier 1: always-on tiled contexts (side rails + extras). These are
	// created every frame regardless of focus-stack state. View handles
	// returned by SetView are collected into rails so the focus-frame
	// pass below can swap FrameColor per frame (gocui resets FrameColor
	// to ColorDefault on each SetView — view.go:498 — so the swap has to
	// run after SetView, not on focus-change events).
	rails := make(map[string]*gocui.View)
	for _, ctx := range g.registry.Flatten() {
		if ctx == nil {
			continue
		}
		kind := ctx.GetKind()
		if kind != types.SIDE_CONTEXT && kind != types.EXTRAS_CONTEXT {
			continue
		}
		name := ctx.GetViewName()
		if name == "" {
			continue
		}
		d, ok := dims[name]
		if !ok && kind == types.EXTRAS_CONTEXT {
			d, ok = dims["extras"]
		}
		if !ok {
			continue
		}
		v, err := g.driver.SetView(name, d.X0, d.Y0, d.X1, d.Y1, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		if v != nil {
			rails[name] = v
			v.Title = ctx.GetTitle()
		}
		_ = ctx.HandleRender()
	}

	// Tier 1.4 (dbsavvy-9p3): always-on main pane. The MAIN_CONTEXT slot
	// (right-top, dims["main"]) hosts QUERY_EDITOR. Painted every frame
	// regardless of focus-stack membership so the user always sees the
	// editor — focus only governs FrameColor + which view gocui routes
	// keystrokes to. On fresh view creation we seed the view content
	// from the canonical *editor.Buffer (the hydrate-after-Connect
	// adapter swaps the buffer pointer — the next keystroke's
	// syncViewToBuffer will refresh the view if the swap happens after
	// the first paint). The view is added to the `rails` map so it
	// participates in the focus-frame swap below.
	if g.registry != nil && g.registry.QueryEditor != nil {
		qec := g.registry.QueryEditor
		name := qec.GetViewName()
		if d, ok := dims["main"]; ok && name != "" && d.X1 > d.X0 && d.Y1 > d.Y0 {
			v, err := g.driver.SetView(name, d.X0, d.Y0, d.X1, d.Y1, 0)
			freshView := errors.Is(err, gocui.ErrUnknownView)
			if err != nil && !freshView {
				return err
			}
			if v != nil {
				rails[name] = v
				v.Title = qec.GetTitle()
				// Sync the view from the canonical *editor.Buffer every
				// frame, not just on fresh creation. Normal-mode motions
				// (h/j/k/l, w/e/b, gg/G, …) in VimEditorController mutate
				// buf.Cursor without ever touching v, so without this the
				// rendered caret stays pinned to its last Insert-mode
				// position. FocusPoint also pins v.oy so the cursor row
				// stays inside the viewport — typing or motion past the
				// view's bottom would otherwise scroll the cursor off
				// screen with the origin stuck at 0 (mirrors the side-rail
				// scrollSideRailIntoView fix from dbsavvy-f50).
				if buf := qec.Buffer(); buf != nil {
					v.SetContent(buf.String())
					cur := buf.CursorPos()
					v.FocusPoint(cur.Col, cur.Line, true)
				}
			}
			// Attach the VimEditor master editor every frame.
			// gocuiDriver.SetMasterEditor flips v.Editable=true and
			// stashes v.Editor, so the gocui dispatch loop routes keys
			// here (gui.go:1576). SetMasterEditor is idempotent;
			// production and recorder-driver paths converge by name.
			if ed, ok := g.masterEditors[qec.GetKey()]; ok {
				_ = g.driver.SetMasterEditor(name, ed)
			}
			_ = qec.HandleRender()
		}
	}

	// Tier 1.5: result-tab pane (dbsavvy-66p.12). The ResultTabsHelper
	// owns dynamic views named `result_tab_<slot>`; for each open tab
	// we SetView at the "secondary" slot rectangle, the active tab is
	// raised to the top via SetViewOnTop. A nil helper or empty tab
	// list collapses to a no-op so the layout pass works pre-wire.
	if g.resultTabsH != nil {
		if d, ok := dims["secondary"]; ok && d.X1 > d.X0 && d.Y1 > d.Y0 {
			activeTabView := g.resultTabsH.LayoutPaint(g.driver, d.X0, d.Y0, d.X1, d.Y1)
			// Attach the RESULT_GRID master editor to the active tab's
			// view every frame so RESULT_GRID-scoped chords (gt/gT, /, n,
			// G, ]p, [p, <leader>X, <leader>=, <leader>x, <leader>s,
			// <leader>gH) dispatch when focus lands on a result tab.
			// SetMasterEditor is idempotent; the editor was built once
			// in installKeyDispatch (dbsavvy-usj).
			if activeTabView != "" {
				if ed, ok := g.masterEditors[types.RESULT_GRID]; ok {
					_ = g.driver.SetMasterEditor(activeTabView, ed)
				}
				// Register the active tab view in the rails map so
				// applyFocusFrameColors below can paint ActiveBorder
				// when focus is on result_tab_<slot>; without this the
				// highlight visibly leaves QUERY_EDITOR but never lands
				// on the result pane. Inactive tabs are fully occluded
				// by SetViewOnTop so their FrameColor never shows.
				// dbsavvy-usj.
				if v, err := g.driver.ViewByName(activeTabView); err == nil && v != nil {
					rails[activeTabView] = v
				}
			}
		}
	}

	// Focus-frame swap (dbsavvy-tro.1): every Tier-1 rail repaints its
	// FrameColor each frame — focused rail gets theme.ActiveBorder, the
	// rest get theme.InactiveBorder. Popups (Tier-3) are NOT touched and
	// keep whatever FrameColor their own render paths assign. Sourced
	// from the existing focus stack (g.tree.Current); no new state.
	focusedName := ""
	if g.tree != nil {
		if top := g.tree.Current(); top != nil {
			focusedName = top.GetViewName()
		}
	}
	applyFocusFrameColors(rails, focusedName, frameAttr(theme.Current().ActiveBorder), frameAttr(theme.Current().InactiveBorder))

	// Tier 3: focus-stack-driven popups (TEMPORARY_POPUP +
	// DISPLAY_CONTEXT). Walk bottom→top so SetViewOnTop ordering matches
	// the stack ordering. Contexts that aren't on the stack get their
	// view DeleteView'd so empty popup rects don't occlude side panels.
	onStack := map[types.ContextKey]struct{}{}
	if g.tree != nil {
		for _, ctx := range g.tree.Stack() {
			if ctx == nil {
				continue
			}
			kind := ctx.GetKind()
			if kind != types.TEMPORARY_POPUP && kind != types.DISPLAY_CONTEXT {
				continue
			}
			name := ctx.GetViewName()
			if name == "" {
				continue
			}
			r, ok := popupRectFor(ctx.GetKey(), dims, w, h)
			if !ok {
				continue
			}
			view, setViewErr := g.driver.SetView(name, r.X0, r.Y0, r.X1, r.Y1, 0)
			freshView := errors.Is(setViewErr, gocui.ErrUnknownView)
			if setViewErr != nil && !freshView {
				return setViewErr
			}
			// Editable views (COMMAND_LINE, QUERY_EDITOR, PROMPT) get
			// their master Editor reattached to the live view-instance
			// every frame the context is on the focus stack — each Push
			// creates a fresh view (the prior was DeleteView'd here) and
			// SetMasterEditor is idempotent. The call is unconditional
			// because the testfake recorder returns a nil view from
			// SetView while still wanting the editor registered by name
			// (FeedChord dispatches through it); production gocui's
			// SetMasterEditor looks the view up by name internally.
			if ed, ok := g.masterEditors[ctx.GetKey()]; ok {
				_ = g.driver.SetMasterEditor(name, ed)
			}
			// COMMAND_LINE-specific frame / prompt / view-plumb. On fresh
			// creation, prepopulate the TextArea with the leading ":"
			// prompt and plumb the view handle through to the
			// CommandLineContext so command.submit can read v.TextArea.
			if ctx.GetKey() == types.COMMAND_LINE {
				if view != nil {
					view.Frame = false
				}
				if view != nil {
					if freshView && view.TextArea != nil {
						view.TextArea.TypeCharacter(":")
						view.RenderTextArea()
					}
					if cl, ok := ctx.(commandLineViewSetter); ok {
						cl.SetView(view)
					}
				}
				// Overlay the COMMAND_LINE buffer with a styled ':' prompt
				// (dbsavvy-tro.12). The TextArea is the source of truth for
				// the typed line; gocui's RenderTextArea writes the raw
				// content (leading ':' + typed text) into the view buffer.
				// We re-write the cell content via SetContent each frame so
				// the ':' carries PromptFg styling. SetViewCursor below is a
				// separate gocui API that positions the caret independently
				// of the buffer bytes, so the caret tracking continues to
				// work. Under the RecorderGuiDriver SetView returns nil, so
				// fall back to ctx.Buffer() (which already strips the
				// leading ':').
				buffer := ""
				if view != nil && view.TextArea != nil {
					buffer = strings.TrimPrefix(view.TextArea.GetContent(), ":")
				} else if bufHolder, ok := ctx.(interface{ Buffer() string }); ok {
					buffer = bufHolder.Buffer()
				}
				_ = g.driver.SetContent(name, promptStyledLine(theme.Current().PromptFg, buffer))
				// Anchor the visible caret to the TextArea's actual cursor
				// each frame. gocui's DefaultEditor moves TextArea.cursor on
				// Left/Right/Backspace/Delete/Home/End but does not call
				// SetCursor on the view, so we mirror it here. Tests use the
				// RecorderGuiDriver which returns view=nil from SetView, so
				// fall back to the context's Buffer() length (assumes the
				// caret is at end-of-buffer in tests — adequate for the
				// recorder, which has no real TextArea cursor). Bug
				// dbsavvy-tro.2 / dbsavvy-go1.
				cursorX, cursorY := 1, 0
				if view != nil && view.TextArea != nil {
					cursorX, cursorY = view.TextArea.GetCursorXY()
				} else if bufHolder, ok := ctx.(interface{ Buffer() string }); ok {
					cursorX = 1 + len(bufHolder.Buffer())
				}
				_ = g.driver.SetViewCursor(name, cursorX, cursorY)
			}
			// PROMPT view-plumb + caret anchor. The PROMPT view is
			// editable post-dbsavvy-fq9 — keystrokes flow through the
			// master Editor's Passthrough branch into
			// gocui.DefaultEditor which writes into v.TextArea. On fresh
			// view creation we seed the TextArea with the helper's
			// initial value (the user-visible re-prompt path uses the
			// last typed input as the new initial). We also publish the
			// view's inner width to PromptContext so its label wrapper
			// fits any validator error onto multiple lines instead of
			// truncating at the popup right edge (dbsavvy-8p5).
			if ctx.GetKey() == types.PROMPT {
				if cl, ok := ctx.(interface{ SetView(types.View) }); ok {
					cl.SetView(view)
				}
				if freshView && view != nil && view.TextArea != nil {
					if g.promptHelp != nil {
						initial := g.promptHelp.Initial()
						if initial != "" {
							for _, r := range initial {
								view.TextArea.TypeCharacter(string(r))
							}
							view.RenderTextArea()
						}
					}
				}
				if wsetter, ok := ctx.(interface{ SetLabelWrapWidth(int) }); ok && view != nil {
					// view.InnerWidth() returns the writable column
					// count (Width-2). Fall back to a sensible default
					// when the view is nil (recorder driver path).
					wsetter.SetLabelWrapWidth(view.InnerWidth())
				}
				if cur, ok := ctx.(interface{ CursorXY() (int, int, bool) }); ok {
					if x, y, active := cur.CursorXY(); active {
						_ = g.driver.SetViewCursor(name, x, y)
					}
				}
			}
			_ = ctx.HandleRender()
			_, _ = g.driver.SetViewOnTop(name)
			onStack[ctx.GetKey()] = struct{}{}
		}
	}

	// Tear down any TEMPORARY_POPUP / DISPLAY_CONTEXT views that aren't
	// currently on the focus stack. WHICH_KEY and LIMIT are managed by
	// their dedicated overlay paths (notifier-driven / tiny-terminal
	// branch respectively) and excluded here.
	for _, ctx := range g.registry.Flatten() {
		if ctx == nil {
			continue
		}
		kind := ctx.GetKind()
		if kind != types.TEMPORARY_POPUP && kind != types.DISPLAY_CONTEXT {
			continue
		}
		key := ctx.GetKey()
		if key == types.WHICH_KEY || key == types.LIMIT {
			continue
		}
		if _, ok := onStack[key]; ok {
			continue
		}
		name := ctx.GetViewName()
		if name == "" {
			continue
		}
		_ = g.driver.DeleteView(name)
	}

	// Tier 4a: always-on status bar (dbsavvy-tro.3). The boxlayout
	// reserves a 1-row "status" slot at the canvas bottom; we materialise
	// a borderless view there each frame and hand it to RenderStatusLine,
	// which multiplexes the toast helper's Current() over the default
	// status line for the TTL window. SetView returning ErrUnknownView
	// is the gocui "created on first call" sentinel — same idiom Tier 1
	// uses above. A nil view (test recorder path) is tolerated; the
	// renderer writes to the view via SetContent, which the recorder
	// driver routes by name regardless of the *View handle.
	//
	// Rect expansion (dbsavvy-8tj): the lazygit gocui fork computes a
	// view's writable InnerHeight as Height-2 regardless of Frame
	// (pkg/gocui/view.go:527-547) and writes cells at screen position
	// (x0+x+1, y0+y+1). A naked Size:1 slot from boxlayout yields
	// Y0==Y1 → InnerHeight=0, and the off-by-one cell offset places
	// content at row H (off-screen) for a bottom strip. We follow the
	// same trick commandLineRect uses for COMMAND_LINE: extend the
	// rectangle by -1/+1 in Y so gocui sees Height=3, InnerHeight=1,
	// with the single visible row landing exactly on the boxlayout
	// slot's reserved screen row (d.Y0). The "virtual" extra rows are
	// never written to — gocui clamps cell writes to inner bounds.
	if d, ok := dims[AppStatusViewName]; ok && d.X1 > d.X0 && d.Y1 >= d.Y0 {
		view, err := g.driver.SetView(AppStatusViewName, d.X0, d.Y0-1, d.X1, d.Y1+1, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		// Borderless 1-row strip — same shape as COMMAND_LINE. Without
		// Frame=false gocui would draw a border box around the cell.
		if view != nil {
			view.Frame = false
		}
		// Resolve the live *models.Connection by joining the activeConnID
		// state with the Deps.ConnectionsProvider (the same source the
		// Connections side rail walks). A missing provider or empty ID
		// collapses to nil — BuildStatusLine renders the no-conn slot.
		activeConn := func() *models.Connection {
			if g.activeConnID == "" || g.deps.ConnectionsProvider == nil {
				return nil
			}
			for _, c := range g.deps.ConnectionsProvider() {
				if c.Name == g.activeConnID {
					cp := c
					return &cp
				}
			}
			return nil
		}
		var tr *i18n.TranslationSet
		if g.deps.Common != nil {
			tr = g.deps.Common.Tr
		}
		RenderStatusLine(StatusRenderDeps{
			Driver:     g.driver,
			Tree:       g.tree,
			KbRuntime:  g.kbRuntime,
			ActiveConn: activeConn,
			Tr:         tr,
			Toast:      g.toastHelp,
			BusyCount:  g.BusyCount,
		})
	}

	// Tier 4: shy overlays driven by notifier visibility (not by stack
	// membership). LIMIT is handled in the early-return tiny-terminal
	// branch and never needs touching here.
	if err := g.renderWhichKeyOverlay(w, h, dims); err != nil {
		return err
	}

	// Focus the gocui current-view on the top of the focus stack. This
	// replaces the swap-hook indirection that previously queued a
	// SetCurrentView via driver.Update and fought the SetViewOnTop pass.
	if g.tree != nil {
		if top := g.tree.Current(); top != nil {
			if vn := top.GetViewName(); vn != "" {
				_, _ = g.driver.SetCurrentView(vn)
			}
			// Caret toggle for tiled contexts (SIDE/MAIN/EXTRAS): gocui's
			// flush only renders the terminal caret when g.Cursor is true,
			// so even though Tier 1.4 / syncViewToBuffer position the view
			// cursor every frame, the user sees no caret unless we enable
			// it here. QUERY_EDITOR is the only tiled editable context;
			// every other tile (side rails / messages) must keep the caret
			// off so the cursor doesn't bleed onto rail rows. PROMPT and
			// COMMAND_LINE are TEMPORARY_POPUPs and own their own caret
			// state via PromptHelper / CommandLineCommandDeps — we leave
			// their kinds untouched so those togglers stay authoritative.
			switch top.GetKind() {
			case types.SIDE_CONTEXT, types.MAIN_CONTEXT, types.EXTRAS_CONTEXT:
				enabled := top.GetKey() == types.QUERY_EDITOR
				g.driver.SetCaretEnabled(enabled)
				// Force a steady (non-blinking) block cursor while the
				// QUERY_EDITOR has focus. The terminal default is a
				// blinking bar/block on most emulators, which is
				// distracting in the editor. tcell deduplicates the
				// escape sequence internally — safe to call every frame.
				// Per-mode shapes (normal/visual/insert distinction) is
				// future work; for now any focused editor frame stays
				// steady-block.
				if enabled && gocui.Screen != nil {
					gocui.Screen.SetCursorStyle(tcell.CursorStyleSteadyBlock)
				}
			}
		}
	}

	return nil
}

// popupRectFor maps a popup ContextKey to its SetView rectangle. The
// rectangle is computed against dims["popup-overlay"] (the centred
// inner canvas inside the side rails / extras).
func popupRectFor(key types.ContextKey, dims map[string]ui.Dimensions, w, h int) (rect, bool) {
	switch key {
	case types.PROMPT:
		// PROMPT carries validator error messages on its second body
		// line (e.g. "DSN: Connection string\nDSN contains an inline
		// password; please remove it"). The 50% × 50% generic popup
		// rect truncates these at the right edge (dbsavvy-8p5). Widen
		// to 80% so the wrapped body fits in typical terminal widths;
		// PromptContext word-wraps the label to popup width as a
		// belt-and-braces guard for shorter terminals.
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		return centeredRect(canvas, 0.8, 0.5), true
	case types.MENU, types.CONFIRMATION, types.SELECTION, types.SUGGESTIONS, types.HIDE_OVERLAY, types.EXPORT_MENU:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		return centeredRect(canvas, 0.5, 0.5), true
	case types.TABLE_INSPECT:
		// TABLE_INSPECT replaces the columns/indexes side rails with a
		// tabbed popup (epic dbsavvy-3vf). Larger than the generic
		// 50% × 50% rect so column/index tables have room to breathe.
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		return centeredRect(canvas, 0.6, 0.6), true
	case types.COMMAND_LINE:
		r := commandLineRect(dims)
		if r == (rect{}) {
			return rect{}, false
		}
		return r, true
	case types.CHEATSHEET:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			canvas = ui.Dimensions{X0: 0, Y0: 0, X1: w - 1, Y1: h - 1}
		}
		return centeredRectMaxSize(canvas, cheatsheetMaxCols, cheatsheetMaxRows), true
	case types.CELL_EDITOR:
		// CELL_EDITOR is a small single-line editing popup over the
		// result grid (dbsavvy-tzi.1). Keep it height-bounded — a 50%
		// box would occlude the grid the user is editing. Width is ~60%
		// of the canvas; cellEditorMaxRows caps it at ~3 content rows
		// (frame top+bottom borders consume 2).
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		cw := canvas.X1 - canvas.X0
		maxCols := cw * 3 / 5
		return centeredRectMaxSize(canvas, maxCols, cellEditorMaxRows), true
	default:
		return rect{}, false
	}
}

// renderLimitOverlay sizes a single LIMIT view to the full canvas and
// invokes the LimitContext's HandleRender to fill in the message.
func (g *Gui) renderLimitOverlay(w, h int) error {
	if w < 1 || h < 1 {
		return nil
	}
	if _, err := g.driver.SetView(string(types.LIMIT), 0, 0, w-1, h-1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		return err
	}
	if g.registry != nil && g.registry.Limit != nil {
		_ = g.registry.Limit.HandleRender()
	}
	// Best-effort cleanup of any overlay views that may have been created
	// in a previous normal-size frame; only LIMIT participates in this
	// branch's render pass.
	_ = g.driver.DeleteView(string(types.WHICH_KEY))
	_, _ = g.driver.SetViewOnTop(string(types.LIMIT))
	return nil
}

// whichKeyMaxRows / whichKeyMaxCols cap the popup rectangle. The
// renderer truncates per-row content separately; the dims here only
// bound the SetView rect so the popup doesn't dominate the screen.
const (
	whichKeyMaxRows = 12
	whichKeyMaxCols = 40
)

// cheatsheetMaxRows / cheatsheetMaxCols cap the cheatsheet popup
// rectangle. Larger than which-key because the cheatsheet enumerates
// every binding for the current (Mode, Scope) plus the Global tier.
// Clamped to the canvas at render time so small terminals don't
// overflow.
const (
	cheatsheetMaxRows = 30
	cheatsheetMaxCols = 60
)

// cellEditorMaxRows caps the cell-edit popup height. The frame's top and
// bottom borders consume 2 rows, leaving ~3 content rows for the single
// "> <buffer>" line. Height-bounded by design so the popup doesn't
// occlude the result grid being edited (dbsavvy-tzi.1).
const cellEditorMaxRows = 5

// renderWhichKeyOverlay positions the WHICH_KEY view in the bottom
// right corner of popup-overlay and invokes WhichKeyContext.HandleRender
// — but only when the notifier reports visible. On invisibility the
// view is best-effort deleted so it doesn't linger from a prior frame.
//
// Wired conservatively: a missing registry, missing WhichKey context,
// or unwired notifier collapses to a no-op (the concrete WhichKey
// wiring lands in dlp.8c).
func (g *Gui) renderWhichKeyOverlay(w, h int, dims map[string]ui.Dimensions) error {
	if g.registry == nil || g.registry.WhichKey == nil {
		return nil
	}
	notifier := g.registry.WhichKey.Notifier()
	if notifier == nil || !notifier.Visible() {
		_ = g.driver.DeleteView(string(types.WHICH_KEY))
		return nil
	}
	// Empty-rows policy (dbsavvy-tro.4): if the wired resolver yields no
	// children for the current (scope, prefix), hide the notifier and
	// delete the view so we don't paint an empty popup rect onscreen. A
	// chord prefix with no continuations is "dead air" — the user would
	// otherwise see an empty box hover for the notifier's TTL.
	scope, prefix, _ := notifier.Snapshot()
	if !g.registry.WhichKey.HasRows(scope, prefix) {
		notifier.Hide()
		_ = g.driver.DeleteView(string(types.WHICH_KEY))
		return nil
	}
	canvas, ok := dims["popup-overlay"]
	if !ok {
		canvas = ui.Dimensions{X0: 0, Y0: 0, X1: w - 1, Y1: h - 1}
	}
	r := bottomRightRect(canvas, whichKeyMaxCols, whichKeyMaxRows)
	if _, err := g.driver.SetView(string(types.WHICH_KEY), r.X0, r.Y0, r.X1, r.Y1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		return err
	}
	_ = g.registry.WhichKey.HandleRender()
	_, _ = g.driver.SetViewOnTop(string(types.WHICH_KEY))
	return nil
}

// centeredRectMaxSize returns a rectangle no larger than maxCols ×
// maxRows centred inside canvas. When the canvas is smaller than the
// requested max, the rect fills the canvas. Ensures min dimensions of
// 1×1 so gocui SetView is happy on tiny terminals.
func centeredRectMaxSize(canvas ui.Dimensions, maxCols, maxRows int) rect {
	cw := canvas.X1 - canvas.X0
	ch := canvas.Y1 - canvas.Y0
	if cw < 2 || ch < 2 {
		return rect{X0: canvas.X0, Y0: canvas.Y0, X1: canvas.X1, Y1: canvas.Y1}
	}
	w := maxCols
	if w > cw {
		w = cw
	}
	if w < 1 {
		w = 1
	}
	h := maxRows
	if h > ch {
		h = ch
	}
	if h < 1 {
		h = 1
	}
	x0 := canvas.X0 + (cw-w)/2
	y0 := canvas.Y0 + (ch-h)/2
	return rect{X0: x0, Y0: y0, X1: x0 + w, Y1: y0 + h}
}

// bottomRightRect returns a maxCols × maxRows rectangle anchored to the
// bottom-right of canvas, clamped to the canvas extent.
func bottomRightRect(canvas ui.Dimensions, maxCols, maxRows int) rect {
	cw := canvas.X1 - canvas.X0
	ch := canvas.Y1 - canvas.Y0
	if cw < 2 || ch < 2 {
		return rect{X0: canvas.X0, Y0: canvas.Y0, X1: canvas.X1, Y1: canvas.Y1}
	}
	w := maxCols
	if w > cw {
		w = cw
	}
	if w < 1 {
		w = 1
	}
	h := maxRows
	if h > ch {
		h = ch
	}
	if h < 1 {
		h = 1
	}
	return rect{
		X0: canvas.X1 - w,
		Y0: canvas.Y1 - h,
		X1: canvas.X1,
		Y1: canvas.Y1,
	}
}

// rect is the (X0, Y0, X1, Y1) tuple Layout passes to SetView.
type rect struct {
	X0, Y0, X1, Y1 int
}

// commandLineRect returns a full-width, single-line strip anchored to
// the bottom of the popup-overlay canvas. Used by the COMMAND_LINE
// TEMPORARY_POPUP — colon ex-commands always render at the very bottom
// of the screen, vim-style.
func commandLineRect(dims map[string]ui.Dimensions) rect {
	canvas, ok := dims["popup-overlay"]
	if !ok {
		return rect{}
	}
	if canvas.Y1-canvas.Y0 < 1 || canvas.X1-canvas.X0 < 1 {
		return rect{X0: canvas.X0, Y0: canvas.Y0, X1: canvas.X1, Y1: canvas.Y1}
	}
	return rect{
		X0: canvas.X0,
		Y0: canvas.Y1 - 1,
		X1: canvas.X1,
		Y1: canvas.Y1 + 1,
	}
}

// applyFocusFrameColors walks the supplied rail views and writes
// FrameColor for each: the view whose name equals focusedName receives
// active, all other Frame=true views receive inactive. Views with
// Frame=false (e.g. COMMAND_LINE) and nil entries are skipped. Caller
// is responsible for excluding popup-Kind views from the input map —
// only top-level rails belong in here.
func applyFocusFrameColors(rails map[string]*gocui.View, focusedName string, active, inactive gocui.Attribute) {
	for name, v := range rails {
		if v == nil || !v.Frame {
			continue
		}
		if name == focusedName {
			v.FrameColor = active
		} else {
			v.FrameColor = inactive
		}
	}
}

// promptStyledLine builds the COMMAND_LINE cell content: a ':' prefix
// wrapped in the PromptFg ANSI SGR escape, followed by the typed buffer
// rendered with default styling. The ANSI reset between prompt and
// buffer ensures the user-typed text isn't accidentally restyled.
// gocui's escape interpreter parses the inline SGR and lifts it to
// per-cell Attribute values; the recorder driver stores the raw bytes
// so tests can assert on the wrapper directly (dbsavvy-tro.12).
//
// A nil style or unrecognised colour collapses to a default-fg ':' —
// callers still get a visible prompt, just without the brighten.
func promptStyledLine(style *theme.Style, buffer string) string {
	prefix := ansiSGRForStyle(style)
	if prefix == "" {
		return ":" + buffer
	}
	return prefix + ":" + ansiResetSGR + buffer
}

// ansiSGRForStyle returns the ANSI SGR escape for the foreground colour
// described by style. Recognises the standard 8 colour names; everything
// else (hex, unknown name, nil) returns "" so callers can fall back to
// the default foreground.
func ansiSGRForStyle(s *theme.Style) string {
	if s == nil {
		return ""
	}
	switch strings.ToLower(s.Fg) {
	case "black":
		return "\x1b[30m"
	case "red":
		return "\x1b[31m"
	case "green":
		return "\x1b[32m"
	case "yellow":
		return "\x1b[33m"
	case "blue":
		return "\x1b[34m"
	case "magenta":
		return "\x1b[35m"
	case "cyan":
		return "\x1b[36m"
	case "white":
		return "\x1b[37m"
	default:
		return ""
	}
}

// frameAttr translates a theme.Style colour-name into the gocui.Attribute
// the runtime stores in v.FrameColor. Nil styles and empty Fg fall back
// to gocui.ColorDefault so the helper never injects an invalid colour
// into a view. gocui.GetColor accepts W3C names and #RRGGBB hex.
func frameAttr(s *theme.Style) gocui.Attribute {
	if s == nil || s.Fg == "" {
		return gocui.ColorDefault
	}
	return gocui.GetColor(s.Fg)
}

// centeredRect returns the subrect occupying (frac w x frac h) of the
// canvas, centred. Minimum dimensions of 1×1 keep gocui happy on small
// but above-threshold terminals.
func centeredRect(canvas ui.Dimensions, fracW, fracH float64) rect {
	w := canvas.X1 - canvas.X0
	h := canvas.Y1 - canvas.Y0
	if w < 2 || h < 2 {
		return rect{X0: canvas.X0, Y0: canvas.Y0, X1: canvas.X1, Y1: canvas.Y1}
	}
	pw := int(float64(w) * fracW)
	ph := int(float64(h) * fracH)
	if pw < 1 {
		pw = 1
	}
	if ph < 1 {
		ph = 1
	}
	x0 := canvas.X0 + (w-pw)/2
	y0 := canvas.Y0 + (h-ph)/2
	return rect{X0: x0, Y0: y0, X1: x0 + pw, Y1: y0 + ph}
}
