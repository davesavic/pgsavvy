package types

// GuiDriver is the minimum gocui-runtime surface the rest of pkg/gui
// depends on. Concrete impls wrap *gocui.Gui (production) or a fake
// (tests). Keeping the surface explicit lets context/controller code be
// unit-tested without instantiating a real terminal Gui.
type GuiDriver interface {
	// Write appends bytes to the named view's content buffer.
	Write(viewName string, b []byte) (int, error)

	// GetViewBuffer returns the current text content of the named view.
	GetViewBuffer(viewName string) string

	// SetView creates or repositions a view at the given rectangle and
	// returns its handle. overlaps is the gocui edge bitmask.
	SetView(name string, x0, y0, x1, y1 int, overlaps byte) (View, error)

	// SetKeybinding registers a key handler scoped to viewName. An empty
	// viewName binds the handler globally.
	SetKeybinding(viewName string, key Key, mod Modifier, handler func() error) error

	// SetViewClickBinding registers a mouse/click handler. The gocui
	// surface is SetViewClickBinding (NOT SetMouseBinding); see decision
	// dbsavvy-enn-T0-gocui-pin.
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

	// MainLoop runs the gocui event loop and blocks until Close is
	// called or an unrecoverable error occurs.
	MainLoop() error

	// Close tears down the runtime. Implementations should make this
	// idempotent.
	Close() error
}
