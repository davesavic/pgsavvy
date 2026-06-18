package context

import (
	"strings"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// columnHeader is the fixed header row prepended to the aligned column
// table rendered in the inspect popup's COLUMNS leaf.
var columnHeader = []string{"NAME", "TYPE", "NULL", "DEFAULT"}

// ColumnsContext renders the column list in the left-rail COLUMNS slot.
type ColumnsContext struct {
	SideListContext
}

// NewColumnsContext builds a ColumnsContext bound to the COLUMNS key and view.
func NewColumnsContext(base BaseContext, deps Deps) *ColumnsContext {
	return &ColumnsContext{
		SideListContext: NewSideListContext(base, deps),
	}
}

// HandleRender writes the aligned column table into the COLUMNS view:
// a fixed header row followed by one SafeText-sanitized, column-aligned
// row per column, or the "(no columns)" empty-state. This is the COLUMNS
// leaf of the TABLE_INSPECT tabbed popup, rendered via the container.
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
func (c *ColumnsContext) HandleRender() error {
	deps := c.deps
	viewName := c.GetViewName()
	body := renderColumnsTable(c.items)
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// renderColumnsTable builds the aligned column table (header + rows), or
// the empty-state placeholder when no columns are present.
func renderColumnsTable(items []any) string {
	rows := make([][]string, 0, len(items)+1)
	for _, it := range items {
		if col := asColumn(it); col != nil {
			rows = append(rows, columnCells(col))
		}
	}
	if len(rows) == 0 {
		return "(no columns)"
	}
	return alignRows(append([][]string{columnHeader}, rows...))
}

func asColumn(it any) *models.Column {
	switch v := it.(type) {
	case *models.Column:
		return v
	case models.Column:
		return &v
	}
	return nil
}

// columnCells renders a single column as the cells of an aligned row:
// {name, type, null-marker, default}. Every DB-supplied string passes
// through config.SafeText.
func columnCells(c *models.Column) []string {
	null := ""
	if !c.Nullable {
		null = "NOT NULL"
	}
	def := ""
	if c.Default != "" {
		def = "default=" + config.SafeText(c.Default)
	}
	return []string{
		config.SafeText(c.Name),
		config.SafeText(c.DataType),
		null,
		def,
	}
}

// alignRows renders rows as a fixed-width table: each column is padded to
// the widest cell in that column so values line up vertically. Cells are
// separated by two spaces; trailing empty cells produce no padding, so
// lines carry no trailing whitespace. Rows are joined by '\n'. Shared by
// the COLUMNS and INDEXES leaf renderers.
func alignRows(rows [][]string) string {
	widths := columnWidths(rows)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, padRow(row, widths))
	}
	return strings.Join(lines, "\n")
}

// columnWidths returns the max rune width of each column across all rows.
func columnWidths(rows [][]string) []int {
	n := 0
	for _, row := range rows {
		if len(row) > n {
			n = len(row)
		}
	}
	widths := make([]int, n)
	for _, row := range rows {
		for i, cell := range row {
			if w := utf8.RuneCountInString(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

// padRow joins a row's cells with a two-space gap, padding each cell to
// its column width. The last non-empty cell (and any empty cells after
// it) are not padded, so no trailing whitespace is emitted.
func padRow(row []string, widths []int) string {
	last := -1
	for i, cell := range row {
		if cell != "" {
			last = i
		}
	}
	var b strings.Builder
	for i := 0; i <= last; i++ {
		if i > 0 {
			b.WriteString("  ")
		}
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		b.WriteString(cell)
		if i < last {
			if pad := widths[i] - utf8.RuneCountInString(cell); pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
	}
	return b.String()
}
