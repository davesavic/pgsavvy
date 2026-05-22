package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestTablesContext_HandleRenderWritesRows guards dbsavvy-5iv: the
// TABLES rail must paint its items into the TABLES view through the
// layout pass. Until a populate path lands the rail stays empty by
// content (current behavior), but once Items() is non-empty rendering
// must produce one row per item.
func TestTablesContext_HandleRenderWritesRows(t *testing.T) {
	drv := &captureDriver{}
	base := NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT})
	c := NewTablesContext(base, types.ContextTreeDeps{GuiDriver: drv})
	c.SetItems([]any{
		&models.Table{Name: "users"},
		&models.Table{Name: "orders"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "users") || !strings.Contains(body, "orders") {
		t.Errorf("body = %q, want both row names", body)
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor row = %q, want '> ' prefix", lines[0])
	}
}

// TestEmptyRailRenderClears ensures the empty-rail path on all
// side contexts writes empty content (not stale) so a disconnect
// clears prior data.
func TestEmptyRailRenderClears(t *testing.T) {
	for _, tc := range []struct {
		name   string
		render func(drv *captureDriver) error
	}{
		{"schemas", func(drv *captureDriver) error {
			base := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, ViewName: string(types.SCHEMAS), Kind: types.SIDE_CONTEXT})
			return NewSchemasContext(base, types.ContextTreeDeps{GuiDriver: drv}).HandleRender()
		}},
		{"tables", func(drv *captureDriver) error {
			base := NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT})
			return NewTablesContext(base, types.ContextTreeDeps{GuiDriver: drv}).HandleRender()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drv := &captureDriver{}
			if err := tc.render(drv); err != nil {
				t.Fatalf("HandleRender: %v", err)
			}
			if drv.writes == 0 {
				t.Fatal("empty-list HandleRender did not write; rail would keep stale rows")
			}
			if drv.lastContent != "" {
				t.Errorf("empty-list body = %q, want empty", drv.lastContent)
			}
		})
	}
}
