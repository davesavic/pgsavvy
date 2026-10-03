package orchestrator

import (
	"context"
	"errors"
	"sync"

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

	publicationMu sync.Mutex
	publications  []*uiPublication
	accepted      map[*uiPublication]struct{}
	wake          chan struct{}
	publisherDone chan struct{}
	started       bool
	retiring      bool
	terminalErr   error
	onRetire      func()
}

type uiPublication struct {
	ctx         context.Context
	cancel      context.CancelFunc
	fn          func() error
	contentOnly bool
	settled     func(error)
	stopCancel  func() bool
	once        sync.Once
}

// newGocuiDriver wraps an already-constructed *gocui.Gui. The caller
// owns the Gui's lifecycle; the driver simply adapts the surface.
func newGocuiDriver(g *gocui.Gui) *gocuiDriver {
	return &gocuiDriver{
		g: g, accepted: make(map[*uiPublication]struct{}),
		wake: make(chan struct{}, 1), publisherDone: make(chan struct{}),
	}
}

func (d *gocuiDriver) settle(p *uiPublication, err error) {
	p.once.Do(func() {
		d.publicationMu.Lock()
		delete(d.accepted, p)
		stop := p.stopCancel
		d.publicationMu.Unlock()
		if stop != nil {
			stop()
		}
		p.cancel()
		if p.settled != nil {
			p.settled(err)
		}
	})
}

// Admission never sends into native userEvents: that send can block while a
// MainLoop callback is stopping a stream. One ordered publisher owns all sends.
func (d *gocuiDriver) publish(ctx context.Context, fn func() error, contentOnly bool, settled func(error)) {
	ctx, cancel := context.WithCancel(ctx)
	p := &uiPublication{ctx: ctx, cancel: cancel, fn: fn, contentOnly: contentOnly, settled: settled}
	d.publicationMu.Lock()
	if d.retiring {
		d.publicationMu.Unlock()
		d.settle(p, context.Canceled)
		return
	}
	d.accepted[p] = struct{}{}
	p.stopCancel = context.AfterFunc(ctx, func() { d.settle(p, ctx.Err()) })
	d.publications = append(d.publications, p)
	d.publicationMu.Unlock()
	d.wakePublisher()
}

func (d *gocuiDriver) wakePublisher() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *gocuiDriver) retire(err error) {
	d.publicationMu.Lock()
	if d.retiring {
		d.publicationMu.Unlock()
		return
	}
	d.retiring = true
	d.terminalErr = err
	pending := make([]*uiPublication, 0, len(d.accepted))
	for p := range d.accepted {
		pending = append(pending, p)
	}
	onRetire := d.onRetire
	d.publicationMu.Unlock()
	for _, p := range pending {
		d.settle(p, context.Canceled)
	}
	if onRetire != nil {
		onRetire()
	}
	d.wakePublisher()
}

func (d *gocuiDriver) callbackResult(err error) error {
	if err != nil {
		d.retire(err)
	}
	// A terminal callback is appended only after the publisher's last send.
	// Until then native MainLoop must keep draining, not wait for that sender.
	return nil
}

func (d *gocuiDriver) publishLoop() {
	defer close(d.publisherDone)
	for {
		d.publicationMu.Lock()
		if d.retiring {
			d.publications = nil
			err := d.terminalErr
			d.publicationMu.Unlock()
			d.g.UpdateContentOnly(func(*gocui.Gui) error {
				// Dequeueing this final event releases the last native send;
				// the publisher has no subsequent UI-dependent work.
				<-d.publisherDone
				return err
			})
			return
		}
		if len(d.publications) == 0 {
			d.publicationMu.Unlock()
			<-d.wake
			continue
		}
		p := d.publications[0]
		d.publications[0] = nil
		d.publications = d.publications[1:]
		d.publicationMu.Unlock()
		fn := func(*gocui.Gui) error {
			d.publicationMu.Lock()
			retiring := d.retiring
			d.publicationMu.Unlock()
			if retiring || p.ctx.Err() != nil {
				d.settle(p, context.Canceled)
				return nil
			}
			err := p.fn()
			callerCanceled := errors.Is(err, context.Canceled) && p.ctx.Err() != nil
			d.settle(p, err)
			if callerCanceled {
				return nil
			}
			return d.callbackResult(err)
		}
		if p.contentOnly {
			d.g.UpdateContentOnly(fn)
		} else {
			d.g.UpdateAsync(fn)
		}
	}
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
		return d.callbackResult(handler())
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
	if e, ok := ed.(*masterEditor); ok {
		e.requestQuit = func() { d.retire(gocui.ErrQuit) }
	}
	v.Editor = ed
	return nil
}

