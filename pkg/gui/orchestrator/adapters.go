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
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
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
	c.hydrateQueryEditorBuffer(profile)
	c.populateSchemasRail(ctx)

	if len(c.g.registry.Schemas.Items()) != 0 {
		c.g.OnUIThread(func() error {
			// Focus the SCHEMAS rail so the user sees the loaded schemas
			// immediately and can j/k to navigate them.
			return c.g.tree.Push(c.g.registry.Schemas)
		})
	}
	return nil
}

// populateSchemasRail loads the schema list via ConnectHelper.LoadSchemas
// and pushes the visible subset (built-in / profile-hidden patterns
// filtered out) onto the SchemasContext so the SCHEMAS rail draws rows
// on the next layout frame. Without this hook the rail stays empty
// after a successful connect even though the driver is ready
// (dbsavvy-855).
//
// Best-effort: a LoadSchemas error is logged and swallowed — the user
// still has the open connection and can retry by re-pressing <cr>.
// Empty registry / context (test wiring) collapses to a silent no-op.
func (c *connectInvoker) populateSchemasRail(ctx context.Context) {
	if c == nil || c.g == nil || c.helper == nil {
		return
	}
	if c.g.registry == nil || c.g.registry.Schemas == nil {
		return
	}
	schemas, err := c.helper.LoadSchemas(ctx, "")
	if err != nil {
		c.g.deps.Common.Logger().Warn("gui: load schemas after connect", "err", err)
		return
	}
	visible := schemas
	if c.g.schemasHelper != nil {
		// Apply builtin + profile hide-pattern filter. Runtime hides
		// (AppState.HiddenSchemas) are deliberately NOT consulted here
		// — the SHOW-HIDDEN toggle (H/U) reveals them on demand, and
		// pulling them in at populate time would require a second pass
		// through the picker on every toggle.
		builtin, profile := defaultHiddenPatterns()
		v, _ := c.g.schemasHelper.FilterHidden(schemas, builtin, profile, nil)
		visible = v
	}
	items := make([]any, len(visible))
	for i := range visible {
		s := visible[i]
		items[i] = s
	}
	c.g.registry.Schemas.SetItems(items)
}

// populateTablesRail loads the table list for schema via
// ConnectHelper.LoadTables and pushes the result onto TablesContext so
// the TABLES rail draws rows on the next layout frame. Wired to the
// SCHEMAS-rail <CR> handler via HelperBag.OnSchemaActivate (dbsavvy-04n).
//
// Best-effort: a LoadTables error is logged and swallowed; the existing
// TablesContext.items are left intact so a transient failure does not
// blank a previously-loaded list. An empty schema name is a silent
// no-op (matches the picker's empty-list contract).
func (c *connectInvoker) populateTablesRail(ctx context.Context, schema string) {
	if c == nil || c.g == nil || c.helper == nil {
		return
	}
	if schema == "" {
		return
	}
	if c.g.registry == nil || c.g.registry.Tables == nil {
		return
	}
	tables, err := c.helper.LoadTables(ctx, schema)
	if err != nil {
		c.g.deps.Common.Logger().Warn(fmt.Sprintf("gui: load tables for schema %q: %v", schema, err))
		return
	}
	items := make([]any, len(tables))
	for i := range tables {
		items[i] = tables[i]
	}
	c.g.registry.Tables.SetItems(items)
}

// populateColumnsRail loads the column list for (schema, table) via
// ConnectHelper.LoadColumns and pushes the result onto ColumnsContext so
// the COLUMNS rail draws rows on the next layout frame. Wired to the
// TABLES-rail <CR> handler via HelperBag.OnTableActivate.
//
// Best-effort: a LoadColumns error is logged and swallowed; the existing
// ColumnsContext.items are left intact so a transient failure does not
// blank a previously-loaded list. Empty schema/table is a silent no-op.
func (c *connectInvoker) populateColumnsRail(ctx context.Context, schema, table string) {
	if c == nil || c.g == nil || c.helper == nil {
		return
	}
	if schema == "" || table == "" {
		return
	}
	if c.g.registry == nil || c.g.registry.Columns == nil {
		return
	}
	cols, err := c.helper.LoadColumns(ctx, schema, table)
	if err != nil {
		c.g.deps.Common.Logger().Warn(fmt.Sprintf("gui: load columns for %s.%s: %v", schema, table, err))
		return
	}
	items := make([]any, len(cols))
	for i := range cols {
		items[i] = cols[i]
	}
	c.g.registry.Columns.SetItems(items)
}

