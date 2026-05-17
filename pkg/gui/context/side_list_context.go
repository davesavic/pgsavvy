package context

// SideListContext is the shared base for the five SIDE_CONTEXT panels
// (Connections, Schemas, Tables, Columns, Indexes). It carries the
// cursor index and the row slice; concrete side contexts swap their row
// type via SetItems and read selection via SelectedItem.
//
// SideListContext stores items as []any rather than a generic type
// parameter to keep the IBaseContext interface footprint flat (Go 1.25
// supports generics but the rest of pkg/gui/types and the test fakes
// treat contexts uniformly via IBaseContext, which itself is not
// generic). Concrete contexts type-assert in their HandleRender pass.
type SideListContext struct {
	BaseContext

	deps Deps

	cursor int
	items  []any
}

// Deps is the per-context view of types.ContextTreeDeps. Aliased locally
// so concrete contexts don't repeat the long type name; it's
// always-by-value because the hook fields inside are themselves pointers
// (func values, interface).
type Deps = depsAlias

// NewSideListContext constructs a SideListContext with a BaseContext
// already wired. Concrete side contexts embed by value and forward to
// this constructor.
func NewSideListContext(base BaseContext, deps Deps) SideListContext {
	return SideListContext{
		BaseContext: base,
		deps:        deps,
	}
}

// SetItems replaces the row slice. Items beyond the new length silently
// clamp the cursor; callers refreshing the data after a delete do not
// need to reset the cursor manually.
func (s *SideListContext) SetItems(items []any) {
	s.items = items
	if s.cursor >= len(items) {
		if len(items) == 0 {
			s.cursor = 0
		} else {
			s.cursor = len(items) - 1
		}
	}
}

// Items returns the current row slice (read-only view; do not mutate).
func (s *SideListContext) Items() []any { return s.items }

// Cursor returns the current cursor index. May be 0 when items is empty.
func (s *SideListContext) Cursor() int { return s.cursor }

// SetCursor moves the cursor, clamping into the valid range. A move
// against an empty list snaps to 0.
func (s *SideListContext) SetCursor(i int) {
	if len(s.items) == 0 {
		s.cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(s.items) {
		i = len(s.items) - 1
	}
	s.cursor = i
}

// SelectedItem returns the item under the cursor, or nil when the list
// is empty.
func (s *SideListContext) SelectedItem() any {
	if len(s.items) == 0 || s.cursor < 0 || s.cursor >= len(s.items) {
		return nil
	}
	return s.items[s.cursor]
}
