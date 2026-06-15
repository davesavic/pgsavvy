// Package testfake supplies an in-memory GuiDriver fake used by tests in
// pkg/gui. Recorder semantics: every driver call is appended to an
// in-memory log, and key-binding handlers are invoked verbatim by
// FeedKey. The fake invokes Update/UpdateContentOnly closures inline so
// tests see their side effects without spinning up a real MainLoop.
package testfake

import (
	"errors"
	"maps"
	"sync"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
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

// SetViewCursorCall records one SetViewCursor invocation. Used by the
// COMMAND_LINE caret tests.
type SetViewCursorCall struct {
	View string
	X, Y int
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

	SetViewCalls       []SetViewCall
	SetViewOnTopCalls  []string
	SetCurrentViewLog  []string
	DeleteViews        []string
	UpdateCalls        int
	ContentOnlyCalls   int
	updateErrors       []error
	CaretEnabled       bool
	CaretEnabledLog    []bool
	SetViewCursorCalls []SetViewCursorCall

	bindings []KbRecord
	clicks   []ClickRecord

	editors map[string]gocui.Editor

	views   map[string]*viewState
	manager types.Manager

	// realViewNames opts specific view names into returning a real
	// *gocui.View from SetView (instead of nil). realViews caches the
	// backing view per enabled name so repeated SetView calls return the
	// same handle (and DeleteView evicts it).
	realViewNames map[string]bool
	realViews     map[string]*gocui.View
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
	// Iterate in registration order so the FIRST registered handler for
	// (view, key, mod) wins — faithful to real gocui. gocui's SetKeybinding
	// APPENDS to g.keybindings (gocui gui.go:551) and execKeybindings
	// forward-scans that slice (gocui gui.go:1546), firing the FIRST
	// view-matching handler and returning. So gocui is FIRST-registered-wins
	// for same-view bindings, NOT overwrite/last-wins.
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
	maps.Copy(out, r.editors)
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

// EnableRealView makes SetView(name, ...) return a real *gocui.View
// (with an auto-initialized TextArea) instead of nil, so tests can
// assert TextArea seeding done by the layout. Opt-in per view name;
// names not enabled keep the historical nil-view behavior.
func (r *RecorderGuiDriver) EnableRealView(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.realViewNames == nil {
		r.realViewNames = map[string]bool{}
	}
	r.realViewNames[name] = true
}

// SetRealView injects a pre-built *gocui.View for a name so callers that
// read live view geometry (Dimensions/Origin/Cursor) via ViewByName see
// the supplied handle. Registers the name in the view map too, so
// ViewByName returns the view rather than ErrUnknownView. Test-only seam
// for cursor-anchored popup placement.
func (r *RecorderGuiDriver) SetRealView(name string, v *gocui.View) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.views[name]; !ok {
		r.views[name] = &viewState{}
	}
	if r.realViews == nil {
		r.realViews = map[string]*gocui.View{}
	}
	r.realViews[name] = v
}

// RealView returns the cached *gocui.View created for an enabled name
// (via EnableRealView + a SetView call), or nil if none exists yet.
// Test-only accessor.
func (r *RecorderGuiDriver) RealView(name string) *gocui.View {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.realViews == nil {
		return nil
	}
	return r.realViews[name]
}

func (r *RecorderGuiDriver) SetView(name string, x0, y0, x1, y1 int, overlaps byte) (types.View, error) {
	r.mu.Lock()
	r.SetViewCalls = append(r.SetViewCalls, SetViewCall{Name: name, X0: x0, Y0: y0, X1: x1, Y1: y1, Overlaps: overlaps})
	_, existed := r.views[name]
	if !existed {
		r.views[name] = &viewState{}
	}
	// Opt-in real-view path: when enabled, lazily create a
	// real *gocui.View on first SetView for the name and cache it. First
	// creation returns (view, ErrUnknownView); subsequent calls return
	// (view, nil) — mirrors gocui's "fresh view paired with ErrUnknownView"
	// semantics. Names not enabled keep the historical nil-view behavior.
	if r.realViewNames != nil && r.realViewNames[name] {
		if r.realViews == nil {
			r.realViews = map[string]*gocui.View{}
		}
		v, cached := r.realViews[name]
		if !cached {
			v = gocui.NewView(name, x0, y0, x1, y1, gocui.OutputNormal)
			r.realViews[name] = v
		}
		r.mu.Unlock()
		if !cached {
			return v, gocui.ErrUnknownView
		}
		return v, nil
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
	// Opt-in real-view path: when a real *gocui.View was
	// created for this name, hand it back so callers that read live view
	// geometry (Dimensions/Origin/Cursor) see real values, matching
	// production. Names without a real view keep the historical nil-view
	// behavior.
	if r.realViews != nil {
		if v, ok := r.realViews[name]; ok {
			return v, nil
		}
	}
	return nil, nil
}

func (r *RecorderGuiDriver) DeleteView(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DeleteViews = append(r.DeleteViews, name)
	// Evict any cached real view so the next SetView for an
	// enabled name re-creates a fresh view (returning ErrUnknownView again),
	// matching gocui teardown semantics on pop/re-push.
	if r.realViews != nil {
		delete(r.realViews, name)
	}
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

func (r *RecorderGuiDriver) SetCaretEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.CaretEnabled = enabled
	r.CaretEnabledLog = append(r.CaretEnabledLog, enabled)
}

func (r *RecorderGuiDriver) SetViewCursor(viewName string, x, y int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SetViewCursorCalls = append(r.SetViewCursorCalls, SetViewCursorCall{View: viewName, X: x, Y: y})
	return nil
}

// AllSetViewCursorCalls returns a defensive copy of the SetViewCursor log.
func (r *RecorderGuiDriver) AllSetViewCursorCalls() []SetViewCursorCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SetViewCursorCall, len(r.SetViewCursorCalls))
	copy(out, r.SetViewCursorCalls)
	return out
}

// AllCaretEnabledLog returns a defensive copy of the caret-toggle log
// (every SetCaretEnabled call in order).
func (r *RecorderGuiDriver) AllCaretEnabledLog() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]bool, len(r.CaretEnabledLog))
	copy(out, r.CaretEnabledLog)
	return out
}

func (r *RecorderGuiDriver) MainLoop() error { return nil }

func (r *RecorderGuiDriver) Close() error { return nil }
