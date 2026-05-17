package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// ConfirmationContext renders the confirmation popup. Border styling and
// header text come from deps.PresentationHook so T8 (enn.9) supplies the
// connection-coloured styling without touching this file.
type ConfirmationContext struct {
	BaseContext

	deps Deps

	// activeConnection is the connection whose color should drive the
	// popup border styling. Helpers set it before pushing; nil means the
	// default border style.
	activeConnection *models.Connection
}

// NewConfirmationContext builds a ConfirmationContext bound to CONFIRMATION.
func NewConfirmationContext(base BaseContext, deps Deps) *ConfirmationContext {
	return &ConfirmationContext{BaseContext: base, deps: deps}
}

// SetActiveConnection records the connection whose color drives the
// border style on the next HandleRender. Callers set this immediately
// before pushing the popup.
func (c *ConfirmationContext) SetActiveConnection(conn *models.Connection) {
	c.activeConnection = conn
}

// Presentation resolves the popup border style and header text via
// deps.PresentationHook. Returns the zero TextStyle and empty header
// when the hook is absent — T2 ships the call seam only; T8 plugs the
// real impl.
func (c *ConfirmationContext) Presentation() (types.TextStyle, string) {
	if c.deps.PresentationHook == nil {
		return types.TextStyle{}, ""
	}
	return c.deps.PresentationHook(c.activeConnection)
}
