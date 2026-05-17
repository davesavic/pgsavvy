package orchestrator

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// connectionsPickerAdapter exposes the CONNECTIONS rail's selected
// *models.Connection via the controllers.ConnectionPicker interface.
type connectionsPickerAdapter struct {
	registry *guicontext.ConnectionsContext
}

func (a connectionsPickerAdapter) SelectedConnection() *models.Connection {
	if a.registry == nil {
		return nil
	}
	v := a.registry.SelectedItem()
	if v == nil {
		return nil
	}
	conn, _ := v.(*models.Connection)
	return conn
}

// schemasPickerAdapter exposes the SCHEMAS rail's selected schema name
// and forwards the show-hidden toggle.
type schemasPickerAdapter struct {
	registry *guicontext.SchemasContext
}

func (a schemasPickerAdapter) SelectedSchemaName() string {
	if a.registry == nil {
		return ""
	}
	v := a.registry.SelectedItem()
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case models.Schema:
		return s.Name
	case *models.Schema:
		if s == nil {
			return ""
		}
		return s.Name
	}
	return ""
}

func (a schemasPickerAdapter) ToggleShowHidden() {
	if a.registry == nil {
		return
	}
	a.registry.SetShowHiddenMode(!a.registry.GetShowHiddenMode())
}

// tablesPickerAdapter exposes the TABLES rail's selected *models.Table.
type tablesPickerAdapter struct {
	registry *guicontext.TablesContext
}

func (a tablesPickerAdapter) SelectedTable() *models.Table {
	if a.registry == nil {
		return nil
	}
	v := a.registry.SelectedItem()
	if v == nil {
		return nil
	}
	t, _ := v.(*models.Table)
	return t
}

// activeConnAdapter reports the currently active connection ID stored
// on the Gui after a successful Connect.
type activeConnAdapter struct {
	g *Gui
}

func (a *activeConnAdapter) ActiveConnectionID() string {
	if a.g == nil {
		return ""
	}
	return a.g.activeConnID
}

// connectInvoker is the controllers.ConnectInvoker facade. It calls the
// real data.ConnectHelper.Connect and stashes the active connection ID
// on the Gui so SchemasInvoker can scope its AppState keys.
type connectInvoker struct {
	g      *Gui
	helper *data.ConnectHelper
}

func (c *connectInvoker) Connect(ctx context.Context, profile *models.Connection) error {
	if c == nil || c.helper == nil {
		return nil
	}
	_, _, err := c.helper.Connect(ctx, profile)
	if err == nil && profile != nil && c.g != nil {
		c.g.activeConnID = profile.Name
	}
	return err
}

// connectionFormInvoker is the controllers.ConnectionFormInvoker facade.
// The real WalkAddConnection takes a ChainedPrompter; the bootstrap
// supplies a no-op prompter for this epic so the binding registers but
// the production walk surfaces a fail-fast error if invoked. A later
// epic wires the PromptHelper to drive ChainedPrompter properly.
type connectionFormInvoker struct {
	g        *Gui
	helper   *data.ConnectionFormHelper
	prompter data.ChainedPrompter
}

func (c *connectionFormInvoker) WalkAdd(ctx context.Context) error {
	if c == nil || c.helper == nil {
		return nil
	}
	return c.helper.WalkAddConnection(ctx, c.prompter, func(_ models.Connection) {
		// No-op: connection-list refresh after add is deferred to E5.
	})
}

// menuPushHelper bridges controllers.MenuPushHelper to the focus-stack
// tree + MENU context.
type menuPushHelper struct {
	tree *gui.ContextTree
	menu *guicontext.MenuContext
}

func (m *menuPushHelper) PushMenu() error {
	if m.tree == nil || m.menu == nil {
		return nil
	}
	return m.tree.Push(m.menu)
}

func (m *menuPushHelper) PopMenu() error {
	if m.tree == nil {
		return nil
	}
	if err := m.tree.Pop(); err != nil && err != gui.ErrPopAtBottom {
		return err
	}
	return nil
}

// stubPrompter is a ChainedPrompter implementation that fails fast. The
// real prompter (PromptHelper-driven) lands in a later epic; until then
// invoking the `a` binding in production surfaces this error so the
// wiring failure is visible rather than silent.
type stubPrompter struct{}

func (stubPrompter) PromptString(_ context.Context, _, _ string, _ func(string) error) (string, error) {
	return "", data.PromptCanceledErr()
}

func (stubPrompter) PromptChoice(_ context.Context, _, _ string, _ []string) (string, error) {
	return "", data.PromptCanceledErr()
}

// Compile-time assertions: all adapters satisfy their target interfaces.
var (
	_ controllers.ConnectionPicker      = connectionsPickerAdapter{}
	_ controllers.SchemaPicker          = schemasPickerAdapter{}
	_ controllers.TablePicker           = tablesPickerAdapter{}
	_ controllers.ActiveConnection      = (*activeConnAdapter)(nil)
	_ controllers.ConnectInvoker        = (*connectInvoker)(nil)
	_ controllers.ConnectionFormInvoker = (*connectionFormInvoker)(nil)
	_ controllers.MenuPushHelper        = (*menuPushHelper)(nil)
	_ data.ChainedPrompter              = stubPrompter{}
)
