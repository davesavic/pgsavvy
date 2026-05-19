package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
)

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

// HandleRender writes the index rows into the INDEXES view. Mirrors
// the other SIDE_CONTEXT renderers (dbsavvy-5iv).
func (idx *IndexesContext) HandleRender() error {
	deps := idx.deps
	viewName := idx.GetViewName()
	body := idx.renderRows()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

func (idx *IndexesContext) renderRows() string {
	if len(idx.items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, item := range idx.items {
		marker := "  "
		if i == idx.cursor {
			marker = "> "
		}
		name := indexName(item)
		if name == "" {
			fmt.Fprintf(&b, "%s%v\n", marker, item)
			continue
		}
		fmt.Fprintf(&b, "%s%s\n", marker, name)
	}
	return b.String()
}

func indexName(item any) string {
	switch v := item.(type) {
	case *models.Index:
		if v == nil {
			return ""
		}
		return v.Name
	case models.Index:
		return v.Name
	}
	return ""
}
