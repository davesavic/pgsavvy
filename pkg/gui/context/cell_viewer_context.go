package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
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

	var visibleLines []string
	if c.wrap {
		w := grid.WrapWindow(sanitized, width, c.scroll[1], bodyHeight)
		c.totalWrappedLines = w.Lines()
		visibleLines = w.Slice()
	} else {
		lines := strings.Split(sanitized, "\n")
		c.totalWrappedLines = len(lines)
		start := c.scroll[1]
		end := start + bodyHeight
		if start < 0 {
			start = 0
		}
		if end > len(lines) {
			end = len(lines)
		}
		visibleLines = lines[start:end]
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
