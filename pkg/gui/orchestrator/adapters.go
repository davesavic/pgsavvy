package orchestrator

import (
	"context"
	"fmt"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
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
// real data.ConnectHelper.Connect, stashes the active connection ID on
// the Gui so SchemasInvoker can scope its AppState keys, and wires the
// fresh SQLSession into the orchestrator-owned QueryRunner (dbsavvy-66p.16).
//
// The query SQLSession runs on a SECOND drivers.Session acquired from
// the same Connection — the first session is owned by ConnectHelper for
// schema-rail traffic. This keeps SQLSession's queue (Execute / Stream /
// Explain serializer) disjoint from ConnectHelper's worker queue so a
// long-running query never blocks a schema refresh and vice-versa.
type connectInvoker struct {
	g       *Gui
	helper  *data.ConnectHelper
	runner  *data.QueryRunner
	history *query.History
}

func (c *connectInvoker) Connect(ctx context.Context, profile *models.Connection) error {
	if c == nil || c.helper == nil {
		return nil
	}
	conn, _, err := c.helper.Connect(ctx, profile)
	if err != nil {
		return err
	}
	if profile != nil && c.g != nil {
		c.g.activeConnID = profile.Name
	}
	if err := c.wireQueryRuntime(ctx, conn, profile); err != nil {
		// Roll back the ConnectHelper.Connect so we don't leak the schema-
		// rail session in a half-wired state. The user sees the wiring
		// error verbatim; a follow-up reconnect goes through the same
		// path cleanly.
		c.helper.Disconnect()
		if c.g != nil {
			c.g.activeConnID = ""
		}
		return err
	}
	return nil
}

// wireQueryRuntime acquires the second drivers.Session, derives the
// driver capabilities, builds the SQLSession with the orchestrator's
// History as recorder, and Bind()s the QueryRunner. Stashes the
// SQLSession on the Gui so Close can cancel an in-flight Stream.
func (c *connectInvoker) wireQueryRuntime(ctx context.Context, conn drivers.Connection, profile *models.Connection) error {
	if c.runner == nil || conn == nil || profile == nil {
		return nil
	}
	caps, capsErr := capsForDriver(ctx, profile.Driver)
	if capsErr != nil {
		return fmt.Errorf("orchestrator: derive capabilities: %w", capsErr)
	}
	sessInner, err := conn.AcquireSession(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: acquire query session: %w", err)
	}
	opts := session.Options{}
	if c.history != nil {
		opts.HistoryRecorder = c.history.AsSessionRecorder(profile.Name)
	}
	sqlSess := session.New(conn, sessInner, opts)
	c.runner.Bind(sqlSess, caps)
	if c.g != nil {
		c.g.activeSQLSession = sqlSess
	}
	return nil
}

// capsForDriver constructs a throwaway driver via the registered Factory
// to read its Capabilities. Cheap — Factory just allocates a struct;
// Open is NOT called.
func capsForDriver(ctx context.Context, name string) (drivers.Capabilities, error) {
	factory, err := drivers.Get(name)
	if err != nil {
		return drivers.Capabilities{}, err
	}
	drv, err := factory(ctx)
	if err != nil {
		return drivers.Capabilities{}, err
	}
	return drv.Capabilities(), nil
}

// connectionFormInvoker is the controllers.ConnectionFormInvoker facade.
// It dispatches the (synchronous, blocking) WalkAddConnection call onto a
// worker goroutine via g.OnWorker so the action handler running on the
// gocui MainLoop returns immediately; the ChainedPrompter adapter then
// re-enters the UI lane via OnUIThread to push popups while the worker
// stays parked on a result channel (see prompt_chain_adapter.go).
type connectionFormInvoker struct {
	g        *Gui
	helper   *data.ConnectionFormHelper
	prompter data.ChainedPrompter

	// onWorker can be overridden in tests to assert that WalkAdd
	// schedules its body via the worker lane rather than running it
	// inline on the caller goroutine. Production wiring leaves this nil
	// and the receiver falls back to c.g.OnWorker.
	onWorker func(fn func(gocui.Task) error)
}

func (c *connectionFormInvoker) WalkAdd(ctx context.Context) error {
	if c == nil || c.helper == nil {
		return nil
	}
	worker := c.onWorker
	if worker == nil {
		if c.g == nil {
			return nil
		}
		worker = c.g.OnWorker
	}
	worker(func(_ gocui.Task) error {
		return c.helper.WalkAddConnection(ctx, c.prompter, func(_ models.Connection) {
			// No-op: connection-list refresh after add is deferred.
		})
	})
	return nil
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

// Compile-time assertions: all adapters satisfy their target interfaces.
var (
	_ controllers.ConnectionPicker      = connectionsPickerAdapter{}
	_ controllers.SchemaPicker          = schemasPickerAdapter{}
	_ controllers.TablePicker           = tablesPickerAdapter{}
	_ controllers.ActiveConnection      = (*activeConnAdapter)(nil)
	_ controllers.ConnectInvoker        = (*connectInvoker)(nil)
	_ controllers.ConnectionFormInvoker = (*connectionFormInvoker)(nil)
	_ controllers.MenuPushHelper        = (*menuPushHelper)(nil)
)
