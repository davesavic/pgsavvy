package context

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// indexHeader is the fixed header row prepended to the aligned index
// table rendered in the inspect popup's INDEXES leaf.
var indexHeader = []string{"NAME", "FLAGS", "COLUMNS", "METHOD"}

// IndexesContext renders the index list in the left-rail INDEXES slot.
type IndexesContext struct {
	SideListContext
}

// NewIndexesContext builds an IndexesContext bound to the INDEXES key and view.
func NewIndexesContext(base BaseContext, deps Deps) *IndexesContext {
	return &IndexesContext{
		SideListContext: NewSideListContext(base, deps),
	}
}

// HandleRender writes the aligned index table into the INDEXES view: a
// fixed header row followed by one SafeText-sanitized, column-aligned row
// per index, or the "(no indexes)" empty-state. This is the INDEXES leaf
// of the TABLE_INSPECT tabbed popup, rendered via the container.
//
// Unlike the other SIDE_CONTEXT renderers, this performs NO view-origin
// write (no scrollSideRailIntoView/FocusPoint/SetOrigin). Inspect has no
// cursor-move bindings, so the cursor stays 0; writing origin every frame
// would pin it to row 0 and fight applyTableInspectScroll, the single
// intended origin owner (layout.go).
//
// The rail-style disconnected-dim path is intentionally NOT carried over:
// inspect always rendered the panel's aligned table, never the rail-style
// renderRows, so dropping dim is a no-op for inspect.
func (idx *IndexesContext) HandleRender() error {
	deps := idx.deps
	viewName := idx.GetViewName()
	body := renderIndexesTable(idx.items)
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// renderIndexesTable builds the aligned index table (header + rows), or
// the empty-state placeholder when no indexes are present.
func renderIndexesTable(items []any) string {
	rows := make([][]string, 0, len(items)+1)
	for _, it := range items {
		if ix := asIndex(it); ix != nil {
			rows = append(rows, indexCells(ix))
		}
	}
	if len(rows) == 0 {
		return "(no indexes)"
	}
	return alignRows(append([][]string{indexHeader}, rows...))
}

func asIndex(it any) *models.Index {
	switch v := it.(type) {
	case *models.Index:
		return v
	case models.Index:
		return &v
	}
	return nil
}

// indexCells renders a single index as the cells of an aligned row:
// {name, flags, columns, method}. Flags are "PK"/"UNIQUE" (space-joined);
// columns are wrapped in parentheses. Every DB-supplied string passes
// through config.SafeText.
func indexCells(idx *models.Index) []string {
	flags := make([]string, 0, 2)
	if idx.IsPrimary {
		flags = append(flags, "PK")
	}
	if idx.IsUnique {
		flags = append(flags, "UNIQUE")
	}
	cols := ""
	if len(idx.Columns) > 0 {
		safeCols := make([]string, 0, len(idx.Columns))
		for _, col := range idx.Columns {
			safeCols = append(safeCols, config.SafeText(col))
		}
		cols = "(" + strings.Join(safeCols, ", ") + ")"
	}
	return []string{
		config.SafeText(idx.Name),
		strings.Join(flags, " "),
		cols,
		config.SafeText(idx.Method),
	}
}
