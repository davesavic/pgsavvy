package orchestrator

import (
	"context"
	"fmt"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
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
	// Force a re-render: the SCHEMAS view content is recomputed by
	// renderRows on the next HandleRender pass and the toggle changes
	// which rows survive the runtime-hidden filter. Without this kick
	// the user only sees the new content on the next ambient layout
	// pass (e.g. a key press); the H toggle would feel laggy.
	_ = a.registry.HandleRender()
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
	// Supersession token (dbsavvy-fow.1): bump on entry and capture the
	// new value. A later activation bumps it again; on completion we
	// compare via isStaleConnect and drop the result if a newer connect
	// has started, so a slow/timed-out dial can't clobber a more recent
	// connection. Atomic so concurrent worker-goroutine Connects don't
	// race the bump.
	var gen uint64
	if c.g != nil {
		gen = c.g.connectGen.Add(1)
	}
	// --- WORKER PHASE: all blocking I/O runs here (Connect itself runs on
	// the worker goroutine — connections_controller.go schedules it via
	// OnWorker). Nothing in this phase writes GUI state the MainLoop reads;
	// results are collected into locals and published in the single
	// OnUIThread closure below (dbsavvy-fow.1).
	conn, _, err := c.helper.Connect(ctx, profile)
	if err != nil {
		return err
	}
	if c.isStaleConnect(gen) {
		// A newer activation superseded this one mid-dial. Tear down the
		// freshly-opened schema-rail session so we don't leak it, and drop
		// the result without touching activeConn / the schemas rail.
		c.helper.Disconnect()
		return nil
	}
	// Open the query session (I/O part of wireQueryRuntime). This acquires
	// a second drivers.Session — kept on the worker so the dial+acquire
	// never blocks the MainLoop.
	rt, err := c.wireQueryRuntimeIO(ctx, conn, profile)
	if err != nil {
		// Roll back the ConnectHelper.Connect so we don't leak the schema-
		// rail session in a half-wired state. The user sees the wiring
		// error verbatim; a follow-up reconnect goes through the same
		// path cleanly. setActiveConn is marshalled onto the UI thread to
		// serialise with the MainLoop reads of activeConnID.
		c.helper.Disconnect()
		if !c.isStaleConnect(gen) {
			c.runOnUIThread(func() error {
				if c.isStaleConnect(gen) {
					return nil
				}
				c.setActiveConn(nil)
				return nil
			})
		}
		return err
	}
	// Load + filter the schema list (I/O+compute part of populateSchemasRail)
	// and resolve the persistent query-editor buffer (disk read part of
	// hydrateQueryEditorBuffer). Both run on the worker; the results are
	// published below.
	schemaItems, schemaOK := c.loadSchemaItems(ctx)
	editorBuf, editorOK := c.loadQueryEditorBuffer(profile)

	// dbsavvy-56u.1: stamp LastConnectionID and prepend the profile to
	// the LIFO RecentConnectionIDs ring (deduped, capped at 10). Persisted
	// AFTER wireQueryRuntimeIO succeeds so a wiring rollback does not leave
	// a debounced write pointing at a profile that failed to connect.
	// MutateAndSave is independently synchronized and touches no gocui view
	// state, so it stays on the worker (dbsavvy-fow.1).
	if profile != nil && c.g != nil && c.g.deps.Store != nil {
		name := profile.Name
		c.g.deps.Store.MutateAndSave(func(a *common.AppState) {
			a.LastConnectionID = name
			a.RecentConnectionIDs = common.PushRecentConnectionID(a.RecentConnectionIDs, name)
		})
	}

	// --- PUBLISH PHASE: a SINGLE OnUIThread closure performs every write
	// the MainLoop reads (activeConn, SQLSession, QueryRunner.Bind, the
	// SCHEMAS rail items, the editor buffer, the focus push) so they
	// serialise with render-frame reads (dbsavvy-fow.1). The stale-gen
	// recheck runs FIRST: if a newer activation superseded us, we tear
	// down everything the worker opened and publish NOTHING — so
	// activeSQLSession is never written for a superseded connect and no
	// session is orphaned (TOCTOU leak fix).
	c.runOnUIThread(func() error {
		if c.isStaleConnect(gen) {
			c.helper.Disconnect()
			if rt.sqlSess != nil {
				_ = rt.sqlSess.Close()
			}
			return nil
		}
		c.publishQueryRuntime(rt)
		c.publishQueryEditorBuffer(editorBuf, editorOK)
		c.publishSchemaItems(schemaItems, schemaOK)
		c.setActiveConn(profile)
		if c.g != nil && c.g.registry != nil && c.g.registry.Schemas != nil &&
			len(c.g.registry.Schemas.Items()) != 0 {
			// Focus the SCHEMAS rail so the user sees the loaded
			// schemas immediately and can j/k to navigate them.
			return c.g.tree.Push(c.g.registry.Schemas)
		}
		return nil
	})
	return nil
}

