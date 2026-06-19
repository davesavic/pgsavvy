package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newTestForeignKeys(drv types.GuiDriver) *ForeignKeysContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.FOREIGN_KEYS,
		ViewName: string(types.FOREIGN_KEYS),
		Kind:     types.SIDE_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewForeignKeysContext(base, deps)
}

// TestForeignKeysContext_BothDirections asserts both sections render with
// their headings and rows when inbound and outbound FKs are present.
func TestForeignKeysContext_BothDirections(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys(
		[]models.ForeignKey{{
			Columns: []string{"author_id"}, RefSchema: "public", RefTable: "users",
			RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION",
		}},
		[]models.ForeignKey{{
			Columns: []string{"post_id"}, RefSchema: "public", RefTable: "comments",
			RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION",
		}},
	)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "References ->") {
		t.Errorf("body missing outbound heading: %q", body)
	}
	if !strings.Contains(body, "author_id -> public.users(id) ON DELETE CASCADE") {
		t.Errorf("body missing outbound row: %q", body)
	}
	if !strings.Contains(body, "Referenced by <-") {
		t.Errorf("body missing inbound heading: %q", body)
	}
	if !strings.Contains(body, "post_id -> public.comments(id)") {
		t.Errorf("body missing inbound row: %q", body)
	}
}

// TestForeignKeysContext_HeadingsDistinct asserts the two section headings
// are visually set apart from the data rows: each heading is wrapped in the
// bold accent SGR, data rows are indented, and a blank line separates the
// outbound section from the inbound one.
func TestForeignKeysContext_HeadingsDistinct(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys(
		[]models.ForeignKey{{
			Columns: []string{"author_id"}, RefSchema: "public", RefTable: "users",
			RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION",
		}},
		[]models.ForeignKey{{
			Columns: []string{"post_id"}, RefSchema: "public", RefTable: "comments",
			RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION",
		}},
	)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, fkHeadingSGR+"References ->"+fkAnsiReset) {
		t.Errorf("outbound heading not styled: %q", body)
	}
	if !strings.Contains(body, fkHeadingSGR+"Referenced by <-"+fkAnsiReset) {
		t.Errorf("inbound heading not styled: %q", body)
	}
	if !strings.Contains(body, "\n"+fkRowIndent+"author_id -> public.users(id)") {
		t.Errorf("outbound row not indented: %q", body)
	}
	if !strings.Contains(body, "\n\n"+fkHeadingSGR+"Referenced by <-") {
		t.Errorf("sections not separated by a blank line: %q", body)
	}
}

// TestForeignKeysContext_CompositeFK asserts a 2-column FK renders both
// column lists parenthesized.
func TestForeignKeysContext_CompositeFK(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys([]models.ForeignKey{{
		Columns: []string{"a", "b"}, RefSchema: "public", RefTable: "parent",
		RefColumns: []string{"x", "y"}, OnDelete: "CASCADE", OnUpdate: "NO ACTION",
	}}, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	want := "(a, b) -> public.parent(x, y) ON DELETE CASCADE"
	if !strings.Contains(drv.lastContent, want) {
		t.Errorf("composite row not found: want %q in %q", want, drv.lastContent)
	}
}

// TestForeignKeysContext_BothActions asserts ON DELETE then ON UPDATE
// render in order when both are non-default.
func TestForeignKeysContext_BothActions(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys([]models.ForeignKey{{
		Columns: []string{"c"}, RefSchema: "public", RefTable: "t",
		RefColumns: []string{"id"}, OnDelete: "CASCADE", OnUpdate: "RESTRICT",
	}}, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "ON DELETE CASCADE ON UPDATE RESTRICT") {
		t.Errorf("both-actions clause missing: %q", drv.lastContent)
	}
}

// TestForeignKeysContext_NoActionOmitsClause asserts a FK with NO ACTION
// on both renders neither clause.
func TestForeignKeysContext_NoActionOmitsClause(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys([]models.ForeignKey{{
		Columns: []string{"c"}, RefSchema: "public", RefTable: "t",
		RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION",
	}}, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "ON DELETE") || strings.Contains(drv.lastContent, "ON UPDATE") {
		t.Errorf("NO ACTION FK must omit clauses: %q", drv.lastContent)
	}
}

// TestForeignKeysContext_InboundErrorKeepsOutbound asserts an inbound
// error pins the inbound error line while outbound rows still render.
func TestForeignKeysContext_InboundErrorKeepsOutbound(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys([]models.ForeignKey{{
		Columns: []string{"c"}, RefSchema: "public", RefTable: "t",
		RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION",
	}}, nil)
	c.SetError("inbound", true)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "could not load inbound foreign keys") {
		t.Errorf("inbound error line missing: %q", body)
	}
	if !strings.Contains(body, "c -> public.t(id)") {
		t.Errorf("outbound row should still render: %q", body)
	}
}

// TestForeignKeysContext_Empty asserts each empty section renders the
// "No foreign keys" placeholder.
func TestForeignKeysContext_Empty(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Count(drv.lastContent, "No foreign keys") != 2 {
		t.Errorf("expected both sections empty: %q", drv.lastContent)
	}
}

// TestForeignKeysContext_SafeText asserts an ANSI escape in a DB-supplied
// column name is sanitized.
func TestForeignKeysContext_SafeText(t *testing.T) {
	drv := &captureDriver{}
	c := newTestForeignKeys(drv)
	c.SetForeignKeys([]models.ForeignKey{{
		Columns: []string{"\x1b[31mevil\x1b[0m"}, RefSchema: "public", RefTable: "t",
		RefColumns: []string{"id"}, OnDelete: "NO ACTION", OnUpdate: "NO ACTION",
	}}, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	// SafeText strips the DB-supplied ESC bytes, so the injected red SGR must
	// not survive; only the app's own heading SGR may appear in the body.
	if strings.Contains(drv.lastContent, "\x1b[31m") {
		t.Errorf("DB-supplied ESC sequence retained: %q", drv.lastContent)
	}
	if !strings.Contains(drv.lastContent, "[31mevil[0m") {
		t.Errorf("expected sanitized column form: %q", drv.lastContent)
	}
}
