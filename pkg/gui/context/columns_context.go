package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
)

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

// HandleRender writes the column rows into the COLUMNS view. Mirrors
// the other SIDE_CONTEXT renderers (dbsavvy-5iv).
func (c *ColumnsContext) HandleRender() error {
	deps := c.deps
	viewName := c.GetViewName()
	body := c.renderRows()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

func (c *ColumnsContext) renderRows() string {
	if len(c.items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, item := range c.items {
		marker := "  "
		if i == c.cursor {
			marker = "> "
		}
		name := columnName(item)
		if name == "" {
			fmt.Fprintf(&b, "%s%v\n", marker, item)
			continue
		}
		fmt.Fprintf(&b, "%s%s\n", marker, name)
	}
	return b.String()
}

func columnName(item any) string {
	switch v := item.(type) {
	case *models.Column:
		if v == nil {
			return ""
		}
		return v.Name
	case models.Column:
		return v.Name
	}
	return ""
}
