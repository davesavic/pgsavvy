package orchestrator

import (
	"errors"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/mattn/go-runewidth"

	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/editor/highlight"
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
	// CONNECTION_MANAGER modal (epic dbsavvy-ig4): while it is top of the
	// focus stack it renders a centered bordered box over a blank
	// background. Suppress the Tier-1 side-rails + extras loop entirely for
	// the frame so nothing paints behind the modal.
	rails := make(map[string]*gocui.View)
	modalTop := g.modalIsTopMain()
	for _, ctx := range g.registry.Flatten() {
		if ctx == nil {
			continue
		}
		kind := ctx.GetKind()
		if kind != types.SIDE_CONTEXT && kind != types.EXTRAS_CONTEXT {
			continue
		}
		if modalTop {
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
	if modalTop {
		g.layoutConnectionManagerMain(dims, rails)
	} else if g.registry != nil && g.registry.QueryEditor != nil {
		// The CONNECTION_MANAGER modal is a MAIN_CONTEXT, so the Tier-3
		// cleanup loop below (TEMPORARY_POPUP / DISPLAY_CONTEXT only) never
		// tears its view down. Once the modal leaves the focus stack its
		// centered box still lives in g.views and gocui's flush() keeps
		// drawing it every frame — leaving border artifacts over the editor /
		// results / status region. DeleteView removes it from g.views so it
		// stops being drawn. Idempotent: ErrUnknownView (already gone) is
		// the expected steady-state and is ignored (dbsavvy-1du).
		if g.registry.ConnectionManager != nil {
			if name := g.registry.ConnectionManager.GetViewName(); name != "" {
				_ = g.driver.DeleteView(name)
			}
		}
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
					content := highlight.Highlight(buf.String())
					if sel := buf.SelectionSnapshot(); sel != nil {
						content = editor.ApplySelectionOverlay(content, *sel)
					}
					if flash := buf.YankFlashSnapshot(); flash != nil {
						content = editor.ApplyYankFlashOverlay(content, *flash)
					}
					v.SetContent(content)
					cur := buf.CursorPos()
					v.FocusPoint(cur.Col, cur.Line, true)
					// FocusPoint pins only the vertical origin; without
					// this the editor never scrolls horizontally, so
					// lines wider than the pane clip past the right
					// border and the caret vanishes (dbsavvy-jdyt).
					scrollEditorColumnIntoView(v, cur.Col)
				}
			}
			// Attach the VimEditor master editor every frame.
			// gocuiDriver.SetMasterEditor flips v.Editable=true and
			// stashes v.Editor, so the gocui dispatch loop routes keys
			// here (gui.go:1576). SetMasterEditor is idempotent;
			// production and recorder-driver paths converge by name.
			if ed, ok := g.keybindingSystem.masterEditors[qec.GetKey()]; ok {
				_ = g.driver.SetMasterEditor(name, ed)
			}
			_ = qec.HandleRender()
			_, _ = g.driver.SetViewOnTop(name)
		}
	}

	// Tier 1.5: result-tab pane. Suppressed when the CONNECTION_MANAGER
	// modal is top so nothing paints behind it (matches Tier 1 suppression).
	activeTabView := ""
	if !modalTop {
		if d, ok := dims["secondary"]; ok && d.X1 > d.X0 && d.Y1 > d.Y0 {
			// Baseline empty-state view — always present behind any tab views.
			emptyView, emptyErr := g.driver.SetView(ResultEmptyViewName, d.X0, d.Y0, d.X1, d.Y1, 0)
			if emptyErr != nil && !errors.Is(emptyErr, gocui.ErrUnknownView) {
				return emptyErr
			}
			if emptyView != nil {
				emptyView.Title = " Results "
				rails[ResultEmptyViewName] = emptyView
			}

			if g.resultTabsH != nil {
				contentY0 := d.Y0
				if g.resultTabsH.Count() > 0 && d.Y1-d.Y0 >= 3 {
					bar, err := g.driver.SetView(ResultTabBarViewName, d.X0, d.Y0-1, d.X1, d.Y0+1, 0)
					if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
						return err
					}
					if bar != nil {
						bar.Frame = false
					}
					_ = g.driver.SetContent(ResultTabBarViewName, g.resultTabsH.RenderTabBar(d.X1-d.X0))
					contentY0 = d.Y0 + 1
				} else {
					_ = g.driver.DeleteView(ResultTabBarViewName)
				}

				activeTabView = g.resultTabsH.LayoutPaint(g.driver, d.X0, contentY0, d.X1, d.Y1)
				if activeTabView != "" {
					// Pick the editor by the active tab's context key: plan tabs
					// dispatch under PLAN, grid tabs under RESULT_GRID (dbsavvy-s7gn).
					editorKey := types.RESULT_GRID
					if ac := g.resultTabsH.ActiveContext(); ac != nil {
						editorKey = ac.GetKey()
					}
					if ed, ok := g.keybindingSystem.masterEditors[editorKey]; ok {
						_ = g.driver.SetMasterEditor(activeTabView, ed)
					}
					if v, err := g.driver.ViewByName(activeTabView); err == nil && v != nil {
						rails[activeTabView] = v
					}
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
			focusedName = resolveFocusedRailName(top.GetViewName(), activeTabView)
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
			r, ok := g.popupRect(ctx, dims, w, h)
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
			if ed, ok := g.keybindingSystem.masterEditors[ctx.GetKey()]; ok {
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
				// Pin the horizontal origin to 0 (dbsavvy-lcxe). The popup
				// is sized (popupRect) to fit the label and "> "+buffer, so
				// the view never needs to scroll horizontally. gocui's
				// RenderTextArea (run by DefaultEditor on each keystroke)
				// only *reduces* the origin when the cursor moves left of
				// it, so after editing a long path down to a short one a
				// stale origin > 0 would persist and clip the label and the
				// "> " prefix. Resetting it each frame keeps content
				// left-anchored; CursorXY below sets the caret in absolute
				// (origin-0) coordinates to match.
				if view != nil {
					view.SetOrigin(0, view.OriginY())
				}
				if cur, ok := ctx.(interface{ CursorXY() (int, int, bool) }); ok {
					if x, y, active := cur.CursorXY(); active {
						_ = g.driver.SetViewCursor(name, x, y)
					}
				}
				// The prompt is always the focused modal while on top, so
				// paint its border (popups are skipped by the Tier-1
				// applyFocusFrameColors pass) and surface its title (set for
				// the masked SSH credential prompt — see
				// PromptContext.GetTitle). The free-form `<c-e>` expression
				// prompt uses WarnBorder (dbsavvy-bwq.23) to flag that its
				// input is injected verbatim; every other prompt keeps the
				// active-border colour. gocui resets FrameColor on each
				// SetView, so this must run after SetView, every frame.
				// Mirrors the CONFIRMATION path below.
				if view != nil {
					view.Title = ctx.GetTitle()
					label := ""
					if g.promptHelp != nil {
						label = g.promptHelp.Label()
					}
					view.FrameColor = frameAttr(promptBorderStyle(label))
				}
			}
			// CELL_EDITOR view-plumb + seed + caret anchor (dbsavvy-tzi.2).
			// Like PROMPT, CELL_EDITOR is an editable popup whose keystrokes
			// flow through the master Editor's Passthrough into
			// gocui.DefaultEditor (TextArea). Plumb the live view so
			// Buffer()/ReadAndClearBuffer() read the TextArea, seed the fresh
			// view's TextArea once from Initial() (the single seed source —
			// the Open()-time seed was removed), and anchor the caret after
			// the "> " body prefix that HandleRender writes.
			if ctx.GetKey() == types.CELL_EDITOR {
				if cl, ok := ctx.(interface{ SetView(types.View) }); ok {
					cl.SetView(view)
				}
				if freshView && view != nil && view.TextArea != nil {
					if cur, ok := ctx.(interface{ Initial() string }); ok {
						if initial := cur.Initial(); initial != "" {
							for _, r := range initial {
								view.TextArea.TypeCharacter(string(r))
							}
							view.RenderTextArea()
						}
					}
				}
				// Pin the horizontal origin to 0 each frame (mirrors the
				// PROMPT path above, dbsavvy-lcxe). HandleRender repaints the
				// "> "+buffer line via SetContent in absolute (origin-0)
				// coordinates and scrolls the buffer itself when it outgrows
				// the box (CellEditorContext.hScroll), but the master Editor's
				// gocui.DefaultEditor calls RenderTextArea on every keystroke,
				// which scrolls OriginX right. A stale OriginX > 0 would then
				// double-scroll the manually-windowed content and push the
				// caret off the box. Resetting it keeps the line left-anchored
				// so the windowed render and CursorXY below line up.
				if view != nil {
					view.SetOrigin(0, view.OriginY())
				}
				// Anchor the caret via the context's CursorXY (mirrors PROMPT,
				// lines above): it returns the prefix-offset column inside the
				// horizontally-scrolled window, so the caret stays visible even
				// when the value is wider than the box. Under the recorder
				// driver SetView returns nil → CursorXY still reports the
				// logical position and SetViewCursor is a no-op on the fake.
				if cur, ok := ctx.(interface{ CursorXY() (int, int, bool) }); ok {
					if cx, cy, active := cur.CursorXY(); active {
						_ = g.driver.SetViewCursor(name, cx, cy)
					}
				}
				// CELL_EDITOR is a focused editable popup like PROMPT but set
				// no FrameColor previously, leaving it on gocui's per-SetView
				// ColorDefault reset (dbsavvy-uly7.14). Paint the active-border
				// colour to match PROMPT/CONFIRMATION/TABLE_INSPECT. gocui
				// resets FrameColor on each SetView, so this runs after
				// SetView, every frame.
				if view != nil {
					view.FrameColor = frameAttr(theme.Current().ActiveBorder)
				}
			}
			// SEARCH_LINE view-plumb + width + caret (dbsavvy-2ttm). Like
			// COMMAND_LINE, the search input is a borderless editable strip
			// whose TextArea holds the raw query; HandleRender writes the "/"
			// prefix + right-aligned match count via SetContent. The caret is
			// offset by len("/")=1 so it tracks the TextArea cursor past the
			// rendered prefix.
			if ctx.GetKey() == types.SEARCH_LINE {
				if view != nil {
					view.Frame = false
				}
				if cl, ok := ctx.(interface{ SetView(types.View) }); ok {
					cl.SetView(view)
				}
				if wsetter, ok := ctx.(interface{ SetWidth(int) }); ok && view != nil {
					wsetter.SetWidth(view.InnerWidth())
				}
				if view != nil && view.TextArea != nil {
					cx, cy := view.TextArea.GetCursorXY()
					_ = g.driver.SetViewCursor(name, cx+1, cy)
				}
			}
			// CONFIRMATION styling (dbsavvy-u6p7): the popup is always the
			// focused modal while it's on top, so paint its border with the
			// active-border colour (popups are skipped by the Tier-1
			// applyFocusFrameColors pass). gocui resets FrameColor on each
			// SetView, so this must run after SetView, every frame. The
			// dynamic title moves to the frame heading (GetTitle override),
			// and Wrap reflows a long SQL statement to the box width.
			if ctx.GetKey() == types.CONFIRMATION && view != nil {
				view.Title = ctx.GetTitle()
				view.Wrap = true
				view.FrameColor = frameAttr(theme.Current().ActiveBorder)
			}
			// TABLE_INSPECT styling (dbsavvy-2048): the columns/indexes
			// inspect popup is the focused modal while on top, so give it
			// the same focused-modal treatment as CONFIRMATION/PROMPT —
			// surface its "Table inspect" frame title and paint the active
			// border (popups are skipped by the Tier-1
			// applyFocusFrameColors pass; gocui only resets FrameColor when
			// the view is freshly created, so this runs after SetView every
			// frame). No Wrap: the tabbed body is pre-formatted and would
			// be mangled by reflow.
			if ctx.GetKey() == types.TABLE_INSPECT && view != nil {
				view.Title = ctx.GetTitle()
				view.FrameColor = frameAttr(theme.Current().ActiveBorder)
			}
			// HISTORY styling (dbsavvy-o9k0): the query-history browse popup
			// is the focused modal while on top, so give it the same
			// focused-modal treatment as TABLE_INSPECT — surface its
			// "History" frame title and paint the active border (popups are
			// skipped by the Tier-1 applyFocusFrameColors pass; gocui only
			// resets FrameColor when the view is freshly created, so this
			// runs after SetView every frame). No Wrap: the body is
			// pre-formatted with a "> " cursor marker that reflow would
			// mangle.
			if ctx.GetKey() == types.HISTORY && view != nil {
				view.Title = ctx.GetTitle()
				view.FrameColor = frameAttr(theme.Current().ActiveBorder)
			}
			// CHEATSHEET styling + scroll (dbsavvy-quyg): the keybinding
			// cheatsheet is the focused modal while on top, so give it the
			// same "Keybindings" frame title + active border as HISTORY
			// (popups are skipped by the Tier-1 applyFocusFrameColors pass;
			// gocui resets FrameColor on SetView, so this runs every frame).
			// The scroll origin is applied AFTER HandleRender below so
			// view.LinesHeight reflects the freshly written body.
			if ctx.GetKey() == types.CHEATSHEET && view != nil {
				view.Title = ctx.GetTitle()
				view.FrameColor = frameAttr(theme.Current().ActiveBorder)
			}
			_ = ctx.HandleRender()
			if ctx.GetKey() == types.CHEATSHEET && view != nil {
				applyCheatsheetScroll(view, ctx)
			}
			if ctx.GetKey() == types.TABLE_INSPECT && view != nil {
				applyTableInspectScroll(view, ctx)
			}
			_, _ = g.driver.SetViewOnTop(name)
			onStack[ctx.GetKey()] = struct{}{}
		}
	}

	// Tear down any TEMPORARY_POPUP / DISPLAY_CONTEXT views that aren't
	// currently on the focus stack. WHICH_KEY, LIMIT and SUGGESTIONS are
	// managed by their dedicated overlay paths (notifier-driven /
	// tiny-terminal branch / IsVisible-driven respectively) and excluded
	// here — SUGGESTIONS in particular is never pushed onto the focus
	// stack (frozen dbsavvy-etp design) so the teardown must not delete
	// its view out from under renderSuggestionsOverlay (dbsavvy-2fo).
	for _, ctx := range g.registry.Flatten() {
		if ctx == nil {
			continue
		}
		kind := ctx.GetKind()
		if kind != types.TEMPORARY_POPUP && kind != types.DISPLAY_CONTEXT {
			continue
		}
		key := ctx.GetKey()
		if key == types.WHICH_KEY || key == types.LIMIT || key == types.SUGGESTIONS {
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
	// reserves a 2-row "status" slot at the canvas bottom; we materialise
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
	// (x0+x+1, y0+y+1). The off-by-one cell offset places content at row
	// H (off-screen) for a bottom strip unless the rect is grown. We
	// follow the same trick commandLineRect uses for COMMAND_LINE: extend
	// the rectangle by -1/+1 in Y. For the Size:2 slot (Y1-Y0==1) gocui
	// then sees Height=4, InnerHeight=2, with the two visible rows landing
	// exactly on the boxlayout slot's reserved screen rows (d.Y0, d.Y1).
	// The "virtual" extra rows are never written to — gocui clamps cell
	// writes to inner bounds.
	if d, ok := dims[AppStatusViewName]; ok && d.X1 > d.X0 && d.Y1 >= d.Y0 {
		view, err := g.driver.SetView(AppStatusViewName, d.X0, d.Y0-1, d.X1, d.Y1+1, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		// Borderless 2-row strip — same shape as COMMAND_LINE. Without
		// Frame=false gocui would draw a border box around the cell.
		if view != nil {
			view.Frame = false
		}
		// Resolve the live *models.Connection by joining the activeConnID
		// state with the Deps.ConnectionsProvider (the same source the
		// Connections side rail walks). A missing provider or empty ID
		// collapses to nil — BuildStatusLine renders the no-conn slot.
		activeConn := func() *models.Connection {
			if g.connectionState.activeConnID == "" || g.deps.ConnectionsProvider == nil {
				return nil
			}
			for _, c := range g.deps.ConnectionsProvider() {
				if c.Name == g.connectionState.activeConnID {
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
			Driver:          g.driver,
			Tree:            g.tree,
			KbRuntime:       g.keybindingSystem.kbRuntime,
			ActiveConn:      activeConn,
			Tr:              tr,
			Toast:           g.toastHelp,
			BusyCount:       g.BusyCount,
			SpinnerFrame:    g.SpinnerFrame,
			TxStatus:        g.txStatusAccessor(),
			SessionSettings: g.sessionSettingsAccessor(),
			SearchStatus:    g.searchStatusAccessor(),
		})
	}

	// Tier 4: shy overlays driven by notifier visibility (not by stack
	// membership). LIMIT is handled in the early-return tiny-terminal
	// branch and never needs touching here.
	if err := g.renderWhichKeyOverlay(w, h, dims); err != nil {
		return err
	}
	// SUGGESTIONS is a shy overlay driven by IsVisible(), never by stack
	// membership (the editor keeps focus per the frozen dbsavvy-etp
	// design). dbsavvy-2fo.
	if err := g.renderSuggestionsOverlay(dims, w, h); err != nil {
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
			// every other tile (side rails) must keep the caret
			// off so the cursor doesn't bleed onto rail rows. PROMPT and
			// COMMAND_LINE are TEMPORARY_POPUPs and own their own caret
			// state via PromptHelper / CommandLineCommandDeps — we leave
			// their kinds untouched so those togglers stay authoritative.
			switch top.GetKind() {
			case types.SIDE_CONTEXT, types.MAIN_CONTEXT, types.EXTRAS_CONTEXT:
				enabled := top.GetKey() == types.QUERY_EDITOR
				g.driver.SetCaretEnabled(enabled)
				// Drive the caret shape from the editor's current mode so
				// the user sees a neovim-style distinction: a blinking bar
				// while inserting, a steady block in normal/visual. The
				// terminal default is a blinking bar/block on most
				// emulators, which is distracting in normal mode. tcell
				// deduplicates the escape sequence internally — safe to
				// call every frame.
				if enabled && gocui.Screen != nil {
					mode := g.keybindingSystem.matcher.CurrentMode(types.QUERY_EDITOR)
					gocui.Screen.SetCursorStyle(cursorStyleForMode(mode))
				}
			default:
				// Non-tiled top context (a focus-stack popup). Editable
				// popups (PROMPT, COMMAND_LINE, CELL_EDITOR, SEARCH_LINE)
				// self-enable the caret on HandleFocus and own it. A
				// non-editable popup (CONFIRMATION, MENU, …) must actively
				// clear any caret it inherited from the editable context
				// beneath it, or gocui draws a stale cursor at the popup's
				// (0,0) — e.g. CONFIRMATION pushed over a focused
				// QUERY_EDITOR (dbsavvy-u6p7).
				if !top.GetKey().IsEditable() {
					g.driver.SetCaretEnabled(false)
				}
			}
		}
	}

	g.resyncOnViewTeardown()
	g.resyncOnModalContentChange()
	return nil
}

// resyncOnViewTeardown forces a one-shot full Screen.Sync() on frames where
// the live gocui view set shrank since the previous frame. A view leaving the
// set (a closed CONNECTION_MANAGER modal, a dismissed popup/overlay) vacates
// the cells it occupied, but tcell's incremental Show() only re-emits cells
// whose content changed against its own model and does not repaint the
// orphaned region; the per-frame Screen.Clear() at the top of RunLayout blanks
// the back buffer but cannot force those physical cells to be re-emitted. A
// Sync() (clear-screen flag + full invalidate) evicts the ghosts. Restricted
// to teardown frames so steady-state rendering keeps the cheap diff path and
// the user never sees a full-screen repaint mid-edit (dbsavvy-1du).
func (g *Gui) resyncOnViewTeardown() {
	vc, ok := g.driver.(interface{ LiveViewCount() int })
	if !ok {
		return
	}
	n := vc.LiveViewCount()
	if n < g.prevLiveViews && gocui.Screen != nil {
		gocui.Screen.Sync()
	}
	g.prevLiveViews = n
}

// resyncOnModalContentChange forces a one-shot full Screen.Sync() on frames
// where the CONNECTION_MANAGER modal is open in ModeConnecting and its rendered
// body changed since the previous frame. The connect lifecycle churns the body
// in place (list row -> "Connecting…" -> "already connected" + retry hints);
// some of those transitions draw the body one row shifted for a frame, and
// tcell's incremental Show() never re-emits the cells the shifted frame
// vacated, so the bodies otherwise stack as ghosts that "move up" on every
// retry. The view buffer is always correct, so a Sync() (full re-emit from the
// correct back buffer) evicts the ghosts. Gated on an actual body change so
// steady-state connecting/error frames keep the cheap diff path, and scoped to
// ModeConnecting so benign list/form navigation never triggers a full repaint.
// Sibling of resyncOnViewTeardown, which covers only the view-count-shrink
// (modal close) case (dbsavvy-emu).
func (g *Gui) resyncOnModalContentChange() {
	if !g.modalIsTopMain() || g.registry.ConnectionManager.Mode() != guicontext.ModeConnecting {
		g.prevModalBody = ""
		return
	}
	name := g.registry.ConnectionManager.GetViewName()
	if name == "" {
		return
	}
	body := g.driver.GetViewBuffer(name)
	if body == g.prevModalBody {
		return
	}
	g.prevModalBody = body
	if gocui.Screen != nil {
		gocui.Screen.Sync()
	}
}

// modalIsTopMain reports whether the CONNECTION_MANAGER MAIN_CONTEXT modal
// is in the focus stack (possibly with popups stacked above it). When true,
// layoutConnectionManagerMain owns the dims["main"] slot and BOTH the side
// rails and the QUERY_EDITOR paint are suppressed so only the centered box
// (and any popup above it) renders over a blank background.
// Nil-safe across the registry / tree / context (epic dbsavvy-ig4).
func (g *Gui) modalIsTopMain() bool {
	if g.registry == nil || g.registry.ConnectionManager == nil || g.tree == nil {
		return false
	}
	for _, ctx := range g.tree.Stack() {
		if ctx.GetKey() == types.CONNECTION_MANAGER {
			return true
		}
	}
	return false
}

// connectionManagerWidthFrac / connectionManagerHeightFrac size the centered
// modal box as a fraction of the dims["main"] slot (epic dbsavvy-ig4).
const (
	connectionManagerWidthFrac  = 0.65
	connectionManagerHeightFrac = 0.65
)

// layoutConnectionManagerMain paints the CONNECTION_MANAGER modal as a
// centered bordered box inside the dims["main"] slot and registers the view
// in rails so it participates in the focus-frame swap. Called only when
// modalIsTopMain reports true, so the QUERY_EDITOR paint and the side rails
// are both suppressed for the frame (epic dbsavvy-ig4).
func (g *Gui) layoutConnectionManagerMain(dims map[string]ui.Dimensions, rails map[string]*gocui.View) {
	cm := g.registry.ConnectionManager
	name := cm.GetViewName()
	d, ok := dims["popup-overlay"]
	if !ok || name == "" || d.X1 <= d.X0 || d.Y1 <= d.Y0 {
		return
	}
	box := centeredRect(d, connectionManagerWidthFrac, connectionManagerHeightFrac)
	v, err := g.driver.SetView(name, box.X0, box.Y0, box.X1, box.Y1, 0)
	if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		return
	}
	if v != nil {
		rails[name] = v
		v.Title = cm.GetTitle()
		// Word-wrap long bodies (e.g. multi-line connection errors) at the
		// box's inner width instead of clipping them at the right border.
		// gocui's Wrap is whitespace-aware, so no manual wrapLabel plumbing
		// is needed here (unlike PROMPT, which wraps an editable buffer).
		v.Wrap = true
	}
	_ = cm.HandleRender()
	// The QUERY_EDITOR view from prior frames still exists in the dims["main"]
	// rect (it is never DeleteView'd). Lift the modal above it so the box is
	// actually visible while it owns the slot. Mirrors layoutConnectingMain.
	_, _ = g.driver.SetViewOnTop(name)
}

// popupRectFor derives a popup context's SetView rectangle from the
// size-policy descriptor it declares in the wiring table
// (pkg/gui/context/setup.go, contextSpec.popupRect). The orchestrator
// owns the pixel math here and switches on the descriptor Kind rather
// than the ContextKey; the per-context rationale (why each popup gets its
// size) now lives as data alongside its spec row. Rectangles are computed
// against dims["popup-overlay"] (the centred inner canvas inside the side
// rails / extras), with kind-specific fallbacks below.
func popupRectFor(key types.ContextKey, dims map[string]ui.Dimensions, w, h int) (rect, bool) {
	spec := guicontext.PopupRectSpecFor(key)
	switch spec.Kind {
	case types.PopupSizeCentered:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		return centeredRect(canvas, spec.WidthFrac, spec.HeightFrac), true
	case types.PopupSizeCommandLine:
		r := commandLineRect(dims)
		if r == (rect{}) {
			return rect{}, false
		}
		return r, true
	case types.PopupSizeCheatsheet:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			canvas = ui.Dimensions{X0: 0, Y0: 0, X1: w - 1, Y1: h - 1}
		}
		return centeredRectMaxSize(canvas, cheatsheetMaxCols, cheatsheetMaxRows), true
	case types.PopupSizeCellEditor:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		cw := canvas.X1 - canvas.X0
		maxCols := cw * 3 / 5
		return centeredRectMaxSize(canvas, maxCols, cellEditorMaxRows), true
	case types.PopupSizePrompt:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		return centeredRectMaxSize(canvas, promptMaxCols, promptMaxRows), true
	default:
		// PopupSizeNone: non-popup contexts plus LIMIT/WHICH_KEY (which
		// render via renderLimitOverlay / the which-key overlay, not this
		// Tier-3 loop). The silent default is load-bearing: TestWiringInvariant
		// fails if a popup-kind key (TEMPORARY_POPUP/DISPLAY_CONTEXT, minus the
		// allowlisted LIMIT/WHICH_KEY) declares no descriptor, instead of
		// silently rendering blank.
		return rect{}, false
	}
}

// suggestionsAnchorProvider is the duck-typed surface RunLayout reads to
// place the cursor-anchored SUGGESTIONS popup: the buffer Position the
// popup hangs off plus the current suggestion list (for height/width
// sizing). SuggestionsContext satisfies it; defined locally to avoid
// widening the IBaseContext surface (same pattern as commandLineViewSetter).
type suggestionsAnchorProvider interface {
	Anchor() editor.Position
	Suggestions() []editor.Suggestion
}

// suggestionsRowsMax / suggestionsColsMax bound the anchored dropdown so a
// long suggestion list / wide identifier can't blow past a sane popup size
// before the editor-view clamp runs. suggestionsRowsMax mirrors the
// context-side visible window (suggestionsVisibleMax).
const (
	suggestionsRowsMax = 8
	suggestionsColsMax = 60
)

// popupRect derives a popup context's SetView rectangle. Anchored-kind
// popups (the completion dropdown) are placed at the call site here —
// where the live editor view handle and the context anchor are in scope —
// because popupRectFor lacks access to either. All other kinds delegate to
// popupRectFor's pure pixel math.
func (g *Gui) popupRect(ctx types.IBaseContext, dims map[string]ui.Dimensions, w, h int) (rect, bool) {
	if guicontext.PopupRectSpecFor(ctx.GetKey()).Kind == types.PopupSizeAnchored {
		return g.anchoredPopupRect(ctx, dims, w, h)
	}
	// A masked PROMPT (the SSH credential prompt) carries its label in the
	// frame title, so its body is a single input line — size it compactly
	// instead of the worst-case validator-error height the fixed cap
	// reserves. popupRectFor still returns a valid rect for the key (the
	// wiring invariant only exercises that pure path), so this override is
	// purely the live, context-aware refinement.
	if m, ok := ctx.(interface{ Masked() bool }); ok && m.Masked() {
		if canvas, ok := dims["popup-overlay"]; ok {
			return centeredRectMaxSize(canvas, promptMaxCols, maskedPromptMaxRows), true
		}
	}
	// An unmasked PROMPT (e.g. the export edit-path / pgpass path field)
	// sizes its box to fit the label and "> "+buffer so a long path does
	// not horizontally scroll the shared view — which would clip the
	// label and hide the caret (dbsavvy-lcxe). The buffer is read live so
	// the box tracks typing; Initial() seeds the width on the first frame
	// before the TextArea is plumbed in.
	if ctx.GetKey() == types.PROMPT {
		if canvas, ok := dims["popup-overlay"]; ok {
			labelRunes := longestLineRunes(promptLabelOf(ctx))
			bufferRunes := len([]rune(promptBufferOf(ctx)))
			if g.promptHelp != nil {
				if n := len([]rune(g.promptHelp.Initial())); n > bufferRunes {
					bufferRunes = n
				}
			}
			cols := promptPopupCols(labelRunes, bufferRunes, canvas.X1-canvas.X0)
			return centeredRectMaxSize(canvas, cols, promptMaxRows), true
		}
	}
	return popupRectFor(ctx.GetKey(), dims, w, h)
}

// anchoredPopupRect places the cursor-anchored completion dropdown below
// the editor cursor using the live QUERY_EDITOR view geometry
// (Dimensions + Origin) and the context's buffer anchor. When the editor
// view handle is unavailable (e.g. before first paint, or under a fake
// driver that returns nil from ViewByName) it falls back to the centred
// rect declared in the spec so the popup is never lost at (0,0).
func (g *Gui) anchoredPopupRect(ctx types.IBaseContext, dims map[string]ui.Dimensions, w, h int) (rect, bool) {
	prov, ok := ctx.(suggestionsAnchorProvider)
	if !ok {
		return popupRectFallbackCentered(ctx.GetKey(), dims, w, h)
	}
	view, err := g.driver.ViewByName(string(types.QUERY_EDITOR))
	if err != nil || view == nil {
		return popupRectFallbackCentered(ctx.GetKey(), dims, w, h)
	}
	vx0, vy0, vx1, vy1 := view.Dimensions()
	ox, oy := view.Origin()

	suggestions := prov.Suggestions()
	rows := min(len(suggestions), suggestionsRowsMax)
	contentW := min(guicontext.SuggestionsRenderWidth(suggestions), suggestionsColsMax)
	return anchoredRect(vx0, vy0, vx1, vy1, ox, oy, prov.Anchor(), contentW, rows), true
}

// popupRectFallbackCentered returns the spec's centred rect (used when the
// anchored placement can't read the editor view). Reuses popupRectFor's
// PopupSizeCentered math by reading the spec's fractions directly.
func popupRectFallbackCentered(key types.ContextKey, dims map[string]ui.Dimensions, w, h int) (rect, bool) {
	canvas, ok := dims["popup-overlay"]
	if !ok {
		canvas = ui.Dimensions{X0: 0, Y0: 0, X1: w - 1, Y1: h - 1}
	}
	spec := guicontext.PopupRectSpecFor(key)
	return centeredRect(canvas, spec.WidthFrac, spec.HeightFrac), true
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

// whichKeyMaxCols caps the popup width; the renderer truncates per-row
// content to fit. whichKeyFrameRows is the gocui top+bottom border the
// popup height must add on top of one row per binding — the height is
// content-driven (dbsavvy-y5t) and clamped to the canvas by
// bottomRightRect, so a long binding list expands to fit instead of
// clipping overflow that the read-only popup cannot scroll.
const (
	whichKeyMaxCols   = 40
	whichKeyFrameRows = 2
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

// promptMaxRows / promptMaxCols cap the single-field PROMPT popup
// (dbsavvy-jzeo). The frame's top+bottom borders consume 2 rows; the
// remaining 6 content rows hold a word-wrapped label (1 line normally,
// up to ~3 for a multi-line validator-error re-prompt), a blank
// separator, and the "> <buffer>" input line — sized to the field, not
// a screen fraction. Clamped to the canvas at render time.
const (
	promptMaxRows = 8
	promptMaxCols = 64
)

// promptPopupCols returns the column count the unmasked PROMPT popup
// needs so its content fits without horizontal scrolling (dbsavvy-lcxe).
// The label and the editable buffer share one gocui view, so a single
// horizontal origin governs both lines: if the buffer is wide enough to
// scroll the origin (to chase the end-of-buffer caret), it drags the
// label and the "> " body prefix off the left edge, and the caret lands
// at view-x >= inner width where gocui's draw() hides it. Sizing the box
// to the content avoids the scroll entirely for realistic paths.
//
// content is the widest of the longest label line and the "> "+buffer
// input line (the "> " prefix is 2 cols). The result adds 2 for the
// frame borders plus 1 spare column so the end-of-buffer caret stays
// strictly inside the inner width, is floored at promptMaxCols (small
// prompts keep the compact box) and clamped to the canvas width.
func promptPopupCols(labelRunes, bufferRunes, canvasCols int) int {
	content := labelRunes
	if w := 2 + bufferRunes; w > content {
		content = w
	}
	cols := min(max(content+3, promptMaxCols), canvasCols)
	return cols
}

// promptLabelOf reads the prompt label via the duck-typed LabelText
// accessor, returning "" for contexts that don't expose it (the
// recorder-driver / non-PROMPT paths).
func promptLabelOf(ctx types.IBaseContext) string {
	if lr, ok := ctx.(interface{ LabelText() string }); ok {
		return lr.LabelText()
	}
	return ""
}

// promptBufferOf reads the prompt's typed buffer via the duck-typed
// Buffer accessor, returning "" when absent.
func promptBufferOf(ctx types.IBaseContext) string {
	if br, ok := ctx.(interface{ Buffer() string }); ok {
		return br.Buffer()
	}
	return ""
}

// longestLineRunes returns the rune count of the widest \n-separated
// line in s (labels may carry multi-line validator-error text).
func longestLineRunes(s string) int {
	max := 0
	for line := range strings.SplitSeq(s, "\n") {
		if n := len([]rune(line)); n > max {
			max = n
		}
	}
	return max
}

// maskedPromptMaxRows caps the masked (SSH credential) prompt height. Its
// label is the frame title, so the body is a single "> <buffer>" input
// line — top+bottom borders plus that line plus one spare row. Kept tight
// so the single-field secret prompt isn't an oversized box.
const maskedPromptMaxRows = 4

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
	// Size the popup to fit every binding row (one row each + the gocui
	// frame); bottomRightRect clamps the height to the canvas so it never
	// exceeds the screen (dbsavvy-y5t).
	rowCount := g.registry.WhichKey.RowCount(scope, prefix)
	r := bottomRightRect(canvas, whichKeyMaxCols, rowCount+whichKeyFrameRows)
	if _, err := g.driver.SetView(string(types.WHICH_KEY), r.X0, r.Y0, r.X1, r.Y1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		return err
	}
	_ = g.registry.WhichKey.HandleRender()
	_, _ = g.driver.SetViewOnTop(string(types.WHICH_KEY))
	return nil
}

// renderSuggestionsOverlay positions the cursor-anchored completion
// popup whenever the SuggestionsContext reports IsVisible() — driven by
// the popup's own visibility, NOT by focus-stack membership. The frozen
// dbsavvy-etp design keeps the QUERY_EDITOR focused (the controller
// intercepts nav keys while the popup is visible) and never pushes
// SUGGESTIONS onto the focus stack, so the focus-stack Tier-3 loop never
// rendered it (dbsavvy-2fo). On invisibility the view is best-effort
// deleted so it doesn't linger from a prior frame. A missing registry
// or suggestions context collapses to a no-op.
func (g *Gui) renderSuggestionsOverlay(dims map[string]ui.Dimensions, w, h int) error {
	if g.registry == nil || g.registry.Suggestions == nil {
		return nil
	}
	sugg := g.registry.Suggestions
	name := string(types.SUGGESTIONS)
	if !sugg.IsVisible() {
		_ = g.driver.DeleteView(name)
		return nil
	}
	r, ok := g.popupRect(sugg, dims, w, h)
	if !ok {
		_ = g.driver.DeleteView(name)
		return nil
	}
	if _, err := g.driver.SetView(name, r.X0, r.Y0, r.X1, r.Y1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		return err
	}
	_ = sugg.HandleRender()
	_, _ = g.driver.SetViewOnTop(name)
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
	w := max(min(maxCols, cw), 1)
	h := max(min(maxRows, ch), 1)
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
	w := max(min(maxCols, cw), 1)
	h := max(min(maxRows, ch), 1)
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
// resolveFocusedRailName returns the rail view name that should receive
// the ActiveBorder. Normally that is the focus-stack top's view name
// (stackViewName). The result pane is the exception: it multiplexes
// several result_tab_<slot> views behind a single focus-stack context
// captured when focus was pushed to results. gt/gT (Cycle) swap the live
// active tab without updating the stack, so the stack name goes stale.
// Whenever the stack points at any result tab we follow the live active
// tab view (activeTabView) instead, so the highlight tracks the visible
// tab rather than the one that was active at push time. dbsavvy-66p.
func resolveFocusedRailName(stackViewName, activeTabView string) string {
	if activeTabView != "" && strings.HasPrefix(stackViewName, types.ResultTabViewPrefix) {
		return activeTabView
	}
	return stackViewName
}

// cheatsheetScroller is the scroll surface CheatsheetContext exposes. The
// layout owns the bottom clamp because only it knows the rendered content
// height + viewport rows; the context owns the top clamp (>= 0).
type cheatsheetScroller interface {
	ScrollY() int
	SetScrollY(int)
}

// applyCheatsheetScroll pins the cheatsheet view's vertical origin to the
// context's scroll offset, clamped to the content's last page. Called
// after HandleRender so view.LinesHeight reflects the freshly written
// body. The clamped value is written back so `G` (a large sentinel) and
// any over-scroll settle exactly on the last page (dbsavvy-quyg).
func applyCheatsheetScroll(view *gocui.View, ctx types.IBaseContext) {
	sc, ok := ctx.(cheatsheetScroller)
	if !ok {
		return
	}
	maxOY := max(view.LinesHeight()-view.InnerHeight(), 0)
	oy := sc.ScrollY()
	if oy > maxOY {
		oy = maxOY
		sc.SetScrollY(oy)
	}
	view.SetOriginY(oy)
}

// tableInspectScroller is the scroll surface TableInspectContext exposes.
// The layout owns the bottom/right clamp (it alone knows the rendered
// content extent vs the viewport); the context owns the top/left clamp.
type tableInspectScroller interface {
	ScrollX() int
	ScrollY() int
	SetScrollX(int)
	SetScrollY(int)
}

// applyTableInspectScroll pins the inspect view's origin to the context's
// scroll offsets, clamping each axis to the content's last page / last
// column. Called after HandleRender so view dimensions reflect the
// freshly written body. Clamped values are written back so the `G` and
// `l` sentinels settle exactly on the last page / column (dbsavvy-ep0k).
func applyTableInspectScroll(view *gocui.View, ctx types.IBaseContext) {
	sc, ok := ctx.(tableInspectScroller)
	if !ok {
		return
	}
	maxOY := max(view.LinesHeight()-view.InnerHeight(), 0)
	oy := sc.ScrollY()
	if oy > maxOY {
		oy = maxOY
		sc.SetScrollY(oy)
	}
	maxOX := max(maxLineWidth(view)-view.InnerWidth(), 0)
	ox := sc.ScrollX()
	if ox > maxOX {
		ox = maxOX
		sc.SetScrollX(ox)
	}
	view.SetOrigin(ox, oy)
}

// maxLineWidth returns the display width of the widest line in the view's
// buffer. BufferLines reconstructs from cells (ANSI already consumed into
// attributes), so the rune width is the exact on-screen width.
func maxLineWidth(view *gocui.View) int {
	w := 0
	for _, l := range view.BufferLines() {
		if lw := runewidth.StringWidth(l); lw > w {
			w = lw
		}
	}
	return w
}

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
	return theme.AnsiFgSGR(s.Fg)
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

// promptBorderStyle picks the border colour for a PROMPT popup from its
// label: the free-form `<c-e>` expression prompt
// (helpers.WarnExprPromptLabel) gets WarnBorder to flag verbatim
// injection (dbsavvy-bwq.23); every other prompt keeps ActiveBorder.
// When WarnBorder is unset in the active theme frameAttr falls back to
// ColorDefault, so an absent key degrades gracefully.
func promptBorderStyle(label string) *theme.Style {
	if label == helpers.WarnExprPromptLabel {
		return theme.Current().WarnBorder
	}
	return theme.Current().ActiveBorder
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

// suggestionsFrame is the per-side frame cost (gocui draws a 1-cell
// border on each edge of a framed popup, so content width/height each
// need +2 to fit the rendered rows / longest line).
const suggestionsFrame = 2

// anchoredRect computes the outer SetView rectangle for a cursor-anchored
// dropdown (the completion SUGGESTIONS popup). The editor view occupies
// the screen rectangle (vx0,vy0)-(vx1,vy1); (ox,oy) is its scroll origin;
// anchor is the rune-indexed buffer Position the popup hangs off.
//
// The cursor's on-screen cell is (vx0+1+anchor.Col-ox, vy0+1+anchor.Line-oy)
// where the +1 accounts for the gocui frame border (content starts one cell
// inside the view).
// The dropdown renders on the row BELOW the cursor; when that would push
// its bottom past the editor's bottom edge (vy1) it flips ABOVE, ending at
// the cursor row. contentW is the longest suggestion Display width and
// rows is the visible suggestion count; both gain a 1-cell frame per side.
// The final rect is clamped within the editor view bounds.
//
// Wide-char (CJK/emoji) rune→cell width is best-effort v1: ASCII
// identifiers position correctly (epic dbsavvy-etp out-of-scope note).
func anchoredRect(vx0, vy0, vx1, vy1, ox, oy int, anchor editor.Position, contentW, rows int) rect {
	cursorX := vx0 + 1 + (anchor.Col - ox)
	cursorY := vy0 + 1 + (anchor.Line - oy)

	pw := contentW + suggestionsFrame
	if maxW := vx1 - vx0; pw > maxW {
		pw = maxW
	}
	if pw < 1 {
		pw = 1
	}
	ph := rows + suggestionsFrame
	if maxH := vy1 - vy0; ph > maxH {
		ph = maxH
	}
	if ph < 1 {
		ph = 1
	}

	y0 := cursorY + 1
	y1 := y0 + ph
	if y1 > vy1 {
		// Flip above: the popup ends at the cursor row (y1 = cursorY) so
		// it never overlaps the cursor line, and grows upward.
		y1 = cursorY
		y0 = y1 - ph
	}

	x0 := cursorX
	x1 := x0 + pw
	return clampRect(rect{X0: x0, Y0: y0, X1: x1, Y1: y1}, vx0, vy0, vx1, vy1)
}

// clampRect slides r so it fits within (bx0,by0)-(bx1,by1), preserving its
// width/height where possible and shrinking only when the bounds are
// smaller than the rect.
func clampRect(r rect, bx0, by0, bx1, by1 int) rect {
	w := r.X1 - r.X0
	h := r.Y1 - r.Y0
	if r.X1 > bx1 {
		r.X0 = bx1 - w
		r.X1 = bx1
	}
	if r.X0 < bx0 {
		r.X0 = bx0
		if r.X1 > bx1 {
			r.X1 = bx1
		}
	}
	if r.Y1 > by1 {
		r.Y0 = by1 - h
		r.Y1 = by1
	}
	if r.Y0 < by0 {
		r.Y0 = by0
		if r.Y1 > by1 {
			r.Y1 = by1
		}
	}
	return r
}
