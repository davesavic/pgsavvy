package orchestrator

import (
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

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
	_ = mod // gocui.SetKeybinding takes (viewname, Key, handler); the modifier is folded into Key per gocui's tcell layer.
	return d.g.SetKeybinding(viewName, key, wrapped)
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

func (d *gocuiDriver) MainLoop() error {
	return d.g.MainLoop()
}

func (d *gocuiDriver) Close() error {
	d.g.Close()
	return nil
}
