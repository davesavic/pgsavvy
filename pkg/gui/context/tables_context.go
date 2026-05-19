package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// TablesContext renders the table list in the left-rail TABLES slot. It
// embeds SideListContext for cursor and row management; table fetching
// is supplied by helpers/controllers in later epics.
type TablesContext struct {
	SideListContext
}

// NewTablesContext builds a TablesContext bound to the TABLES key and view.
func NewTablesContext(base BaseContext, deps Deps) *TablesContext {
	return &TablesContext{
		SideListContext: NewSideListContext(base, deps),
	}
}

// HandleRender writes the table-row text into the TABLES view each
// frame. Mirrors ConnectionsContext / SchemasContext. Items is empty
// until a populate path lands (RefreshTables currently discards its
// result — see dbsavvy-5iv notes); the empty branch produces a clean
// blank rail.
func (t *TablesContext) HandleRender() error {
	deps := t.deps
	viewName := t.GetViewName()
	body := t.renderRows()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

func (t *TablesContext) renderRows() string {
	if len(t.items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, item := range t.items {
		marker := "  "
		if i == t.cursor {
			marker = "> "
		}
		name := tableName(item)
		if name == "" {
			fmt.Fprintf(&b, "%s%v\n", marker, item)
			continue
		}
		fmt.Fprintf(&b, "%s%s\n", marker, name)
	}
	return b.String()
}

func tableName(item any) string {
	switch v := item.(type) {
	case *models.Table:
		if v == nil {
			return ""
		}
		return v.Name
	case models.Table:
		return v.Name
	}
	return ""
}
