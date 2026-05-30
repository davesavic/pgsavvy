package context

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ConnectionManagerContext is the centered modal connection-manager box
// (dbsavvy-ig4). MAIN_CONTEXT kind: when top of the focus stack the layout
// pass paints it as a centered bordered box over a blank background,
// suppressing both the side rails and the QUERY_EDITOR for that frame.
//
// This is a scaffold: HandleRender writes a placeholder body. The list /
// form / connect flows land in later tasks.
//
// Strings are hardcoded English (mirrors ConnectingContext — i18n is not
// threaded through this scaffold).
type ConnectionManagerContext struct {
	BaseContext

	deps depsAlias
}

// Compile-time assertion that the live type satisfies the lifecycle
// contract.
var _ types.IBaseContext = (*ConnectionManagerContext)(nil)

// NewConnectionManagerContext builds the context bound to CONNECTION_MANAGER.
func NewConnectionManagerContext(base BaseContext, deps depsAlias) *ConnectionManagerContext {
	return &ConnectionManagerContext{BaseContext: base, deps: deps}
}

// HandleRender writes the placeholder body into the CONNECTION_MANAGER view.
// A nil GuiDriver is a silent no-op (test wiring / partial bootstrap) so the
// call never panics.
func (c *ConnectionManagerContext) HandleRender() error {
	viewName := c.GetViewName()
	writeView(c.deps, func() error {
		return c.deps.GuiDriver.SetContent(viewName, "Connection Manager")
	})
	return nil
}

// GetKind overrides BaseContext.GetKind to publish MAIN_CONTEXT, mirroring
// ConnectingContext so a later refactor that drops the explicit kind in
// setup.go stays sound.
func (c *ConnectionManagerContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }
