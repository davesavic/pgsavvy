package context

import (
	"fmt"

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

	title string
	body  string
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

// SetContent installs the title and body the next HandleRender paints.
// Called by ConfirmHelper.Confirm before pushing onto the focus stack.
func (c *ConfirmationContext) SetContent(title, body string) {
	c.title = title
	c.body = body
}

// GetTitle returns the dynamic confirmation title so the layout pass can
// paint it as the popup's frame heading. Falls back to the static
// BaseContext title when no content has been installed yet.
func (c *ConfirmationContext) GetTitle() string {
	if c.title != "" {
		return c.title
	}
	return c.BaseContext.GetTitle()
}

// HandleRender writes the body into the gocui view. The title is painted
// as the frame heading by the layout pass (see GetTitle), so it is not
// repeated in the body content.
func (c *ConfirmationContext) HandleRender() error {
	if c.title == "" && c.body == "" {
		return nil
	}
	content := fmt.Sprintf("%s\n\n[y]es / [n]o", c.body)
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, content)
	})
	return nil
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