func (d *gocuiDriver) SetViewClickBinding(b *types.ViewMouseBinding) error {
	copyBinding := *b
	copyBinding.Handler = func(opts types.ViewMouseBindingOpts) error {
		return d.callbackResult(b.Handler(opts))
	}
	return d.g.SetViewClickBinding(&copyBinding)
}

func (d *gocuiDriver) Update(fn func() error) {
	d.publish(context.Background(), fn, false, nil)
}

func (d *gocuiDriver) UpdateContentOnly(fn func() error) {
	d.publish(context.Background(), fn, true, nil)
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
	d.g.SetManager(gocui.ManagerFunc(func(g *gocui.Gui) error {
		d.publicationMu.Lock()
		retiring := d.retiring
		d.publicationMu.Unlock()
		if retiring {
			return nil
		}
		for _, manager := range managers {
			if err := manager.Layout(g); err != nil {
				return d.callbackResult(err)
			}
		}
		return nil
	}))
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

// SetViewTabs sets the native in-border tab labels and active index on
// the named view. labels are caller-supplied verbatim (a caller may bake
// an active marker into them — the driver is agnostic). activeIdx is
// clamped into [0, len(labels)-1] (0 when labels is empty) so an
// out-of-range index never causes an out-of-bounds render.
func (d *gocuiDriver) SetViewTabs(name string, labels []string, activeIdx int) error {
	v, err := d.g.View(name)
	if err != nil {
		return err
	}
	v.Tabs = labels
	v.TabIndex = clampTabIndex(activeIdx, len(labels))
	return nil
}

// clampTabIndex returns idx clamped into [0, n-1], or 0 when n == 0.
func clampTabIndex(idx, n int) int {
	if n == 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// SetTabClickBinding registers a handler invoked with the clicked tab
// index when the user clicks a native in-border tab on the named view.
func (d *gocuiDriver) SetTabClickBinding(name string, handler func(idx int) error) error {
	return d.g.SetTabClickBinding(name, func(idx int) error { return d.callbackResult(handler(idx)) })
}

// SetViewTabColors sets the active (SelFgColor) and inactive (FgColor)
// tab-label foreground colors on the named view. Takes resolved
// gocui.Attribute values — the driver does not resolve themes.
func (d *gocuiDriver) SetViewTabColors(name string, activeFg, inactiveFg gocui.Attribute) error {
	v, err := d.g.View(name)
	if err != nil {
		return err
	}
	v.SelFgColor = activeFg
	v.FgColor = inactiveFg
	return nil
}

func (d *gocuiDriver) MainLoop() error {
	d.publicationMu.Lock()
	d.started = true
	d.publicationMu.Unlock()
	go d.publishLoop()
	priorErrorHandler := d.g.ErrorHandler
	d.g.ErrorHandler = func(err error) error {
		d.publicationMu.Lock()
		retiring := d.retiring
		d.publicationMu.Unlock()
		if retiring {
			select {
			case <-d.publisherDone:
				return err
			default:
			}
		}
		if priorErrorHandler != nil {
			err = priorErrorHandler(err)
		}
		return d.callbackResult(err)
	}
	defer d.retire(gocui.ErrQuit)
	return d.g.MainLoop()
}

func (d *gocuiDriver) Close() error {
	d.retire(gocui.ErrQuit)
	d.g.Close()
	d.publicationMu.Lock()
	started := d.started
	d.publicationMu.Unlock()
	if started {
		select {
		case <-d.publisherDone:
		default:
			return errors.New("native UI publisher has not retired")
		}
	}
	return nil
}
