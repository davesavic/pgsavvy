package context

import (
	"fmt"
	"strings"

	"github.com/rivo/uniseg"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/textcells"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

type CellViewerContext struct {
	BaseContext

	deps Deps

	active bool

	originalValue any
	column        models.ColumnMeta
	primaryKey    []any

	colname   string
	typename  string
	byteCount int
	lineCount int

	wrap   bool
	pretty bool

	scroll [2]int

	view              types.View
	totalWrappedLines int
}

func NewCellViewerContext(base BaseContext, deps Deps) *CellViewerContext {
	return &CellViewerContext{
		BaseContext: base,
		deps:        deps,
		wrap:        true,
		pretty:      true,
	}
}

func CellViewerKey() types.ContextKey { return types.CELL_VIEWER }

func (c *CellViewerContext) HandleFocus(_ types.OnFocusOpts) error {
	return nil
}

func (c *CellViewerContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	c.view = nil
	if c.deps.GuiDriver != nil {
		c.deps.GuiDriver.SetCaretEnabled(false)
	}
	return nil
}

func (c *CellViewerContext) NeedsRerenderOnWidthChange() bool { return true }

func (c *CellViewerContext) CursorXY() (int, int, bool) {
	return 0, 0, false
}

func (c *CellViewerContext) SetView(v types.View) { c.view = v }

func (c *CellViewerContext) Open(originalValue any, column models.ColumnMeta, primaryKey []any) {
	c.active = true
	c.originalValue = originalValue
	c.column = column
	if len(primaryKey) == 0 {
		c.primaryKey = nil
	} else {
		c.primaryKey = append([]any(nil), primaryKey...)
	}

	c.colname = column.Name
	c.typename = column.TypeName

	plain := grid.FormatViewerBodyPlain(originalValue, column, true)
	c.byteCount = len(plain)
	c.lineCount = strings.Count(plain, "\n") + 1
	if plain == grid.ViewerCellNULL || plain == grid.ViewerCellEmpty {
		c.lineCount = 1
	}

	c.wrap = true
	c.pretty = true
	c.scroll = [2]int{0, 0}
	c.totalWrappedLines = 0
}

func (c *CellViewerContext) Close() {
	c.active = false
	c.originalValue = nil
	c.column = models.ColumnMeta{}
	c.primaryKey = nil
	c.view = nil
	c.totalWrappedLines = 0
}

func (c *CellViewerContext) Active() bool { return c.active }

func (c *CellViewerContext) OriginalValue() any { return c.originalValue }

func (c *CellViewerContext) Column() models.ColumnMeta { return c.column }

func (c *CellViewerContext) PrimaryKey() []any {
	if len(c.primaryKey) == 0 {
		return nil
	}
	out := make([]any, len(c.primaryKey))
	copy(out, c.primaryKey)
	return out
}

func (c *CellViewerContext) ScrollX() int           { return c.scroll[0] }
func (c *CellViewerContext) ScrollY() int           { return c.scroll[1] }
func (c *CellViewerContext) TotalWrappedLines() int { return c.totalWrappedLines }

func (c *CellViewerContext) SetScrollX(x int) {
	if x < 0 {
		x = 0
	}
	c.scroll[0] = x
}

func (c *CellViewerContext) SetScrollY(y int) {
	if y < 0 {
		y = 0
	}
	c.scroll[1] = y
}

func (c *CellViewerContext) Scroll(dx, dy int) {
	c.SetScrollX(c.scroll[0] + dx)
	c.SetScrollY(c.scroll[1] + dy)
}

func (c *CellViewerContext) ToggleWrap()   { c.wrap = !c.wrap }
func (c *CellViewerContext) TogglePretty() { c.pretty = !c.pretty }
func (c *CellViewerContext) Wrap() bool    { return c.wrap }
func (c *CellViewerContext) Pretty() bool  { return c.pretty }

func (c *CellViewerContext) Colname() string  { return c.colname }
func (c *CellViewerContext) Typename() string { return c.typename }
func (c *CellViewerContext) ByteCount() int   { return c.byteCount }
func (c *CellViewerContext) LineCount() int   { return c.lineCount }

func (c *CellViewerContext) GetTitle() string {
	return c.buildTitle()
}

func (c *CellViewerContext) HandleRender() error {
	if !c.active {
		return nil
	}

	width := 80
	height := 24
	if c.view != nil {
		if w := c.view.InnerWidth(); w > 0 {
			width = w
		}
		if h := c.view.InnerHeight(); h > 0 {
			height = h
		}
	}

	bodyHeight := height - 1
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	body, _ := grid.FormatViewerBody(c.originalValue, c.column, c.pretty)
	sanitized := grid.SanitizeCellEscapes(body)
	tabWidth := 4
	if c.view != nil && c.view.TabWidth > 0 {
		tabWidth = c.view.TabWidth
	}
	var measure cellViewerWidthMeasure

	var visibleLines []string
	if c.wrap {
		start := c.scroll[1]
		visibleLines, c.totalWrappedLines = measure.wrappedWindow(sanitized, tabWidth, width, start, bodyHeight)
		if clamped := c.verticalWindowStart(bodyHeight); clamped != start {
			visibleLines, _ = measure.wrappedWindow(sanitized, tabWidth, width, clamped, bodyHeight)
		}
	} else {
		maxWidth := 0
		c.totalWrappedLines = 0
		for line := range strings.SplitSeq(sanitized, "\n") {
			maxWidth = max(maxWidth, measure.lineWidth(line, tabWidth))
			c.totalWrappedLines++
		}
		maxX := max(maxWidth-width, 0)
		x := min(c.scroll[0], maxX)
		// Keep X across temporary no-scroll frames, as with the vertical window.
		if maxX > 0 {
			c.scroll[0] = x
		}
		start := c.verticalWindowStart(bodyHeight)
		row := 0
		for line := range strings.SplitSeq(sanitized, "\n") {
			if row >= start {
				visibleLines = append(visibleLines, measure.horizontalWindow(expandCellViewerTabs(line, tabWidth), x, width))
				if len(visibleLines) == bodyHeight {
					break
				}
			}
			row++
		}
	}

	var highlightedLines []string
	for _, line := range visibleLines {
		highlightedLines = append(highlightedLines, highlight.HighlightJSON(line))
	}

	visibleBody := strings.Join(highlightedLines, "\n")

	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, visibleBody)
	})
	return nil
}

