package context

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// SchemasContext renders the schema list in the left-rail SCHEMAS slot.
// The ShowHiddenMode flag is a transient view-state bit toggled by the
// schemas helper (T5) via the show/hide leader bindings; the
// helper reads/writes it through the accessors below so the context
// stays the single source of truth for its own UI state.
type SchemasContext struct {
	SideListContext

	// showHiddenMode mirrors the H/U/leader-H toggle from T5. When true,
	// HandleRender (introduced incrementally by T5) should include
	// hidden-flagged schemas in the row list. Stored as atomic.Bool so
	// concurrent H/U toggles from the helper layer remain race-clean
	// without forcing callers through a mutex.
	showHiddenMode atomic.Bool
}

// NewSchemasContext builds a SchemasContext bound to the SCHEMAS key and
// view.
func NewSchemasContext(base BaseContext, deps Deps) *SchemasContext {
	ctx := &SchemasContext{
		SideListContext: NewSideListContext(base, deps),
	}
	ctx.SetRailNameAccessor(func(it any) string { return grid.SanitizeCellEscapes(schemaName(it)) })
	ctx.SetRailVisible(ctx.isRowVisible)
	return ctx
}

// GetShowHiddenMode reports whether the show-hidden toggle is active.
func (s *SchemasContext) GetShowHiddenMode() bool { return s.showHiddenMode.Load() }

// SetShowHiddenMode flips the show-hidden toggle. Called by the schemas
// helper after H/U/leader-H.
func (s *SchemasContext) SetShowHiddenMode(v bool) { s.showHiddenMode.Store(v) }

// hiddenSchemaSet returns the set of schema names hidden for the active
// connection, or nil when show-hidden mode is on or nothing is hidden. This
// is the single source of truth shared by renderRows and isRowVisible so the
// match set and the rendered rows cannot diverge.
func (s *SchemasContext) hiddenSchemaSet() map[string]struct{} {
	if s.showHiddenMode.Load() || s.deps.HiddenSchemasForActiveConn == nil {
		return nil
	}
	names := s.deps.HiddenSchemasForActiveConn()
	if len(names) == 0 {
		return nil
	}
	hidden := make(map[string]struct{}, len(names))
	for _, n := range names {
		hidden[n] = struct{}{}
	}
	return hidden
}

// isRowVisible reports whether the schema row at raw index i survives the
// hidden filter — the SAME set renderRows uses, so the matcher's match set
// and the rendered rows agree.
func (s *SchemasContext) isRowVisible(i int) bool {
	if i < 0 || i >= len(s.items) {
		return false
	}
	name := schemaName(s.items[i])
	if name == "" {
		return true
	}
	_, hidden := s.hiddenSchemaSet()[name]
	return !hidden
}

// HandleRender writes the schema-row text into the SCHEMAS view each
// frame. Mirrors ConnectionsContext.HandleRender: cursor row gets a
// "> " marker, other rows get "  " so columns line up. populateSchemasRail
// feeds Items; without this method the rail stayed blank
// after a successful connect.
func (s *SchemasContext) HandleRender() error {
	deps := s.deps
	viewName := s.GetViewName()
	body := s.renderRows()
	if body == "" {
		body = railEmptyPlaceholder(deps, s.GetKey())
	}
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	scrollSideRailIntoView(deps, viewName, s.cursor)
	return nil
}

func (s *SchemasContext) renderRows() string {
	if len(s.items) == 0 {
		return ""
	}
	// Runtime-hidden filter: when showHiddenMode is false (default), skip
	// items whose name is in AppState.HiddenSchemas[activeConnID]. When
	// true, render everything so the user can see and unhide entries. The
	// set is sourced from hiddenSchemaSet, the same helper isRowVisible
	// (and thus the matcher) uses, so render and matches cannot diverge.
	hidden := s.hiddenSchemaSet()

	// Cursor tracks the source items slice (SelectedItem reads
	// items[cursor]). We walk that slice but skip rows that hit the
	// hidden filter; the "> " marker only paints when the cursor lands
	// on a row that survives the filter. j/k clamping is unchanged.
	// dim items when the session is disconnected.
	dim := s.deps.IsDisconnected != nil && s.deps.IsDisconnected()

	var b strings.Builder
	for i, item := range s.items {
		name := schemaName(item)
		if name != "" {
			if _, skip := hidden[name]; skip {
				continue
			}
		}
		marker := "  "
		if i == s.cursor {
			marker = "> "
		}
		if name == "" {
			if dim {
				fmt.Fprintf(&b, "%s\x1b[2m%v\x1b[0m\n", marker, item)
			} else {
				fmt.Fprintf(&b, "%s%v\n", marker, item)
			}
			continue
		}
		display := s.nameOf(item)
		spans := s.highlightSpansForRow(i)
		fmt.Fprintf(&b, "%s%s\n", marker, renderRailName(display, dim, spans))
	}
	return b.String()
}

func schemaName(item any) string {
	switch v := item.(type) {
	case models.Schema:
		return v.Name
	case *models.Schema:
		if v == nil {
			return ""
		}
		return v.Name
	}
	return ""
}