// hydrateQueryEditorBuffer is the dbsavvy-wwd.9 post-Connect hook. It
// resolves (or generates) the persistent buffer UUID for the active
// connection via AppState.LastBufferUUIDs, loads the on-disk buffer (or
// a fresh empty Buffer when missing), and injects it into the live
// QueryEditorContext. Missing Common / registry / profile are silent
// no-ops so test wiring without persistence still passes through.
//
// The hydration runs on the Connect goroutine (worker, via
// onWorkerConnect) so the disk read does not block the MainLoop.
// SetBuffer itself is mutex-free on QueryEditorContext but the swapped
// *editor.Buffer's own sync.RWMutex serialises subsequent edits.
func (c *connectInvoker) hydrateQueryEditorBuffer(profile *models.Connection) {
	if c == nil || c.g == nil || profile == nil {
		return
	}
	if c.g.deps.Common == nil {
		return
	}
	common := c.g.deps.Common
	if c.g.registry == nil || c.g.registry.QueryEditor == nil {
		return
	}
	appState := common.AppState
	if appState == nil {
		return
	}
	connID := profile.Name
	uuid := appState.GetOrCreateBufferUUID(connID)
	if uuid == "" {
		return
	}
	buf, err := editor.LoadBuffer(common.Fs, common.StateDir, connID, uuid)
	if err != nil {
		common.Logger().Warn(fmt.Sprintf("gui: load query-editor buffer for %q: %v", connID, err))
		return
	}
	c.g.registry.QueryEditor.SetBuffer(buf)
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
	if c.g != nil {
		opts.Logger = c.g.deps.Common.Logger()
	}
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
			if c.g == nil {
				return
			}
			// Re-seed the CONNECTIONS rail on the UI thread so the new
			// profile shows up in the list. The onComplete callback fires
			// on the worker goroutine that owns WalkAddConnection; routing
			// the slice swap through OnUIThread keeps it ordered with the
			// next render frame.
			c.g.OnUIThread(func() error {
				c.g.refreshConnectionsRail()
				return nil
			})
		})
	})
	return nil
}

// hideOverlayStateAdapter implements guicontext.HideOverlayState by
// proxying through the ResultTabsHelper. The helper owns overlay
// lifecycle (HideOverlayActive / HideOverlayBody) and the context only
// renders the rendered body each frame. dbsavvy-uv0.6.
type hideOverlayStateAdapter struct {
	helper *ui.ResultTabsHelper
}

func (a hideOverlayStateAdapter) Active() bool {
	if a.helper == nil {
		return false
	}
	return a.helper.HideOverlayActive()
}

func (a hideOverlayStateAdapter) Body() string {
	if a.helper == nil {
		return ""
	}
	return a.helper.HideOverlayBody()
}

// exportMenuStateAdapter implements guicontext.ExportMenuState by
// proxying through the ResultTabsHelper. Mirrors hideOverlayStateAdapter.
// dbsavvy-uv0.9.
type exportMenuStateAdapter struct {
	helper *ui.ResultTabsHelper
}

func (a exportMenuStateAdapter) Active() bool {
	if a.helper == nil {
		return false
	}
	return a.helper.ExportMenuActive()
}

func (a exportMenuStateAdapter) Body() string {
	if a.helper == nil {
		return ""
	}
	return a.helper.ExportMenuBody()
}

// promptStateAdapter implements guicontext.PromptState by surfacing
// the PromptHelper's label + active flag to PromptContext.HandleRender.
// The typed buffer is no longer combined here: post-dbsavvy-fq9 the
// PROMPT view is editable and the runtime source of truth for the
// input is the view's TextArea (PromptContext.Buffer reads through),
// mirroring the COMMAND_LINE path.
type promptStateAdapter struct {
	helper *ui.PromptHelper
}

func (a *promptStateAdapter) Active() bool {
	if a == nil || a.helper == nil {
		return false
	}
	return a.helper.Active()
}

func (a *promptStateAdapter) Label() string {
	if a == nil || a.helper == nil {
		return ""
	}
	return a.helper.Label()
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
