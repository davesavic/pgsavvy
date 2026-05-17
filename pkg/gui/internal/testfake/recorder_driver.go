// Package testfake supplies an in-memory GuiDriver fake used by tests in
// pkg/gui. Recorder semantics: every driver call is appended to an
// in-memory log, and key-binding handlers are invoked verbatim by
// FeedKey. The fake invokes Update/UpdateContentOnly closures inline so
// tests see their side effects without spinning up a real MainLoop.
package testfake

import (
	"errors"
	"sync"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// SetViewCall records one SetView invocation.
type SetViewCall struct {
	Name           string
	X0, Y0, X1, Y1 int
	Overlaps       byte
}

// KbRecord is the public shape returned by AllKeybindings.
type KbRecord struct {
	View    string
	Key     types.Key
	Mod     types.Modifier
	Handler func() error
}

// ClickRecord captures one SetViewClickBinding call.
type ClickRecord struct {
	View    string
	Key     types.KeyName
	Mod     types.Modifier
	Handler func(types.ViewMouseBindingOpts) error
}

// LayoutRunner is the optional interface the recorder calls from SetSize
// to drive Layout once view dimensions change.
type LayoutRunner interface {
	RunLayout(w, h int) error
}

// RecorderGuiDriver implements types.GuiDriver in memory.
type RecorderGuiDriver struct {
	mu sync.Mutex

	width, height int

	SetViewCalls      []SetViewCall
	SetViewOnTopCalls []string
	SetCurrentViewLog []string
	DeleteViews       []string
	UpdateCalls       int
	ContentOnlyCalls  int
	updateErrors      []error

	bindings []KbRecord
	clicks   []ClickRecord

	views   map[string]*viewState
	manager types.Manager
}

type viewState struct {
	buf []byte
}

// NewRecorderGuiDriver builds an empty recorder. Default size 80x24.
func NewRecorderGuiDriver() *RecorderGuiDriver {
	return &RecorderGuiDriver{
		width:  80,
		height: 24,
		views:  map[string]*viewState{},
	}
}

// SetSize updates the recorded terminal size and (if a manager is
// installed and implements RunLayout) triggers a fresh layout pass.
func (r *RecorderGuiDriver) SetSize(w, h int) error {
	r.mu.Lock()
	r.width = w
	r.height = h
	mgr := r.manager
	r.mu.Unlock()
	if mgr == nil {
		return nil
	}
	if lr, ok := mgr.(LayoutRunner); ok {
		return lr.RunLayout(w, h)
	}
	return nil
}

// FeedKey invokes the recorded handler for (view, key, mod) if any.
// Returns errNotFound when no matching binding has been registered.
func (r *RecorderGuiDriver) FeedKey(view string, key types.Key, mod types.Modifier) error {
	r.mu.Lock()
	var handler func() error
	for i := range r.bindings {
		b := r.bindings[i]
		if b.View == view && b.Key == key && b.Mod == mod {
			handler = b.Handler
			break
		}
	}
	r.mu.Unlock()
	if handler == nil {
		return errNotFound
	}
	return handler()
}

var errNotFound = errors.New("recorder: no binding for (view, key, mod)")

// HasKeybinding reports whether (view, key, mod) was registered.
func (r *RecorderGuiDriver) HasKeybinding(view string, key types.Key, mod types.Modifier) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.bindings {
		if b.View == view && b.Key == key && b.Mod == mod {
			return true
		}
	}
	return false
}

// KeybindingCount returns the number of registered key bindings.
func (r *RecorderGuiDriver) KeybindingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bindings)
}

// AllKeybindings returns a copy of the registered key bindings.
func (r *RecorderGuiDriver) AllKeybindings() []KbRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]KbRecord, len(r.bindings))
	copy(out, r.bindings)
	return out
}

// AllSetViewCalls returns a copy of the SetView log.
func (r *RecorderGuiDriver) AllSetViewCalls() []SetViewCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SetViewCall, len(r.SetViewCalls))
	copy(out, r.SetViewCalls)
	return out
}

// HasSetView reports whether SetView was called for the given view name.
func (r *RecorderGuiDriver) HasSetView(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.SetViewCalls {
		if c.Name == name {
			return true
		}
	}
	return false
}

// --- types.GuiDriver implementation ---

func (r *RecorderGuiDriver) Write(viewName string, b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[viewName]
	if !ok {
		return 0, gocui.ErrUnknownView
	}
	v.buf = append(v.buf, b...)
	return len(b), nil
}

func (r *RecorderGuiDriver) GetViewBuffer(viewName string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[viewName]
	if !ok {
		return ""
	}
	return string(v.buf)
}

