package orchestrator

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// LiveViewCount returns the number of live gocui views. RunLayout reads it to
// detect teardown frames (a shrink in the set) that need a full Screen.Sync()
// to evict cells orphaned by a closed modal/popup, which the incremental
// Show() leaves behind.
func (d *gocuiDriver) LiveViewCount() int {
	return len(d.g.Views())
}

// gocuiDriver wraps *gocui.Gui to satisfy types.GuiDriver. Per
// architectural decision D4 / G3-D: this is the ONLY place that ever
// imports and references gocui.Gui; the rest of pkg/gui reaches the
// runtime through the GuiDriver interface.
type gocuiDriver struct {
	g *gocui.Gui
}

// newGocuiDriver wraps an already-constructed *gocui.Gui. The caller
// owns the Gui's lifecycle; the driver simply adapts the surface.
func newGocuiDriver(g *gocui.Gui) *gocuiDriver {
	return &gocuiDriver{g: g}
}

func (d *gocuiDriver) Write(viewName string, b []byte) (int, error) {
	v, err := d.g.View(viewName)
	if err != nil {
		return 0, err
	}
	return v.Write(b)
}

func (d *gocuiDriver) SetContent(viewName string, str string) error {
	v, err := d.g.View(viewName)
	if err != nil {
		return err
	}
	v.SetContent(str)
	return nil
}

func (d *gocuiDriver) GetViewBuffer(viewName string) string {
	v, err := d.g.View(viewName)
	if err != nil {
		return ""
	}
	return v.Buffer()
}

func (d *gocuiDriver) SetView(name string, x0, y0, x1, y1 int, overlaps byte) (types.View, error) {
	v, err := d.g.SetView(name, x0, y0, x1, y1, overlaps)
	return v, err
}

func (d *gocuiDriver) SetKeybinding(viewName string, key types.Key, mod types.Modifier, handler func() error) error {
	// gocui's SetKeybinding handler takes (*Gui, *View). Wrap the
	// handler closure so callers can pass a parameterless func() error.
	wrapped := func(_ *gocui.Gui, _ *gocui.View) error {
		return handler()
	}
	// gocui.Key is a {keyName, str, mod} composite — Equals checks all
	// three fields against the runtime-decoded event Key. Callers pass
	// the modifier alongside (the GuiDriver surface mirrors gocui's
	// older two-arg shape); rebuild the Key with the modifier baked in
	// so bindings like Ctrl+C match the event the tcell layer emits.
	keyWithMod := gocui.NewKey(key.KeyName(), key.Str(), mod)
	return d.g.SetKeybinding(viewName, keyWithMod, wrapped)
}

// Gocui returns the underlying *gocui.Gui. Used by wireWithDriver to
// construct the master Editor (NewMasterEditor needs *gocui.Gui to
// schedule pending-buffer flushes onto the MainLoop). Returns nil when
// the driver is not a real gocuiDriver — tests with a recorder driver
// pass nil through to NewMasterEditor, which handles it.
func (d *gocuiDriver) Gocui() *gocui.Gui {
	if d == nil {
		return nil
	}
	return d.g
}

func (d *gocuiDriver) SetMasterEditor(view string, ed gocui.Editor) error {
	v, err := d.g.View(view)
	if err != nil {
		return err
	}
	v.Editable = true
	v.Editor = ed
	return nil
}

func (d *gocuiDriver) SetViewClickBinding(b *types.ViewMouseBinding) error {
	return d.g.SetViewClickBinding(b)
}

func (d *gocuiDriver) Update(fn func() error) {
	d.g.Update(func(*gocui.Gui) error { return fn() })
}

func (d *gocuiDriver) UpdateContentOnly(fn func() error) {
	d.g.UpdateContentOnly(func(*gocui.Gui) error { return fn() })
}

func (d *gocuiDriver) SetCurrentView(viewName string) (types.View, error) {
	return d.g.SetCurrentView(viewName)
}

func (d *gocuiDriver) SetViewOnTop(viewName string) (types.View, error) {
	return d.g.SetViewOnTop(viewName)
}

func (d *gocuiDriver) ViewByName(name string) (types.View, error) {
	return d.g.View(name)
}

func (d *gocuiDriver) DeleteView(name string) error {
	return d.g.DeleteView(name)
}

func (d *gocuiDriver) SetManager(managers ...types.Manager) {
	d.g.SetManager(managers...)
}

func (d *gocuiDriver) SetCaretEnabled(enabled bool) {
	d.g.Cursor = enabled
}

func (d *gocuiDriver) SetViewCursor(viewName string, x, y int) error {
	v, err := d.g.View(viewName)
	if err != nil {
		return err
	}
	v.SetCursor(x, y)
	return nil
}

func (d *gocuiDriver) MainLoop() error {
	return d.g.MainLoop()
}

func (d *gocuiDriver) Close() error {
	d.g.Close()
	return nil
}
