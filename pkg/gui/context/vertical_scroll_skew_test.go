package context

import (
	"fmt"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestSchemaRail_VerticalScrollFollowsRenderedRowPastHiddenSchemas is the
// vertical counterpart to the horizontal pan skew fix. renderRows skips
// hidden schemas, so the cursor's raw items index runs ahead of the buffer
// line it actually paints on. scrollSideRailIntoView must focus the PAINTED
// line (RenderedCursorRow), not the raw index — otherwise, once enough hidden
// rows precede the cursor, FocusPoint scrolls past the selected row and the
// "> " marker disappears off the top of the viewport.
func TestSchemaRail_VerticalScrollFollowsRenderedRowPastHiddenSchemas(t *testing.T) {
	v := gocui.NewView("schemas-tables", 0, 0, 20, 10, gocui.OutputNormal)
	drv := &railDeferredDriver{view: v}

	hidden := map[string]struct{}{}
	tree := NewContextTree(types.ContextTreeDeps{
		GuiDriver: drv,
		HiddenSchemasForActiveConn: func() []string {
			out := make([]string, 0, len(hidden))
			for n := range hidden {
				out = append(out, n)
			}
			return out
		},
	})

	rows := make([]any, 30)
	for i := range rows {
		rows[i] = models.Schema{Name: fmt.Sprintf("schema_%02d", i)}
	}
	tree.Schemas.SetItems(rows)

	// Hide the first 10 schemas (schema_00..schema_09) through the same dep
	// renderRows consults, so the rendered buffer holds schema_10..schema_29.
	// The cursor sits on schema_15 (raw index 15) — painted on buffer line 5.
	for i := range 10 {
		hidden[fmt.Sprintf("schema_%02d", i)] = struct{}{}
	}
	tree.Schemas.SetCursor(15)

	wantRow := tree.Schemas.RenderedCursorRow()
	if wantRow != 5 {
		t.Fatalf("precondition: RenderedCursorRow() = %d, want 5", wantRow)
	}

	if err := tree.SchemaRail.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	drv.drain() // run the deferred FocusPoint, like the gocui main loop

	if _, oy := v.Origin(); oy > wantRow {
		t.Fatalf("oy = %d scrolled past the selected row (painted on buffer line %d): "+
			"the selected schema is off the top of the viewport. Pre-fix FocusPoint "+
			"targeted the raw cursor index (15) instead of the rendered row.", oy, wantRow)
	}
}