// runOnUIThread marshals fn onto the UI thread via g.OnUIThread when an
// async driver is wired, and otherwise runs it inline. The inline branch
// preserves the synchronous test-wiring path (c.g nil or the driver not
// yet attached) where publication must still happen on the caller
// goroutine. dbsavvy-fow.1.
func (c *connectInvoker) runOnUIThread(fn func() error) {
	if c == nil || fn == nil {
		return
	}
	if c.g != nil && c.g.driver != nil {
		c.g.OnUIThread(fn)
		return
	}
	_ = fn()
}

// isStaleConnect reports whether a connect that captured token gen has
// been superseded by a newer activation (a later Connect bumped
// connectGen past gen). Always false when g is nil (test wiring without
// a Gui). dbsavvy-fow.1.
func (c *connectInvoker) isStaleConnect(gen uint64) bool {
	if c == nil || c.g == nil {
		return false
	}
	return c.g.connectGen.Load() != gen
}

// setActiveConn writes the active-connection state. MUST be called on
// the UI thread (via OnUIThread) so it serialises with the MainLoop
// reads of activeConnID. Passing nil clears the state (wiring-rollback
// path). dbsavvy-fow.1.
func (c *connectInvoker) setActiveConn(profile *models.Connection) {
	if c == nil || c.g == nil {
		return
	}
	if profile == nil {
		c.g.activeConnID = ""
		c.g.activeConnProfile = nil
		return
	}
	c.g.activeConnID = profile.Name
	c.g.activeConnProfile = profile
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
	items, ok := c.loadSchemaItems(ctx)
	if !ok {
		return
	}
	c.publishSchemaItems(items, ok)
}

