package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newTestConstraints(drv types.GuiDriver) *ConstraintsContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CONSTRAINTS,
		ViewName: string(types.CONSTRAINTS),
		Kind:     types.SIDE_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewConstraintsContext(base, deps)
}

// TestConstraintsContext_Populated asserts each constraint renders its
// Kind and Definition.
func TestConstraintsContext_Populated(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConstraints(drv)
	c.SetConstraints([]models.Constraint{
		{Kind: "CHECK", Definition: "CHECK (age > 0)"},
		{Kind: "UNIQUE", Definition: "UNIQUE (email)"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "CHECK") || !strings.Contains(body, "CHECK (age > 0)") {
		t.Errorf("CHECK constraint missing: %q", body)
	}
	if !strings.Contains(body, "UNIQUE (email)") {
		t.Errorf("UNIQUE constraint missing: %q", body)
	}
	if strings.Count(body, "\n") != 1 {
		t.Errorf("expected two rows (one newline): %q", body)
	}
}

// TestConstraintsContext_Empty asserts the empty-state renders exactly
// "No constraints".
func TestConstraintsContext_Empty(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConstraints(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "No constraints" {
		t.Errorf("empty body = %q, want %q", drv.lastContent, "No constraints")
	}
}

// TestConstraintsContext_Error asserts the error-state renders exactly the
// pinned error line.
func TestConstraintsContext_Error(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConstraints(drv)
	c.SetConstraints([]models.Constraint{{Kind: "CHECK", Definition: "x"}})
	c.SetError(true)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "could not load constraints" {
		t.Errorf("error body = %q, want %q", drv.lastContent, "could not load constraints")
	}
}

// TestConstraintsContext_SafeText asserts an ANSI escape in a DB-supplied
// definition is sanitized.
func TestConstraintsContext_SafeText(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConstraints(drv)
	c.SetConstraints([]models.Constraint{{Kind: "CHECK", Definition: "\x1b[31mevil\x1b[0m"}})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "\x1b") {
		t.Errorf("body retains raw ESC: %q", drv.lastContent)
	}
}
