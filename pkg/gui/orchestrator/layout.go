package orchestrator

import (
	"errors"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
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
	// created every frame regardless of focus-stack state.
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
		if _, err := g.driver.SetView(name, d.X0, d.Y0, d.X1, d.Y1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
		_ = ctx.HandleRender()
	}

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
			// COMMAND_LINE is an editable view; the master Editor is bound
			// to the view-instance. Each Push creates a fresh view (the
			// prior was DeleteView'd here), so reattach on every frame the
			// context is on the stack. SetMasterEditor is idempotent. On
			// fresh creation, prepopulate the TextArea with the leading
			// ":" prompt and plumb the view handle through to the
			// CommandLineContext so command.submit can read v.TextArea.
			if ctx.GetKey() == types.COMMAND_LINE && g.commandLineEditor != nil {
				if view != nil {
					view.Frame = false
				}
				_ = g.driver.SetMasterEditor(name, g.commandLineEditor)
				if view != nil {
					if freshView && view.TextArea != nil {
						view.TextArea.TypeCharacter(":")
						view.RenderTextArea()
					}
					if cl, ok := ctx.(commandLineViewSetter); ok {
						cl.SetView(view)
					}
				}
				// Anchor the caret to the end of the typed buffer each
				// frame. Production view exposes TextArea.GetContent (full
				// content including ':'); tests use the RecorderGuiDriver
				// which returns view=nil from SetView, so fall back to the
				// context's Buffer() which strips the ':' prompt — add 1
				// for the prompt column. Bug dbsavvy-tro.2.
				cursorX := 1
				if view != nil && view.TextArea != nil {
					cursorX = len(view.TextArea.GetContent())
				} else if bufHolder, ok := ctx.(interface{ Buffer() string }); ok {
					cursorX = 1 + len(bufHolder.Buffer())
				}
				_ = g.driver.SetViewCursor(name, cursorX, 0)
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
		}
	}

	return nil
}

// popupRectFor maps a popup ContextKey to its SetView rectangle. The
// rectangle is computed against dims["popup-overlay"] (the centred
// inner canvas inside the side rails / extras).
func popupRectFor(key types.ContextKey, dims map[string]ui.Dimensions, w, h int) (rect, bool) {
	switch key {
	case types.MENU, types.CONFIRMATION, types.PROMPT, types.SUGGESTIONS:
		canvas, ok := dims["popup-overlay"]
		if !ok {
			return rect{}, false
		}
		return centeredRect(canvas, 0.5, 0.5), true
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
