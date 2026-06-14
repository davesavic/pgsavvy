package types

import "github.com/jesseduffield/lazygit/pkg/gocui"

// GuiDriver is the minimum gocui-runtime surface the rest of pkg/gui
// depends on. Concrete impls wrap *gocui.Gui (production) or a fake
// (tests). Keeping the surface explicit lets context/controller code be
// unit-tested without instantiating a real terminal Gui.
type GuiDriver interface {
	// Write appends bytes to the named view's content buffer. Reserved
	// for streaming/append-only sinks (e.g. LOG); full-redraw render
	// passes must use SetContent so the buffer doesn't grow per frame.
	Write(viewName string, b []byte) (int, error)

	// SetContent replaces the named view's content with str (gocui's
	// View.SetContent clears the buffer under the view's writeMutex and
	// writes str in one critical section). This is the correct primitive
	// for any HandleRender that re-renders the whole view from scratch.
	SetContent(viewName string, str string) error

	// GetViewBuffer returns the current text content of the named view.
	GetViewBuffer(viewName string) string

	// SetView creates or repositions a view at the given rectangle and
	// returns its handle. overlaps is the gocui edge bitmask.
	SetView(name string, x0, y0, x1, y1 int, overlaps byte) (View, error)

	// SetKeybinding registers a key handler scoped to viewName. An empty
	// viewName binds the handler globally.
	SetKeybinding(viewName string, key Key, mod Modifier, handler func() error) error

	// SetMasterEditor installs ed as the per-view gocui.Editor for the
	// named view (and marks the view editable). Used to route
	// every keystroke for a view through the chord Matcher.
	SetMasterEditor(view string, ed gocui.Editor) error

	// SetViewClickBinding registers a mouse/click handler. The gocui
	// surface is SetViewClickBinding (NOT SetMouseBinding).
	SetViewClickBinding(b *ViewMouseBinding) error

	// Update schedules fn for execution on the MainLoop with a full
	// re-layout afterwards.
	Update(fn func() error)

	// UpdateContentOnly schedules fn for execution on the MainLoop with
	// a content-only repaint (no re-layout). Required for low-flicker
	// partial updates (DESIGN.md §6).
	UpdateContentOnly(fn func() error)

	// SetCurrentView gives the named view input focus.
	SetCurrentView(viewName string) (View, error)

	// SetViewOnTop raises the named view to the top of the z-order.
	SetViewOnTop(viewName string) (View, error)

	// ViewByName returns the handle for the named view, or an error if
	// no such view has been created.
	ViewByName(name string) (View, error)

	// DeleteView removes the named view and all its keybindings.
	DeleteView(name string) error

	// SetManager installs the variadic Manager chain the runtime calls
	// on every Layout pass.
	SetManager(managers ...Manager)

	// SetCaretEnabled toggles the global terminal caret (gocui.Gui.Cursor
	// — gui.go:161-162 in the vendored lazygit/gocui fork). When true,
	// gocui calls Screen.ShowCursor each frame at the current view's
	// cursor position; when false, Screen.HideCursor. CommandOpen flips
	// it on; CommandCancel/Submit flip it off.
	SetCaretEnabled(enabled bool)

	// SetViewCursor positions the per-view caret of the named view to
	// (x, y) via *gocui.View.SetCursor (view.go:612-618). Used by the
	// COMMAND_LINE Layout branch to anchor the caret to the end of the
	// typed buffer. No-op (returns nil) under fakes where the view
	// doesn't exist or where the driver records calls instead.
	SetViewCursor(viewName string, x, y int) error

	// MainLoop runs the gocui event loop and blocks until Close is
	// called or an unrecoverable error occurs.
	MainLoop() error

	// Close tears down the runtime. Implementations should make this
	// idempotent.
	Close() error
}