// loadSchemaItems is the I/O+compute phase of populateSchemasRail: it
// loads the schema list via ConnectHelper.LoadSchemas, applies the
// builtin+profile hide-pattern filter, and builds the []any rail slice.
// It performs NO context write (SetItems) so it is safe to run on the
// worker goroutine; publishSchemaItems does the write on the UI thread
// (dbsavvy-fow.1). The bool reports whether a slice was produced (false
// on missing deps or a LoadSchemas error) so callers can skip the publish
// and leave the existing rail items intact.
func (c *connectInvoker) loadSchemaItems(ctx context.Context) ([]any, bool) {
	if c == nil || c.g == nil || c.helper == nil {
		return nil, false
	}
	if c.g.registry == nil || c.g.registry.Schemas == nil {
		return nil, false
	}
	schemas, err := c.helper.LoadSchemas(ctx, "")
	if err != nil {
		c.g.deps.Common.Logger().Warn("gui: load schemas after connect", "err", err)
		return nil, false
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
	return items, true
}

// publishSchemaItems is the UI-thread publish phase paired with
// loadSchemaItems: it writes the computed slice onto the SchemasContext.
// SideListContext.SetItems is a plain mutex-free write of items+cursor
// that the MainLoop reads every frame via Items()/HandleRender()/
// SelectedItem(), so this MUST run on the UI thread to serialise with
// those reads (dbsavvy-fow.1). A false ok (load skipped/failed) is a
// no-op, leaving the existing rail intact.
func (c *connectInvoker) publishSchemaItems(items []any, ok bool) {
	if c == nil || !ok || c.g == nil || c.g.registry == nil || c.g.registry.Schemas == nil {
		return
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

// populateIndexesRail loads the index list for (schema, table) via
// ConnectHelper.LoadIndexes and pushes the result onto IndexesContext so
// the INDEXES rail draws rows on the next layout frame. Mirrors
// populateColumnsRail — wired alongside it from the TABLES-rail <CR>
// composite worker (dbsavvy-56u.1).
//
// Best-effort: a LoadIndexes error is logged and swallowed; the existing
// IndexesContext.items are left intact so a transient failure does not
// blank a previously-loaded list. Empty schema/table is a silent no-op.
func (c *connectInvoker) populateIndexesRail(ctx context.Context, schema, table string) {
	if c == nil || c.g == nil || c.helper == nil {
		return
	}
	if schema == "" || table == "" {
		return
	}
	if c.g.registry == nil || c.g.registry.Indexes == nil {
		return
	}
	idxs, err := c.helper.LoadIndexes(ctx, schema, table)
	if err != nil {
		c.g.deps.Common.Logger().Warn(fmt.Sprintf("gui: load indexes for %s.%s: %v", schema, table, err))
		return
	}
	items := make([]any, len(idxs))
	for i := range idxs {
		items[i] = idxs[i]
	}
	c.g.registry.Indexes.SetItems(items)
}

// loadQueryEditorBuffer is the I/O phase of the dbsavvy-wwd.9 post-Connect
// hook. It resolves (or generates) the persistent buffer UUID for the
// active connection via AppState.LastBufferUUIDs and loads the on-disk
// buffer (or a fresh empty Buffer when missing). It performs NO context
// write (SetBuffer) so it is safe to run on the worker goroutine; the
// disk read does not block the MainLoop. publishQueryEditorBuffer does the
// write on the UI thread (dbsavvy-fow.1). Missing Common / registry /
// profile are silent no-ops (false ok) so test wiring without persistence
// still passes through.
func (c *connectInvoker) loadQueryEditorBuffer(profile *models.Connection) (*editor.Buffer, bool) {
	if c == nil || c.g == nil || profile == nil {
		return nil, false
	}
	if c.g.deps.Common == nil {
		return nil, false
	}
	common := c.g.deps.Common
	if c.g.registry == nil || c.g.registry.QueryEditor == nil {
		return nil, false
	}
	appState := common.AppState
	if appState == nil {
		return nil, false
	}
	connID := profile.Name
	uuid := appState.GetOrCreateBufferUUID(connID)
	if uuid == "" {
		return nil, false
	}
	buf, err := editor.LoadBuffer(common.Fs, common.StateDir, connID, uuid)
	if err != nil {
		common.Logger().Warn(fmt.Sprintf("gui: load query-editor buffer for %q: %v", connID, err))
		return nil, false
	}
	return buf, true
}

// publishQueryEditorBuffer is the UI-thread publish phase paired with
// loadQueryEditorBuffer: it injects the loaded buffer into the live
// QueryEditorContext. SetBuffer is mutex-free on QueryEditorContext and
// the MainLoop renders the buffer every frame, so this MUST run on the UI
// thread to serialise with those reads (dbsavvy-fow.1). The swapped
// *editor.Buffer's own sync.RWMutex serialises subsequent edits. A false
// ok (load skipped/failed) is a no-op.
func (c *connectInvoker) publishQueryEditorBuffer(buf *editor.Buffer, ok bool) {
	if c == nil || !ok || buf == nil || c.g == nil {
		return
	}
	if c.g.registry == nil || c.g.registry.QueryEditor == nil {
		return
	}
	c.g.registry.QueryEditor.SetBuffer(buf)
}

// queryRuntime carries the result of the wireQueryRuntime I/O phase
// (worker goroutine) so the publish phase (UI thread) can Bind the runner
// and stash the SQLSession on the Gui without re-acquiring. A nil sqlSess
// means there was nothing to wire (runner/conn/profile absent — test
// wiring); the publish phase then no-ops. dbsavvy-fow.1.
type queryRuntime struct {
	sqlSess *session.SQLSession
	caps    drivers.Capabilities
}

// wireQueryRuntimeIO is the I/O phase of wiring the query runtime: it
// acquires the second drivers.Session, derives the driver capabilities,
// and builds the SQLSession with the orchestrator's History as recorder.
// It performs NO GUI-state writes (no QueryRunner.Bind, no
// g.activeSQLSession assignment) — those run on the UI thread in
// publishQueryRuntime so the MainLoop's reads of activeSQLSession
// serialise with the publication. Runs on the worker goroutine
// (dbsavvy-fow.1).
func (c *connectInvoker) wireQueryRuntimeIO(ctx context.Context, conn drivers.Connection, profile *models.Connection) (queryRuntime, error) {
	if c.runner == nil || conn == nil || profile == nil {
		return queryRuntime{}, nil
	}
	caps, capsErr := capsForDriver(ctx, profile.Driver)
	if capsErr != nil {
		return queryRuntime{}, fmt.Errorf("orchestrator: derive capabilities: %w", capsErr)
	}
	sessInner, err := conn.AcquireSession(ctx)
	if err != nil {
		return queryRuntime{}, fmt.Errorf("orchestrator: acquire query session: %w", err)
	}
	opts := session.Options{}
	if c.g != nil {
		opts.Logger = c.g.deps.Common.Logger()
	}
	if c.history != nil {
		opts.HistoryRecorder = c.history.AsSessionRecorder(profile.Name)
	}
	sqlSess := session.New(conn, sessInner, opts)
	return queryRuntime{sqlSess: sqlSess, caps: caps}, nil
}

// publishQueryRuntime is the UI-thread publish phase paired with
// wireQueryRuntimeIO: it Bind()s the QueryRunner and stashes the
// SQLSession on the Gui so Close can cancel an in-flight Stream. MUST run
// on the UI thread (the MainLoop reads g.activeSQLSession every frame).
// QueryRunner.Bind is itself atomic, but g.activeSQLSession is a plain
// field, so this serialises with render reads. A zero queryRuntime
// (nil sqlSess) is a no-op. dbsavvy-fow.1.
func (c *connectInvoker) publishQueryRuntime(rt queryRuntime) {
	if c == nil || rt.sqlSess == nil {
		return
	}
	if c.runner != nil {
		c.runner.Bind(rt.sqlSess, rt.caps)
	}
	if c.g != nil {
		c.g.activeSQLSession = rt.sqlSess
	}
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