func (r *RecorderGuiDriver) SetView(name string, x0, y0, x1, y1 int, overlaps byte) (types.View, error) {
	r.mu.Lock()
	r.SetViewCalls = append(r.SetViewCalls, SetViewCall{Name: name, X0: x0, Y0: y0, X1: x1, Y1: y1, Overlaps: overlaps})
	_, existed := r.views[name]
	if !existed {
		r.views[name] = &viewState{}
	}
	r.mu.Unlock()
	if !existed {
		// Match gocui semantics: a freshly-created view returns
		// ErrUnknownView as the "new view created" sentinel.
		return nil, gocui.ErrUnknownView
	}
	return nil, nil
}

func (r *RecorderGuiDriver) SetKeybinding(viewName string, key types.Key, mod types.Modifier, handler func() error) error {
	r.mu.Lock()
	r.bindings = append(r.bindings, KbRecord{View: viewName, Key: key, Mod: mod, Handler: handler})
	r.mu.Unlock()
	return nil
}

func (r *RecorderGuiDriver) SetViewClickBinding(b *types.ViewMouseBinding) error {
	if b == nil {
		return nil
	}
	r.mu.Lock()
	r.clicks = append(r.clicks, ClickRecord{View: b.ViewName, Key: b.Key, Mod: b.Modifier, Handler: b.Handler})
	r.mu.Unlock()
	return nil
}

// errMouseNotFound is returned by FeedMouse when no recorded click
// binding matches the requested (view, key, mod).
var errMouseNotFound = errors.New("recorder: no mouse binding for (view, key, mod)")

// FeedMouse locates the first recorded click binding matching (view, key,
// mod) and invokes its handler with opts. Returns errMouseNotFound when
// no match exists or the matching record has a nil handler.
func (r *RecorderGuiDriver) FeedMouse(view string, key types.KeyName, mod types.Modifier, opts types.ViewMouseBindingOpts) error {
	r.mu.Lock()
	var handler func(types.ViewMouseBindingOpts) error
	for i := range r.clicks {
		c := r.clicks[i]
		if c.View == view && c.Key == key && c.Mod == mod {
			handler = c.Handler
			break
		}
	}
	r.mu.Unlock()
	if handler == nil {
		return errMouseNotFound
	}
	return handler(opts)
}

// AllClicks returns a defensive copy of the recorded click bindings.
func (r *RecorderGuiDriver) AllClicks() []ClickRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ClickRecord, len(r.clicks))
	copy(out, r.clicks)
	return out
}

func (r *RecorderGuiDriver) Update(fn func() error) {
	r.mu.Lock()
	r.UpdateCalls++
	r.mu.Unlock()
	if fn == nil {
		return
	}
	err := fn()
	if err != nil {
		r.mu.Lock()
		r.updateErrors = append(r.updateErrors, err)
		r.mu.Unlock()
	}
}

func (r *RecorderGuiDriver) UpdateContentOnly(fn func() error) {
	r.mu.Lock()
	r.ContentOnlyCalls++
	r.mu.Unlock()
	if fn == nil {
		return
	}
	err := fn()
	if err != nil {
		r.mu.Lock()
		r.updateErrors = append(r.updateErrors, err)
		r.mu.Unlock()
	}
}

// UpdateErrors returns a defensive copy of every non-nil error returned
// by Update / UpdateContentOnly closures. The real gocui MainLoop kills
// the TUI when a queued closure returns a non-nil error, so any entry
// here is a real production-affecting bug that should fail the test.
func (r *RecorderGuiDriver) UpdateErrors() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]error, len(r.updateErrors))
	copy(out, r.updateErrors)
	return out
}

func (r *RecorderGuiDriver) SetCurrentView(viewName string) (types.View, error) {
	r.mu.Lock()
	r.SetCurrentViewLog = append(r.SetCurrentViewLog, viewName)
	r.mu.Unlock()
	return nil, nil
}

func (r *RecorderGuiDriver) SetViewOnTop(viewName string) (types.View, error) {
	r.mu.Lock()
	r.SetViewOnTopCalls = append(r.SetViewOnTopCalls, viewName)
	_, ok := r.views[viewName]
	r.mu.Unlock()
	if !ok {
		return nil, gocui.ErrUnknownView
	}
	return nil, nil
}

func (r *RecorderGuiDriver) ViewByName(name string) (types.View, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.views[name]; !ok {
		return nil, gocui.ErrUnknownView
	}
	return nil, nil
}

func (r *RecorderGuiDriver) DeleteView(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DeleteViews = append(r.DeleteViews, name)
	if _, ok := r.views[name]; !ok {
		return gocui.ErrUnknownView
	}
	delete(r.views, name)
	return nil
}

func (r *RecorderGuiDriver) SetManager(managers ...types.Manager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(managers) == 0 {
		r.manager = nil
		return
	}
	r.manager = managers[0]
}

// Manager returns the manager installed via SetManager (or nil).
func (r *RecorderGuiDriver) Manager() types.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager
}

func (r *RecorderGuiDriver) MainLoop() error { return nil }

func (r *RecorderGuiDriver) Close() error { return nil }
