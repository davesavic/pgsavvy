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

// Layout satisfies gocui.Manager. The runtime invokes it on every
// frame; we delegate to RunLayout so the same code path is testable
// without a real *gocui.Gui.
func (g *Gui) Layout(ng *gocui.Gui) error {
	w, h := ng.Size()
	return g.RunLayout(w, h)
}

// RunLayout positions every live (non-STUB) Context's view inside a
// terminal of the supplied dimensions. Below the limit threshold the
// pass renders only the LIMIT overlay (D11 / terminal-too-small AC).
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

	dims := ui.GetWindowDimensions(w, h)

	// Compute a centred popup rectangle covering ~50% of the available
	// canvas inside popup-overlay.
	popup := centeredRect(dims["popup-overlay"], 0.5, 0.5)

	for _, name := range orderedViewNames() {
		ctx := g.registry.ByKey(types.ContextKey(name))
		if ctx == nil || ctx.GetKind() == types.STUB {
			continue
		}
		rect, ok := chooseRect(name, dims, popup)
		if !ok {
			continue
		}
		if _, err := g.driver.SetView(name, rect.X0, rect.Y0, rect.X1, rect.Y1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			return err
		}
	}

	// Limit overlay is not active at this size; best-effort delete it
	// so it doesn't linger from a previous tiny-terminal frame.
	_ = g.driver.DeleteView(string(types.LIMIT))

	// Raise everything to its declared z-order. Failures here are not
	// load-bearing — a missing view simply hasn't been created yet.
	for _, name := range orderedViewNames() {
		_, _ = g.driver.SetViewOnTop(name)
	}

	// Best-effort render pass on every live context. DISPLAY_CONTEXT
	// instances (LimitContext, WhichKeyContext) are rendered from
	// their dedicated overlay functions instead; invoking their
	// HandleRender here would queue a Write to a view that may not have
	// been created for this frame, surfacing gocui.ErrUnknownView out of
	// the MainLoop.
	for _, ctx := range g.registry.Flatten() {
		if ctx == nil || ctx.GetKind() == types.STUB || ctx.GetKind() == types.DISPLAY_CONTEXT {
			continue
		}
		_ = ctx.HandleRender()
	}

	if err := g.renderWhichKeyOverlay(w, h, dims); err != nil {
		return err
	}
	return g.renderCheatsheetOverlay(w, h, dims)
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

// renderCheatsheetOverlay positions the CHEATSHEET view centred inside
// popup-overlay and invokes CheatsheetContext.HandleRender — but only
// when CHEATSHEET is currently on the focus stack. When absent, the
// view is best-effort deleted so it doesn't linger from a prior frame.
//
// Defensive nil-guards mirror renderWhichKeyOverlay: a missing
// registry, missing Cheatsheet context, or missing focus stack
// collapses to a no-op.
func (g *Gui) renderCheatsheetOverlay(w, h int, dims map[string]ui.Dimensions) error {
	if g.registry == nil || g.registry.Cheatsheet == nil {
		return nil
	}
	if !g.cheatsheetOnStack() {
		_ = g.driver.DeleteView(string(types.CHEATSHEET))
		return nil
	}
	canvas, ok := dims["popup-overlay"]
	if !ok {
		canvas = ui.Dimensions{X0: 0, Y0: 0, X1: w - 1, Y1: h - 1}
	}
	r := centeredRectMaxSize(canvas, cheatsheetMaxCols, cheatsheetMaxRows)
	if _, err := g.driver.SetView(string(types.CHEATSHEET), r.X0, r.Y0, r.X1, r.Y1, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		return err
	}
	_ = g.registry.Cheatsheet.HandleRender()
	_, _ = g.driver.SetViewOnTop(string(types.CHEATSHEET))
	return nil
}

// cheatsheetOnStack reports whether the CHEATSHEET context is currently
// present on the focus stack (it is a DISPLAY_CONTEXT, so Push just
// appends rather than wiping/replacing). Returns false when the focus
// tree is nil.
func (g *Gui) cheatsheetOnStack() bool {
	if g.tree == nil {
		return false
	}
	for _, c := range g.tree.Stack() {
		if c != nil && c.GetKey() == types.CHEATSHEET {
			return true
		}
	}
	return false
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

// chooseRect maps a view name onto the window-arrangement output. The
// popup overlay rect is shared by every popup view; side rails resolve
// to their named dims entry.
func chooseRect(name string, dims map[string]ui.Dimensions, popup rect) (rect, bool) {
	switch name {
	case string(types.MENU),
		string(types.CONFIRMATION),
		string(types.PROMPT),
		string(types.SUGGESTIONS):
		return popup, true
	case string(types.CHEATSHEET):
		// CHEATSHEET is positioned by renderCheatsheetOverlay (it is a
		// DISPLAY_CONTEXT, rendered only when on the focus stack). The
		// main layout pass must NOT create the view eagerly — that would
		// leave an empty popup on screen at startup.
		return rect{}, false
	case string(types.COMMAND_LINE):
		return commandLineRect(dims), true
	case string(types.LOG):
		d, ok := dims["extras"]
		if !ok {
			return rect{}, false
		}
		return rect{X0: d.X0, Y0: d.Y0, X1: d.X1, Y1: d.Y1}, true
	default:
		d, ok := dims[name]
		if !ok {
			return rect{}, false
		}
		return rect{X0: d.X0, Y0: d.Y0, X1: d.X1, Y1: d.Y1}, true
	}
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
		Y1: canvas.Y1,
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
