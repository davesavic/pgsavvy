package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// StubContext is the placeholder Context implementation used for the six
// deferred Contexts (QUERY_EDITOR, TABLE_DATA_EDITOR, RESULT_GRID, PLAN,
// WHICH_KEY, HISTORY) that ship in later epics. Every method satisfies
// IBaseContext fully; lifecycle hooks return nil with no side effects.
// Kind() returns types.STUB so Layout (T10) filters the view out of the
// SetView pass (DESIGN.md §8 D11 resolution).
//
// StubContext is intentionally minimal: no embedded BaseContext, no
// keybinding slice, no driver reference. This keeps the placeholder
// cheap and makes its "no-op" guarantee obvious from the receiver shape
// rather than from defaults inherited via embedding.
type StubContext struct {
	key      types.ContextKey
	viewName string
}

// NewStubContext builds a StubContext for the given key and view name.
// The window name always matches the view name for stubs (they occupy
// the slot they will eventually render into).
func NewStubContext(key types.ContextKey, viewName string) *StubContext {
	return &StubContext{key: key, viewName: viewName}
}

// GetKey returns the stub Context's stable identity.
func (s *StubContext) GetKey() types.ContextKey { return s.key }

// GetViewName returns the view name the stub reserves for the eventual
// implementation. Layout filters by Kind == STUB before calling SetView
// so the view is never actually created at this stage.
func (s *StubContext) GetViewName() string { return s.viewName }

// GetWindowName returns the same string as GetViewName for stubs.
func (s *StubContext) GetWindowName() string { return s.viewName }

// GetKind always returns types.STUB.
func (s *StubContext) GetKind() types.ContextKind { return types.STUB }

// GetTitle returns the empty string for stubs — Layout filters them out
// of the SetView pass, so the value is never read.
func (s *StubContext) GetTitle() string { return "" }

// HandleFocus is a no-op for stubs.
func (s *StubContext) HandleFocus(_ types.OnFocusOpts) error { return nil }

// HandleFocusLost is a no-op for stubs.
func (s *StubContext) HandleFocusLost(_ types.OnFocusLostOpts) error { return nil }

// HandleRender is a no-op for stubs. Layout skips stubs entirely so this
// method should never actually fire in production wiring — its presence
// satisfies IBaseContext and keeps direct calls from panicking in tests.
func (s *StubContext) HandleRender() error { return nil }

// HandleRenderToMain is a no-op for stubs.
func (s *StubContext) HandleRenderToMain() error { return nil }

// HandleQuit is a no-op for stubs. Does not affect ContextMgr.Pop
// semantics.
func (s *StubContext) HandleQuit() error { return nil }

// NeedsRerenderOnHeightChange returns false — stubs do not render.
func (s *StubContext) NeedsRerenderOnHeightChange() bool { return false }

// NeedsRerenderOnWidthChange returns false — stubs do not render.
func (s *StubContext) NeedsRerenderOnWidthChange() bool { return false }

// AddKeybindingsFn is a no-op for stubs; nothing can be bound to a
// deferred context.
func (s *StubContext) AddKeybindingsFn(_ types.KeybindingsFn) {}

// GetKeybindings returns an empty, non-nil slice for stubs (AC: empty
// slice not nil).
func (s *StubContext) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return []*types.ChordBinding{}
}

// GetMouseKeybindings returns nil for stubs — mouse handling is not
// established until the real context lands.
func (s *StubContext) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}