func (c *CellViewerContext) verticalWindowStart(bodyHeight int) int {
	maxY := max(c.totalWrappedLines-bodyHeight, 0)
	start := min(c.scroll[1], maxY)
	// Preserve the stored offset on a temporary no-scroll frame during resize.
	if maxY > 0 {
		c.scroll[1] = start
	}
	return start
}

type cellViewerWidthMeasure struct{ textcells.Measure }

func (m *cellViewerWidthMeasure) lineWidth(line string, tabWidth int) int {
	if width, ascii := asciiCellViewerWidth(line, tabWidth); ascii {
		return width
	}
	width, cells := 0, 0
	clusters := uniseg.NewGraphemes(line)
	for clusters.Next() {
		if clusters.Str() == "\t" {
			spaces := tabWidth - cells%tabWidth
			width += spaces
			cells += spaces
		} else {
			width += m.ClusterWidth(clusters.Str())
			cells++
		}
	}
	return width
}

func (m *cellViewerWidthMeasure) horizontalWindow(line string, offset, width int) string {
	if _, ascii := asciiCellViewerWidth(line, 4); ascii {
		start := min(offset, len(line))
		return line[start:min(start+width, len(line))]
	}
	var b strings.Builder
	column := 0
	clusters := uniseg.NewGraphemes(line)
	for clusters.Next() {
		start := column
		cellWidth := m.ClusterWidth(clusters.Str())
		column += cellWidth
		if column <= offset {
			continue
		}
		if column > offset+width {
			break // Never draw a partial grapheme over the right border.
		}
		if start < offset {
			b.WriteString(strings.Repeat(" ", column-offset))
		} else {
			textcells.WriteCluster(&b, clusters.Str(), cellWidth)
		}
	}
	return b.String()
}

func (m *cellViewerWidthMeasure) wrappedWindow(body string, tabWidth, width, offset, height int) ([]string, int) {
	if body == "" {
		return nil, 0
	}
	var visible []string
	var b strings.Builder
	total, used := 0, 0
	flush := func() {
		if total >= offset && total-offset < height {
			visible = append(visible, b.String())
		}
		total++
		b.Reset()
		used = 0
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
		if lineWidth, ascii := asciiCellViewerWidth(line, tabWidth); ascii {
			rows := max((lineWidth+width-1)/width, 1)
			first := max(offset-total, 0)
			last := min(offset+height-total, rows)
			if first < last {
				line = expandCellViewerTabs(line, tabWidth)
				for row := first; row < last; row++ {
					start := row * width
					visible = append(visible, line[start:min(start+width, len(line))])
				}
			}
			total += rows
			continue
		}
		line = expandCellViewerTabs(line, tabWidth)
		clusters := uniseg.NewGraphemes(line)
		for clusters.Next() {
			cellWidth := m.ClusterWidth(clusters.Str())
			if used+cellWidth > width && used > 0 {
				flush()
			}
			if cellWidth > width {
				if total >= offset && total-offset < height {
					b.WriteString(strings.Repeat(" ", width))
				}
				used = width
			} else {
				if total >= offset && total-offset < height {
					textcells.WriteCluster(&b, clusters.Str(), cellWidth)
				}
				used += cellWidth
			}
		}
		flush()
	}
	return visible, total
}

func asciiCellViewerWidth(line string, tabWidth int) (int, bool) {
	width := 0
	for i := range len(line) {
		switch c := line[i]; {
		case c >= 0x80:
			return 0, false
		case c == '\t':
			width += tabWidth - width%tabWidth
		default:
			width++
		}
	}
	return width, true
}

func expandCellViewerTabs(line string, tabWidth int) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var b strings.Builder
	cells := 0
	clusters := uniseg.NewGraphemes(line)
	for clusters.Next() {
		if clusters.Str() == "\t" {
			// gocui tab stops count grapheme cells, not their display widths.
			spaces := tabWidth - cells%tabWidth
			b.WriteString(strings.Repeat(" ", spaces))
			cells += spaces
		} else {
			b.WriteString(clusters.Str())
			cells++
		}
	}
	return b.String()
}

func (c *CellViewerContext) buildTitle() string {
	var sb strings.Builder
	sb.WriteString(c.colname)
	sb.WriteString(" :: ")
	sb.WriteString(c.typename)
	fmt.Fprintf(&sb, " (%d bytes . %d lines)", c.byteCount, c.lineCount)

	if c.wrap {
		sb.WriteString(" [wrap]")
	} else {
		sb.WriteString(" [nowrap]")
	}

	if c.pretty {
		sb.WriteString(" [pretty]")
	} else {
		sb.WriteString(" [raw]")
	}

	plain := grid.FormatViewerBodyPlain(c.originalValue, c.column, c.pretty)
	switch plain {
	case grid.ViewerCellNULL:
		sb.WriteString(" [ NULL ]")
	case grid.ViewerCellEmpty:
		sb.WriteString(" [ empty ]")
	}

	return sb.String()
}
