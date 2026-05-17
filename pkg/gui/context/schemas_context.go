package context

import "sync/atomic"

// SchemasContext renders the schema list in the left-rail SCHEMAS slot.
// The ShowHiddenMode flag is a transient view-state bit toggled by the
// schemas helper (T5, enn.6) via the show/hide leader bindings; the
// helper reads/writes it through the accessors below so the context
// stays the single source of truth for its own UI state.
type SchemasContext struct {
	SideListContext

	// showHiddenMode mirrors the H/U/leader-H toggle from T5. When true,
	// HandleRender (introduced incrementally by T5) should include
	// hidden-flagged schemas in the row list. Stored as atomic.Bool so
	// concurrent H/U toggles from the helper layer remain race-clean
	// without forcing callers through a mutex (enn.6 AC).
	showHiddenMode atomic.Bool
}

// NewSchemasContext builds a SchemasContext bound to the SCHEMAS key and
// view.
func NewSchemasContext(base BaseContext, deps Deps) *SchemasContext {
	return &SchemasContext{
		SideListContext: NewSideListContext(base, deps),
	}
}

// GetShowHiddenMode reports whether the show-hidden toggle is active.
func (s *SchemasContext) GetShowHiddenMode() bool { return s.showHiddenMode.Load() }

// SetShowHiddenMode flips the show-hidden toggle. Called by the schemas
// helper after H/U/leader-H.
func (s *SchemasContext) SetShowHiddenMode(v bool) { s.showHiddenMode.Store(v) }
