package context

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

type ChangelogContext struct {
	BaseContext

	deps Deps

	active bool

	content           string
	scroll            [2]int
	totalWrappedLines int
}

func NewChangelogContext(base BaseContext, deps Deps) *ChangelogContext {
	return &ChangelogContext{
		BaseContext: base,
		deps:        deps,
	}
}

func ChangelogKey() types.ContextKey { return types.CHANGELOG }

func (c *ChangelogContext) HandleFocus(_ types.OnFocusOpts) error {
	c.active = true
	return nil
}

func (c *ChangelogContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	c.active = false
	return nil
}

func (c *ChangelogContext) NeedsRerenderOnWidthChange() bool { return true }

func (c *ChangelogContext) Active() bool { return c.active }

func (c *ChangelogContext) ScrollY() int { return c.scroll[1] }

func (c *ChangelogContext) SetScrollY(y int) {
	if y < 0 {
		y = 0
	}
	c.scroll[1] = y
}

func (c *ChangelogContext) TotalWrappedLines() int { return c.totalWrappedLines }

func (c *ChangelogContext) Scroll(dy int) {
	c.SetScrollY(c.scroll[1] + dy)
}

func (c *ChangelogContext) Open(version string) {
	c.active = true
	c.content = grid.SanitizeCellEscapes(c.deps.ReleaseNotesContent)
	c.scroll = [2]int{0, 0}
	c.totalWrappedLines = 0
}

func (c *ChangelogContext) Close() {
	c.active = false
	c.content = ""
	c.totalWrappedLines = 0
}

func (c *ChangelogContext) HandleRender() error {
	if !c.active {
		return nil
	}

	content := c.content
	if content == "" {
		viewName := c.GetViewName()
		writeView(c.deps, func() error {
			return c.deps.GuiDriver.SetContent(viewName, "")
		})
		return nil
	}

	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, content)
	})

	lines := strings.Split(content, "\n")
	c.totalWrappedLines = len(lines)

	return nil
}
