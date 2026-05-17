package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// ConnectionsContext renders the connection picker in the left-rail
// CONNECTIONS slot. It reads its row data via SideListContext.Items() (a
// slice of *models.Connection at runtime) and consults
// deps.EmptyStateHook / deps.PerRowDecorationHook from ContextTreeDeps so
// the helper layer (T6) and the style/presentation layer (T8) can
// contribute behaviour without touching this file.
type ConnectionsContext struct {
	SideListContext
}

// NewConnectionsContext builds a ConnectionsContext bound to the
// CONNECTIONS key and view.
func NewConnectionsContext(base BaseContext, deps Deps) *ConnectionsContext {
	return &ConnectionsContext{
		SideListContext: NewSideListContext(base, deps),
	}
}

// HandleRender writes the picker rows to the CONNECTIONS view. When
// deps.EmptyStateHook reports renderEmpty=true it writes the hint
// instead. Both code paths are silent no-ops when GuiDriver is nil.
func (c *ConnectionsContext) HandleRender() error {
	deps := c.deps
	viewName := c.GetViewName()

	// Empty-state branch: hook decides on the current common.Common; if
	// the hook is missing we fall through to the default row pass.
	if deps.EmptyStateHook != nil {
		// T2 ships with nil common pointer; T6/T10 will plumb the real
		// *common.Common via a richer ContextTreeDeps. The hook contract
		// is nil-safe by spec.
		renderEmpty, hint := deps.EmptyStateHook(nil)
		if renderEmpty {
			writeView(deps, func() error {
				return deps.GuiDriver.SetContent(viewName, hint)
			})
			return nil
		}
	}

	rows := c.renderRows()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, rows)
	})
	return nil
}

// renderRows produces the row text for the current Items slice. Concrete
// row decoration (icon, label, colour swatch) comes from
// deps.PerRowDecorationHook when supplied; absent the hook each row is
// plain "<name>".
func (c *ConnectionsContext) renderRows() string {
	if len(c.items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range c.items {
		conn, ok := item.(*models.Connection)
		if !ok {
			// Unknown row type: render best-effort via fmt so we never
			// silently drop a row.
			fmt.Fprintf(&b, "%v\n", item)
			continue
		}
		if c.deps.PerRowDecorationHook != nil {
			icon, label, _ := c.deps.PerRowDecorationHook(conn)
			if label == "" {
				label = conn.Name
			}
			if icon != "" {
				fmt.Fprintf(&b, "%s %s\n", icon, label)
			} else {
				fmt.Fprintf(&b, "%s\n", label)
			}
			continue
		}
		fmt.Fprintf(&b, "%s\n", conn.Name)
	}
	return b.String()
}
