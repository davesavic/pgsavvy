package context

import (
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// constraintsErrorLine is the pinned error line for the CONSTRAINTS leaf.
// Asserted verbatim by tests.
const constraintsErrorLine = "could not load constraints"

// ConstraintsContext renders the constraints leaf of the TABLE_INSPECT
// tabbed popup: one aligned "Kind  Definition" row per constraint.
// Render-only; T2 populates it.
type ConstraintsContext struct {
	BaseContext

	deps Deps

	items   []models.Constraint
	errored bool
}

// NewConstraintsContext builds a ConstraintsContext bound to the
// CONSTRAINTS key and view.
func NewConstraintsContext(base BaseContext, deps Deps) *ConstraintsContext {
	return &ConstraintsContext{
		BaseContext: base,
		deps:        deps,
	}
}

// SetConstraints replaces the constraint slice.
func (c *ConstraintsContext) SetConstraints(items []models.Constraint) {
	c.items = items
}

// SetError flags the leaf as failed to load, pinning the error line on the
// next render.
func (c *ConstraintsContext) SetError(errored bool) {
	c.errored = errored
}

// HandleRender writes the constraint table into the view: the pinned error
// line on failure, "No constraints" when empty, else one aligned row per
// constraint showing Kind + Definition.
func (c *ConstraintsContext) HandleRender() error {
	deps := c.deps
	viewName := c.GetViewName()
	body := c.BodyText()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// BodyText returns the constraint table the leaf renders, so the TABLE_INSPECT
// container can compose a stats header above it (bodyTextRenderer).
func (c *ConstraintsContext) BodyText() string { return c.body() }

func (c *ConstraintsContext) body() string {
	if c.errored {
		return constraintsErrorLine
	}
	if len(c.items) == 0 {
		return "No constraints"
	}
	rows := make([][]string, 0, len(c.items))
	for i := range c.items {
		rows = append(rows, []string{
			config.SafeText(c.items[i].Kind),
			config.SafeText(c.items[i].Definition),
		})
	}
	return alignRows(rows)
}
