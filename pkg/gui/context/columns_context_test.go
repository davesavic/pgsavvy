package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// originSpyDriver records calls to ViewByName, which scrollSideRailIntoView
// is the only side-rail render path to make. The aligned leaf render must
// never write the view origin, so ViewByName must stay at zero calls.
type originSpyDriver struct {
	captureDriver
	originWrites int
}

func (d *originSpyDriver) ViewByName(string) (types.View, error) {
	d.originWrites++
	return nil, nil
}

func newTestColumns(drv types.GuiDriver) *ColumnsContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.COLUMNS,
		ViewName: string(types.COLUMNS),
		Kind:     types.SIDE_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewColumnsContext(base, deps)
}

// TestColumnsContext_HandleRenderAlignedTable asserts the leaf renders
// the aligned column table: a "NAME ... TYPE ... NULL ... DEFAULT" header
// followed by one aligned row per column.
func TestColumnsContext_HandleRenderAlignedTable(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "integer", Nullable: false, Default: "nextval('s')"},
		&models.Column{Name: "email", DataType: "text", Nullable: true},
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
	for _, h := range []string{"NAME", "TYPE", "NULL", "DEFAULT"} {
		if !strings.Contains(header, h) {
			t.Errorf("header %q missing %q", header, h)
		}
	}
	if !strings.Contains(lines[1], "id") || !strings.Contains(lines[1], "integer") {
		t.Errorf("row 1 = %q, want id/integer", lines[1])
	}
	// Columns must line up: TYPE starts at the same offset on header and rows.
	if got := strings.Index(header, "TYPE"); got != strings.Index(lines[1], "integer") {
		t.Errorf("TYPE column not aligned: header offset %d, row offset %d (header=%q row=%q)",
			got, strings.Index(lines[1], "integer"), header, lines[1])
	}
}

// TestColumnsContext_HandleRenderFormatsNonNullAndDefault asserts a
// non-nullable column renders the "NOT NULL" marker and a column with a
// default renders the "default=" prefix. (Parity with the retired inspect
// panel's FormatsNonNullAndDefault test.)
func TestColumnsContext_HandleRenderFormatsNonNullAndDefault(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "int", Nullable: false, Default: "nextval()"},
		models.Column{Name: "note", DataType: "text", Nullable: true},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "NOT NULL") {
		t.Errorf("body should contain NOT NULL marker for non-nullable column: %q", body)
	}
	if !strings.Contains(body, "default=nextval()") {
		t.Errorf("body should contain default=nextval(): %q", body)
	}
	if strings.Count(body, "\n") != 2 {
		t.Errorf("body expected header + two rows (2 newlines): %q", body)
	}
}

// TestColumnsContext_HandleRenderEmpty asserts the empty-state renders
// exactly "(no columns)" with no header row.
func TestColumnsContext_HandleRenderEmpty(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (empty): %v", err)
	}
	if drv.lastContent != "(no columns)" {
		t.Errorf("empty body = %q, want %q", drv.lastContent, "(no columns)")
	}
}

// TestColumnsContext_HandleRenderSafeText asserts an ANSI escape in a
// DB-supplied cell is sanitized rather than written raw.
func TestColumnsContext_HandleRenderSafeText(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "x", DataType: "text", Nullable: true, Default: "\x1b[31mevil\x1b[0m"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Errorf("body retains raw ESC: %q", drv.lastContent)
	}
}

// TestColumnsContext_HandleRenderNullableOmitsMarker asserts a nullable
// column omits the NOT NULL marker.
func TestColumnsContext_HandleRenderNullableOmitsMarker(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "x", DataType: "text", Nullable: true},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	row := strings.Split(drv.lastContent, "\n")[1]
	if strings.Contains(row, "NOT NULL") {
		t.Errorf("nullable column row %q must not contain NOT NULL", row)
	}
}

// TestColumnsContext_HandleRenderDescriptionColumn asserts a DESCRIPTION
// header appears when at least one column is documented, the documented
// row shows its comment, and an undocumented row stays column-aligned with
// a blank description cell.
func TestColumnsContext_HandleRenderDescriptionColumn(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "integer", Nullable: false, Description: "primary id"},
		models.Column{Name: "name", DataType: "text", Nullable: true},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	lines := strings.Split(body, "\n")
	header := lines[0]
	if !strings.Contains(header, "DESCRIPTION") {
		t.Errorf("header %q missing DESCRIPTION", header)
	}
	if !strings.Contains(lines[1], "primary id") {
		t.Errorf("id row %q missing description", lines[1])
	}
	// The DESCRIPTION column starts at the same offset on header and on the
	// documented row; the undocumented row simply has no text there.
	descOff := strings.Index(header, "DESCRIPTION")
	if got := strings.Index(lines[1], "primary id"); got != descOff {
		t.Errorf("DESCRIPTION not aligned: header offset %d, row offset %d (header=%q row=%q)",
			descOff, got, header, lines[1])
	}
	if strings.Contains(lines[2], "primary id") {
		t.Errorf("name row %q should have a blank description", lines[2])
	}
}

// TestColumnsContext_HandleRenderNoDescriptionColumn asserts that when no
// column is documented the DESCRIPTION column is suppressed: exactly four
// columns and no trailing whitespace on any line.
func TestColumnsContext_HandleRenderNoDescriptionColumn(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "integer", Nullable: false},
		models.Column{Name: "name", DataType: "text", Nullable: true},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if strings.Contains(body, "DESCRIPTION") {
		t.Errorf("undocumented table must not render DESCRIPTION: %q", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line has trailing whitespace: %q", line)
		}
	}
}

// TestColumnsContext_HandleRenderDescriptionTruncated asserts a long
// description is truncated with an ellipsis.
func TestColumnsContext_HandleRenderDescriptionTruncated(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	long := strings.Repeat("a", 100)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "integer", Description: long},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "…") {
		t.Errorf("long description should be truncated with ellipsis: %q", body)
	}
	if strings.Contains(body, long) {
		t.Errorf("long description should not appear in full: %q", body)
	}
}

// TestColumnsContext_HandleRenderDescriptionSingleLine asserts a multi-line
// comment is rendered on a single line (newlines collapsed to spaces).
func TestColumnsContext_HandleRenderDescriptionSingleLine(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "integer", Description: "line1\nline2"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "line1 line2") {
		t.Errorf("multi-line description should render as one line: %q", body)
	}
}

// TestColumnsContext_HandleRenderDescriptionSafeText asserts a control byte
// in a description is sanitized rather than written raw.
func TestColumnsContext_HandleRenderDescriptionSafeText(t *testing.T) {
	drv := &captureDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{
		models.Column{Name: "id", DataType: "integer", Description: "ev\x1bil"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Errorf("body retains raw ESC: %q", drv.lastContent)
	}
}

// TestColumnsContext_HandleRenderNoOrigin asserts the leaf render does
// not write the view origin (SetViewOrigin must not be called).
func TestColumnsContext_HandleRenderNoOrigin(t *testing.T) {
	drv := &originSpyDriver{}
	c := newTestColumns(drv)
	c.SetItems([]any{models.Column{Name: "id", DataType: "integer"}})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.originWrites != 0 {
		t.Errorf("HandleRender wrote view origin %d time(s); want 0", drv.originWrites)
	}
}
