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

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ErrNoEditor is returned by FeedChord when no master Editor has been
// installed for the requested view.
var ErrNoEditor = errors.New("testfake: no master editor installed for view")

// ErrEditorNotDispatcher is returned by FeedChord when the installed
// editor does not satisfy the Dispatcher side-channel interface (i.e.
// is not a *orchestrator.masterEditor).
var ErrEditorNotDispatcher = errors.New("testfake: installed editor does not implement keys.DispatchResult Dispatcher")

// chordDispatcher mirrors orchestrator.Dispatcher. Declared locally so
// the testfake package does not depend on the orchestrator package
// (which would introduce an import cycle: orchestrator already imports
// testfake from other tests).
type chordDispatcher interface {
	Dispatch(v *gocui.View, key gocui.Key) (keys.DispatchResult, error)
}

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

	editors map[string]gocui.Editor

	views   map[string]*viewState
	manager types.Manager
}

type viewState struct {
	buf []byte
}

// NewRecorderGuiDriver builds an empty recorder. Default size 80x24.
func NewRecorderGuiDriver() *RecorderGuiDriver {
	return &RecorderGuiDriver{
		width:   80,
		height:  24,
		views:   map[string]*viewState{},
		editors: map[string]gocui.Editor{},
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

// SetMasterEditor records ed as the master Editor for view. A second
// call for the same view replaces the first (idempotent — matches the
// production driver's behaviour of overwriting v.Editor).
func (r *RecorderGuiDriver) SetMasterEditor(view string, ed gocui.Editor) error {
	r.mu.Lock()
	r.editors[view] = ed
	r.mu.Unlock()
	return nil
}

// InstalledEditors returns a defensive copy of the editors recorded by
// SetMasterEditor. Mutating the returned map does not affect the
// recorder.
func (r *RecorderGuiDriver) InstalledEditors() map[string]gocui.Editor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]gocui.Editor, len(r.editors))
	for k, v := range r.editors {
		out[k] = v
	}
	return out
}

// FeedChord drives seq through the master Editor installed for view by
// type-asserting to the chordDispatcher interface. Returns
// ErrNoEditor when no editor is installed, ErrEditorNotDispatcher
// when the installed editor cannot be driven directly, or the final
// DispatchResult of the last key in seq.
//
// seq is empty → returns (keys.FellThrough, nil) with no editor call.
func (r *RecorderGuiDriver) FeedChord(view string, seq []keys.Key) (keys.DispatchResult, error) {
	r.mu.Lock()
	ed, ok := r.editors[view]
	r.mu.Unlock()
	if !ok {
		return keys.FellThrough, ErrNoEditor
	}
	disp, isDisp := ed.(chordDispatcher)
	if !isDisp {
		return keys.FellThrough, ErrEditorNotDispatcher
	}
	if len(seq) == 0 {
		return keys.FellThrough, nil
	}
	var (
		result keys.DispatchResult
		err    error
	)
	for _, k := range seq {
		gk, gmod, encErr := encodeKeyForFeed(k)
		if encErr != nil {
			return keys.FellThrough, encErr
		}
		_ = gmod // gocui folds mod into the Key (NewKeyStrMod / NewKey).
		result, err = disp.Dispatch(nil, gk)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// encodeKeyForFeed converts a chord Key back to a gocui.Key for
// feeding through the master editor. It reuses the same forward
// mapping helpers RegisterChord uses, then folds the modifier back
// into the Key via gocui.NewKeyStrMod (rune) / gocui.NewKey (special).
func encodeKeyForFeed(k keys.Key) (gocui.Key, gocui.Modifier, error) {
	// Bare rune (no special).
	if k.Special == keys.KeyNone {
		gmod := chordModForFeed(k.Mod)
		if k.Mod == 0 {
			return gocui.NewKeyRune(k.Code), 0, nil
		}
		return gocui.NewKeyStrMod(string(k.Code), gmod), gmod, nil
	}
	name, err := keys.SpecialKeyToGocui(k.Special)
	if err != nil {
		return gocui.Key{}, 0, err
	}
	gmod := chordModForFeed(k.Mod)
	return gocui.NewKey(name, "", gmod), gmod, nil
}

// chordModForFeed translates keys.Modifier bits to gocui.Modifier.
// Kept local so testfake does not need to re-export the (private)
// chordModifierToGocui in pkg/gui/keys.
func chordModForFeed(m keys.Modifier) gocui.Modifier {
	var out gocui.Modifier
	if m&keys.ModCtrl != 0 {
		out |= gocui.ModCtrl
	}
	if m&keys.ModAlt != 0 {
		out |= gocui.ModAlt
	}
	if m&keys.ModShift != 0 {
		out |= gocui.ModShift
	}
	if m&keys.ModMeta != 0 {
		out |= gocui.ModAlt
	}
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

func (r *RecorderGuiDriver) SetContent(viewName string, str string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[viewName]
	if !ok {
		return gocui.ErrUnknownView
	}
	v.buf = append(v.buf[:0], []byte(str)...)
	return nil
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
