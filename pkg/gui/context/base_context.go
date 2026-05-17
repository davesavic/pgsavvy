package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// BaseContext is the concrete root for every Context in the dbsavvy TUI.
// Concrete contexts embed BaseContext and override the lifecycle hooks
// they care about. Lifecycle hooks default to no-op returning nil so
// embedding contexts only declare what they need (DESIGN.md §8).
//
// AddKeybindingsFn appends to an internal slice; GetKeybindings walks the
// slice and concatenates the returned bindings so the LAST attached
// controller wins on key collisions. Mouse bindings are not collected at
// this layer; concrete contexts that publish mouse bindings override
// GetMouseKeybindings directly.
//
// BaseContext is NOT goroutine-safe; all AddKeybindingsFn / GetKeybindings
// calls happen on the MainLoop.
type BaseContext struct {
	key        types.ContextKey
	viewName   string
	windowName string
	kind       types.ContextKind

	// keybindingsFns are appended in attachment order. GetKeybindings
	// iterates the slice in order and concatenates; later entries
	// overwrite earlier ones on key collision (last-attached-wins).
	keybindingsFns []types.KeybindingsFn
}

// BaseContextOpts is the constructor argument bundle for BaseContext.
// Using a struct keeps the call sites readable in setup.go where 17
// contexts are instantiated.
type BaseContextOpts struct {
	Key        types.ContextKey
	ViewName   string
	WindowName string
	Kind       types.ContextKind
}

// NewBaseContext constructs a BaseContext from the supplied options.
// WindowName defaults to ViewName when left empty — most contexts share
// the two identifiers.
func NewBaseContext(opts BaseContextOpts) BaseContext {
	if opts.WindowName == "" {
		opts.WindowName = opts.ViewName
	}
	return BaseContext{
		key:        opts.Key,
		viewName:   opts.ViewName,
		windowName: opts.WindowName,
		kind:       opts.Kind,
	}
}

// GetKey returns the Context's stable identity.
func (b *BaseContext) GetKey() types.ContextKey { return b.key }

// GetViewName returns the name of the gocui view this Context renders to.
// May be the empty string for GLOBAL_CONTEXT (no view).
func (b *BaseContext) GetViewName() string { return b.viewName }

// GetWindowName returns the layout window slot this Context occupies.
func (b *BaseContext) GetWindowName() string { return b.windowName }

// GetKind returns the Context's kind classification.
func (b *BaseContext) GetKind() types.ContextKind { return b.kind }

// HandleFocus is the default no-op focus hook. Concrete contexts override.
func (b *BaseContext) HandleFocus(_ types.OnFocusOpts) error { return nil }

// HandleFocusLost is the default no-op focus-lost hook.
func (b *BaseContext) HandleFocusLost(_ types.OnFocusLostOpts) error { return nil }

// HandleRender is the default no-op render hook.
func (b *BaseContext) HandleRender() error { return nil }

// HandleRenderToMain is the default no-op for rendering preview content
// to the main pane (used by side contexts that mirror selection into the
// main pane in later epics).
func (b *BaseContext) HandleRenderToMain() error { return nil }

// HandleQuit is the default no-op quit hook. Concrete contexts that hold
// resources (e.g. subscriptions) override to release them.
func (b *BaseContext) HandleQuit() error { return nil }

// NeedsRerenderOnHeightChange reports whether the Context wants HandleRender
// re-invoked on terminal height changes. Default false; concrete contexts
// override.
func (b *BaseContext) NeedsRerenderOnHeightChange() bool { return false }

// NeedsRerenderOnWidthChange reports whether the Context wants HandleRender
// re-invoked on terminal width changes. Default false.
func (b *BaseContext) NeedsRerenderOnWidthChange() bool { return false }

// AddKeybindingsFn appends a controller's keybinding contributor to this
// Context. Per AC, a nil fn is silently dropped (no-op) so controller
// registration code does not need to guard for the empty case.
func (b *BaseContext) AddKeybindingsFn(fn types.KeybindingsFn) {
	if fn == nil {
		return
	}
	b.keybindingsFns = append(b.keybindingsFns, fn)
}

// GetKeybindings returns the union of every attached controller's
// bindings, concatenated in attachment order. The runtime resolves
// duplicate Key+ViewName tuples by last-write-wins when registering with
// the driver, so the documented last-attached-wins semantics hold without
// any de-duplication here.
//
// Returns a non-nil empty slice when no controllers are attached.
func (b *BaseContext) GetKeybindings(opts types.KeybindingsOpts) []*types.ChordBinding {
	out := make([]*types.ChordBinding, 0)
	for _, fn := range b.keybindingsFns {
		out = append(out, fn(opts)...)
	}
	return out
}

// GetMouseKeybindings returns no bindings by default. Concrete contexts
// that need mouse handling override this method.
func (b *BaseContext) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}
