package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newTestIndexes(drv types.GuiDriver) *IndexesContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.INDEXES,
		ViewName: string(types.INDEXES),
		Kind:     types.SIDE_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewIndexesContext(base, deps)
}

// TestIndexesContext_HandleRenderAlignedTable asserts the leaf renders
// the aligned index table: a "NAME ... FLAGS ... COLUMNS ... METHOD"
// header followed by one aligned row per index.
func TestIndexesContext_HandleRenderAlignedTable(t *testing.T) {
	drv := &captureDriver{}
	c := newTestIndexes(drv)
	c.SetItems([]any{
		models.Index{Name: "pk_users", Columns: []string{"id"}, IsPrimary: true, IsUnique: true, Method: "btree"},
		&models.Index{Name: "idx_email", Columns: []string{"email"}, Method: "btree"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	lines := strings.Split(body, "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3 (header + 2 rows); body=%q", len(lines), body)
	}
	header := lines[0]
	for _, h := range []string{"NAME", "FLAGS", "COLUMNS", "METHOD"} {
		if !strings.Contains(header, h) {
			t.Errorf("header %q missing %q", header, h)
		}
	}
	if !strings.Contains(lines[1], "pk_users") || !strings.Contains(lines[1], "PK") {
		t.Errorf("row 1 = %q, want pk_users/PK", lines[1])
	}
	if !strings.Contains(lines[1], "(id)") {
		t.Errorf("row 1 = %q, want columns wrapped as (id)", lines[1])
	}
}

// TestIndexesContext_HandleRenderFormatsUniqueAndColumns asserts a
// unique (non-primary) index renders the "UNIQUE" flag, its columns
// wrapped in parentheses, and its method. (Parity with the retired inspect
// panel's FormatsUniqueAndColumns test.)
func TestIndexesContext_HandleRenderFormatsUniqueAndColumns(t *testing.T) {
	drv := &captureDriver{}
	c := newTestIndexes(drv)
	c.SetItems([]any{
		models.Index{Name: "u_email", IsUnique: true, Columns: []string{"email"}, Method: "btree"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "UNIQUE") {
		t.Errorf("body should contain UNIQUE flag: %q", body)
	}
	if !strings.Contains(body, "(email)") {
		t.Errorf("body should contain (email): %q", body)
	}
	if !strings.Contains(body, "btree") {
		t.Errorf("body should contain method btree: %q", body)
	}
}

// TestIndexesContext_HandleRenderEmpty asserts the empty-state renders
// exactly "(no indexes)" with no header row.
func TestIndexesContext_HandleRenderEmpty(t *testing.T) {
	drv := &captureDriver{}
	c := newTestIndexes(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (empty): %v", err)
	}
	if drv.lastContent != "(no indexes)" {
		t.Errorf("empty body = %q, want %q", drv.lastContent, "(no indexes)")
	}
}

// TestIndexesContext_HandleRenderSafeText asserts an ANSI escape in a
// DB-supplied cell is sanitized rather than written raw.
func TestIndexesContext_HandleRenderSafeText(t *testing.T) {
	drv := &captureDriver{}
	c := newTestIndexes(drv)
	c.SetItems([]any{
		models.Index{Name: "\x1b[31mevil\x1b[0m", Columns: []string{"id"}, Method: "btree"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Errorf("body retains raw ESC: %q", drv.lastContent)
	}
}

// TestIndexesContext_HandleRenderNoOrigin asserts the leaf render does
// not write the view origin.
func TestIndexesContext_HandleRenderNoOrigin(t *testing.T) {
	drv := &originSpyDriver{}
	c := newTestIndexes(drv)
	c.SetItems([]any{models.Index{Name: "pk", Columns: []string{"id"}, Method: "btree"}})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.originWrites != 0 {
		t.Errorf("HandleRender wrote view origin %d time(s); want 0", drv.originWrites)
	}
}
