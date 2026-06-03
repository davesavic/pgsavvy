package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
)

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
	// dbsavvy-ioaj note 3: drop any active rail search on a show-hidden
	// toggle (UI-thread path, race-free) so n/N can't park the cursor on
	// a now-hidden row. Must precede the HandleRender kick below.
	a.registry.ClearSearch()
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

	// mu guards cancelFn + current, which the MainLoop-serialised
	// startAttempt / Cancel / Retry seams read and write (epic
	// dbsavvy-e53.5).
	mu sync.Mutex
	// cancelFn aborts the in-flight UI dial's ctx. Set by startAttempt,
	// cleared+called by Cancel, released by the worker closure on
	// completion. Nil when no UI attempt is in flight.
	cancelFn context.CancelFunc
	// current is the profile of the most recent UI attempt, so Retry can
	// re-dial it from the error state.
	current *models.Connection
}

func (c *connectInvoker) Connect(ctx context.Context, profile *models.Connection) error {
	if c == nil || c.helper == nil {
		return nil
	}
	// Supersession token (dbsavvy-fow.1): bump on entry and capture the
	// new value. This is the reconnect / direct-Connect path; UI-initiated
	// attempts go through startAttempt (which bumps on the MainLoop). A
	// later activation bumps it again; on completion we compare via
	// isStaleConnect and drop the result if a newer connect has started,
	// so a slow/timed-out dial can't clobber a more recent connection.
	var gen uint64
	if c.g != nil {
		gen = c.g.connectGen.Add(1)
	}
	return c.connectWithGen(ctx, profile, gen)
}

// startAttempt is the single shared UI dial path. It MUST be invoked on the
// gocui MainLoop: the connectGen bump and the Cancel bump both run there, so
// Cancel's bump is always strictly higher than the attempt's gen and the
// worker is reliably superseded (epic dbsavvy-e53.5 — the critical cancel
// race fix; do NOT move the bump onto the worker for UI attempts).
func (c *connectInvoker) startAttempt(profile *models.Connection) {
	if c == nil || c.g == nil || profile == nil {
		return
	}
	gen := c.g.connectGen.Add(1)
	// Cancel-only, no deadline: the network connect budget lives in the pg
	// driver (connectTimeout), applied AFTER interactive credential prompts so
	// a human typing a passphrase is not charged against the dial budget (epic
	// dbsavvy-t60w). cancel still drives Cancel/supersession.
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancelFn = cancel
	c.current = profile
	c.mu.Unlock()
	// Plain setter, MainLoop-safe; also clears any prior error so a retry
	// re-enters the connecting state. Always writes the modal's
	// ConnectingState sink (dbsavvy-bsh: standalone CONNECTING retired).
	if sink := c.connectingSink(true); sink != nil {
		sink.SetConnecting(profile.Name)
	}
	c.g.OnWorker(func(_ gocui.Task) error {
		// Release the timeout ctx when the attempt finishes. Idempotent
		// with Cancel (calling a CancelFunc twice is a no-op).
		defer cancel()
		// OnWorker logs a returned error, so the worker-lane breadcrumb is
		// preserved without a separate sink here.
		return c.connectWithGen(ctx, profile, gen)
	})
}

// startModalAttempt is the CONNECTION_MANAGER modal's connect entry point
// (dbsavvy-1rf). It marks the attempt as modal-origin (so the connecting body
// renders inside the modal and a successful publish pops it), flips the modal
// into connecting mode, then dials via the SHARED startAttempt path — the
// gen/supersession/cancel/worker logic is unchanged. MUST run on the MainLoop.
func (c *connectInvoker) startModalAttempt(profile *models.Connection) {
	if c == nil || profile == nil {
		return
	}
	if c.g != nil && c.g.registry != nil && c.g.registry.ConnectionManager != nil {
		c.g.registry.ConnectionManager.SetMode(guicontext.ModeConnecting)
	}
	c.startAttempt(profile)
}

// connectingStateSink is the narrow connecting/error write surface shared by
// the standalone CONNECTING screen and the modal's ConnectingState
// (dbsavvy-1rf).
type connectingStateSink interface {
	SetConnecting(name string)
	SetError(msg string)
}

// connectingSink returns the write target for the connecting/error body:
// always the CONNECTION_MANAGER modal's ConnectingState (standalone
// CONNECTING was retired by dbsavvy-bsh). The modal parameter is retained
// for signature compatibility with callers but is unused. Nil when the
// modal context is unwired (test fixtures).
func (c *connectInvoker) connectingSink(_ bool) connectingStateSink {
	if c == nil || c.g == nil || c.g.registry == nil {
		return nil
	}
	if c.g.registry.ConnectionManager == nil {
		return nil
	}
	return c.g.registry.ConnectionManager.ConnectingState()
}

// Cancel supersedes the in-flight UI attempt and aborts its dial. Invoked
// on the MainLoop via the Esc seam: the connectGen bump here is strictly
// higher than the bump startAttempt made (both serialised on the loop), so
// the in-flight worker finds itself stale and publishes nothing (epic
// dbsavvy-e53.5).
func (c *connectInvoker) Cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cf := c.cancelFn
	c.cancelFn = nil
	c.mu.Unlock()
	if c.g != nil {
		c.g.connectGen.Add(1)
	}
	if cf != nil {
		cf()
	}
}

// Retry re-attempts the most recent UI profile from the error state.
// Invoked on the MainLoop via the [r] seam; CONNECTING is already top so no
// re-push is needed, and startAttempt's SetConnecting clears the error.
func (c *connectInvoker) Retry() {
	c.mu.Lock()
	p := c.current
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.startAttempt(p)
}

func (c *connectInvoker) connectWithGen(ctx context.Context, profile *models.Connection, gen uint64) error {
	// --- WORKER PHASE: all blocking I/O runs here (Connect itself runs on
	// the worker goroutine — connections_controller.go schedules it via
	// OnWorker). Nothing in this phase writes GUI state the MainLoop reads;
	// results are collected into locals and published in the single
	// OnUIThread closure below (dbsavvy-fow.1).
	conn, _, err := c.helper.Connect(ctx, profile)
	if err != nil {
		c.routeConnectError(gen, err)
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
				c.publishConnectError(gen, err)
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

	// dbsavvy-dl7.4: direct-load saved schema/table state on worker.
	savedSchemaIdx, tableItems, savedTableIdx := c.loadSavedSchemaTableState(
		ctx, profile, schemaItems, schemaOK)

	// dbsavvy-56u.1: stamp LastConnectionID and prepend the profile to
	// the LIFO RecentConnectionIDs ring (deduped, capped at 10). Persisted
	// AFTER wireQueryRuntimeIO succeeds so a wiring rollback does not leave
	// a debounced write pointing at a profile that failed to connect.
	// MutateAndSave is independently synchronized and touches no gocui view
	// state, so it stays on the worker (dbsavvy-fow.1).
	// Stale-gated (epic dbsavvy-e53.5): a cancel-after-successful-dial bumps
	// gen, so a superseded attempt must NOT stamp persisted state.
	if profile != nil && c.g != nil && c.g.deps.Store != nil && !c.isStaleConnect(gen) {
		name := profile.Name
		c.g.deps.Store.MutateAndSave(func(a *common.AppState) {
			a.LastConnectionID = name
			a.RecentConnectionIDs = common.PushRecentConnectionID(a.RecentConnectionIDs, name)
		})
	}

	// hq5.12: replay persisted session settings (search_path,
	// statement_timeout, timezone, application_name) on the fresh session.
	// Runs on the worker — the toast hint is published in the UI closure.
	// Stale-gated (epic dbsavvy-e53.5): a superseded attempt must NOT replay
	// SET commands on the doomed session.
	var restoreHint string
	if profile != nil && rt.sqlSess != nil && !c.isStaleConnect(gen) {
		restoreHint = c.restoreSessionSettings(ctx, rt.sqlSess, profile.Name)
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
		// dbsavvy-1rf: a modal-origin connect renders its connecting body
		// inside the CONNECTION_MANAGER modal. On success pop the modal (and
		// reset it back to list mode for a later re-open) BEFORE pushing the
		// schemas/tables rails so the user lands in restored navigation with
		// the modal gone.
		c.popModalOnSuccess()
		// dbsavvy-dl7.4: restore schema cursor + publish tables.
		if savedSchemaIdx >= 0 && c.g.registry != nil && c.g.registry.Schemas != nil {
			c.g.registry.Schemas.SetCursor(savedSchemaIdx)
		}
		if tableItems != nil && c.g.registry != nil && c.g.registry.Tables != nil {
			c.g.registry.Tables.SetItems(tableItems)
			if savedTableIdx >= 0 {
				c.g.registry.Tables.SetCursor(savedTableIdx)
			}
		}
		// hq5.12: show restore toast on the UI thread.
		if restoreHint != "" && c.g.toastHelp != nil {
			c.g.toastHelp.Show(restoreHint, 4*time.Second)
		}
		// dbsavvy-yea: land focus in the query editor on connection open,
		// not the side rail. Push the rail first (SIDE_CONTEXT) so it is
		// populated and rendered, then push the query editor (MAIN_CONTEXT)
		// on top so it holds focus and the cursor starts there.
		if tableItems != nil && c.g != nil && c.g.registry != nil && c.g.registry.Tables != nil {
			if err := c.g.tree.Push(c.g.registry.Tables); err != nil {
				return err
			}
		} else if c.g != nil && c.g.registry != nil && c.g.registry.Schemas != nil &&
			len(c.g.registry.Schemas.Items()) != 0 {
			if err := c.g.tree.Push(c.g.registry.Schemas); err != nil {
				return err
			}
		}
		if c.g != nil && c.g.registry != nil && c.g.registry.QueryEditor != nil {
			return c.g.tree.Push(c.g.registry.QueryEditor)
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

// routeConnectError marshals the failure onto the UI thread and paints it
// on the CONNECTING screen via publishConnectError. Used by the dial-error
// branch (the wiring-rollback branch already owns a UI closure and calls
// publishConnectError inline). Stale-gen guarded both before scheduling and
// inside the closure (the gen could be bumped between the two), so a
// superseded worker never paints the live screen (epic dbsavvy-e53).
func (c *connectInvoker) routeConnectError(gen uint64, err error) {
	if c.isStaleConnect(gen) {
		return
	}
	c.runOnUIThread(func() error {
		c.publishConnectError(gen, err)
		return nil
	})
}

// publishConnectError sets the CONNECTING screen into its error state with
// the sanitized message. MUST run on the UI thread (SetError is a plain
// setter the MainLoop reads in HandleRender). No-ops when the worker is
// stale (superseded by a newer activation) or when CONNECTING is no longer
// top of the focus stack — a cancel/retry may have popped it, and writing a
// dead screen would be a leak. Credentials are redacted (URL + kv forms)
// THEN control bytes stripped before reaching the screen (SECURITY: never
// surface a raw err). epic dbsavvy-e53.
func (c *connectInvoker) publishConnectError(gen uint64, err error) {
	if err == nil || c.isStaleConnect(gen) {
		return
	}
	if c.g == nil || c.g.tree == nil {
		return
	}
	// The error must only paint the screen that is actually top of the
	// focus stack — a cancel/retry may have popped it, and writing a dead
	// screen would be a leak (dbsavvy-bsh: always CONNECTION_MANAGER).
	if top := c.g.tree.Current(); top == nil || top.GetKey() != types.CONNECTION_MANAGER {
		return
	}
	sink := c.connectingSink(true)
	if sink == nil {
		return
	}
	msg := config.SafeText(session.RedactConnectionString(connectErrMessage(err)))
	sink.SetError(msg)
}

// popModalOnSuccess pops the CONNECTION_MANAGER modal off the focus stack and
// resets it to list mode after a modal-origin connect succeeds (dbsavvy-1rf).
// No-op for standalone CONNECTING-origin connects, or when the registry/tree
// is unwired. MUST run on the UI thread (called from the publish closure).
func (c *connectInvoker) popModalOnSuccess() {
	if c == nil || c.g == nil || c.g.registry == nil || c.g.registry.ConnectionManager == nil || c.g.tree == nil {
		return
	}
	c.g.registry.ConnectionManager.SetMode(guicontext.ModeList)
	_ = c.g.tree.PopIfTop(types.CONNECTION_MANAGER)
}

// connectErrMessage returns the user-facing string for a Connect error.
// Rewrites the data-layer "already connected" sentinel into a friendlier
// short phrase (dbsavvy-e9i); every other error is surfaced verbatim. The
// caller redacts + sanitizes the returned string before it reaches the
// CONNECTING screen.
func connectErrMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// data.ConnectHelper raises "data: already connected (call Disconnect
	// first)" when <cr> hits a profile that's already open. From the user's
	// perspective this is a no-op, not an error.
	if strings.Contains(msg, "already connected") {
		return "already connected"
	}
	return msg
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
//
// dbsavvy-zt9: this runs on a WORKER goroutine, so the publishSchemaItems
// call (a mutex-free SetItems write the MainLoop reads every frame) is
// marshaled onto the UI thread via runOnUIThread, mirroring the
// load-on-worker / publish-on-UI-thread split (dbsavvy-fow.1). The other
// publishSchemaItems caller (the connect path) is already on the UI thread
// and stays raw, so the marshal lives here at the worker call site rather
// than inside publishSchemaItems.
func (c *connectInvoker) populateSchemasRail(ctx context.Context) {
	items, ok := c.loadSchemaItems(ctx)
	if !ok {
		return
	}
	c.runOnUIThread(func() error {
		c.publishSchemaItems(items, ok)
		return nil
	})
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

// loadSavedSchemaTableState finds the saved schema in schemaItems and, if
// found, loads its tables via LoadTables (direct-load pattern). Returns the
// schema cursor index (-1 if not found), table items (nil if not loaded), and
// table cursor index (-1 if not found). All failures are no-ops. dbsavvy-dl7.4.
func (c *connectInvoker) loadSavedSchemaTableState(
	ctx context.Context, profile *models.Connection, schemaItems []any, schemaOK bool,
) (schemaIdx int, tableItems []any, tableIdx int) {
	schemaIdx, tableIdx = -1, -1
	if !schemaOK || len(schemaItems) == 0 || profile == nil {
		return
	}
	if c.g == nil || c.g.deps.Store == nil {
		return
	}
	connID := profile.Name
	savedSchema := c.g.deps.Store.LastSchemaNameSnapshot(connID)
	if savedSchema == "" {
		return
	}
	for i, it := range schemaItems {
		s, ok := it.(models.Schema)
		if !ok {
			continue
		}
		if s.Name == savedSchema {
			schemaIdx = i
			break
		}
	}
	if schemaIdx < 0 {
		return
	}
	tables, err := c.helper.LoadTables(ctx, savedSchema)
	if err != nil {
		c.g.deps.Common.Logger().Warn("gui: direct-load tables for saved schema",
			"schema", savedSchema, "err", err)
		return
	}
	tableItems = make([]any, len(tables))
	for i := range tables {
		tableItems[i] = tables[i]
	}
	savedTable := c.g.deps.Store.LastTableNameSnapshot(connID)
	if savedTable == "" {
		return
	}
	for i, it := range tableItems {
		t, ok := it.(*models.Table)
		if !ok || t == nil {
			continue
		}
		if t.Name == savedTable {
			tableIdx = i
			break
		}
	}
	return
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
//
// dbsavvy-zt9: this runs on a WORKER goroutine, so the SetItems publish (a
// mutex-free items+cursor write the MainLoop reads every frame) is marshaled
// onto the UI thread via runOnUIThread to serialise with render-frame reads,
// mirroring the load-on-worker / publish-on-UI-thread split (dbsavvy-fow.1).
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
	tablesCtx := c.g.registry.Tables
	c.runOnUIThread(func() error {
		tablesCtx.SetItems(items)
		return nil
	})
}

// populateColumnsRail loads the column list for (schema, table) via
// ConnectHelper.LoadColumns and pushes the result onto ColumnsContext so
// the COLUMNS rail draws rows on the next layout frame. Wired to the
// TABLES-rail <CR> handler via HelperBag.OnTableActivate.
//
// Best-effort: a LoadColumns error is logged and swallowed; the existing
// ColumnsContext.items are left intact so a transient failure does not
// blank a previously-loaded list. Empty schema/table is a silent no-op.
//
// dbsavvy-zt9: this runs on a WORKER goroutine, so the SetItems publish (a
// mutex-free items+cursor write the MainLoop reads every frame) is marshaled
// onto the UI thread via runOnUIThread to serialise with render-frame reads,
// mirroring the load-on-worker / publish-on-UI-thread split (dbsavvy-fow.1).
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
	columnsCtx := c.g.registry.Columns
	c.runOnUIThread(func() error {
		columnsCtx.SetItems(items)
		return nil
	})
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
//
// dbsavvy-zt9: this runs on a WORKER goroutine, so the SetItems publish (a
// mutex-free items+cursor write the MainLoop reads every frame) is marshaled
// onto the UI thread via runOnUIThread to serialise with render-frame reads,
// mirroring the load-on-worker / publish-on-UI-thread split (dbsavvy-fow.1).
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
	indexesCtx := c.g.registry.Indexes
	c.runOnUIThread(func() error {
		indexesCtx.SetItems(items)
		return nil
	})
}

// loadQueryEditorBuffer is the I/O phase of the dbsavvy-wwd.9 post-Connect
// hook. It resolves (or generates) the persistent buffer UUID for the
// active connection via AppStateStore.GetOrCreateBufferUUID and loads the
// on-disk buffer (or a fresh empty Buffer when missing). The UUID lookup
// MUST go through the store (not Common.AppState, which is an unwired
// empty literal that never reaches disk — dbsavvy-lrh) so the same UUID
// is reused across runs and previously-persisted .sql files are picked up
// instead of orphaned. It performs NO context write (SetBuffer) so it is
// safe to run on the worker goroutine; the disk read does not block the
// MainLoop. publishQueryEditorBuffer does the write on the UI thread
// (dbsavvy-fow.1). Missing Common / Store / registry / profile are silent
// no-ops (false ok) so test wiring without persistence still passes
// through.
func (c *connectInvoker) loadQueryEditorBuffer(profile *models.Connection) (*editor.Buffer, bool) {
	if c == nil || c.g == nil || profile == nil {
		return nil, false
	}
	if c.g.deps.Common == nil || c.g.deps.Store == nil {
		return nil, false
	}
	common := c.g.deps.Common
	if c.g.registry == nil || c.g.registry.QueryEditor == nil {
		return nil, false
	}
	connID := profile.Name
	uuid := c.g.deps.Store.GetOrCreateBufferUUID(connID)
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

// restoreSessionSettings reads persisted session settings from AppState and
// replays allowed SET commands on the freshly opened SQLSession. Returns a
// human-readable hint listing restored settings (empty when nothing restored).
// Failures are logged and skipped — a partial restore is better than aborting
// the connect. Runs on the worker goroutine (I/O phase). hq5.12.
func (c *connectInvoker) restoreSessionSettings(ctx context.Context, sess *session.SQLSession, connID string) string {
	if sess == nil || connID == "" || c.g == nil || c.g.deps.Store == nil {
		return ""
	}
	store := c.g.deps.Store

	saved := store.LastSessionSettingsSnapshot(connID)
	if saved == nil {
		saved = make(map[string]string)
	}
	if to := store.StatementTimeoutOverrideValue(connID); to != "" {
		saved["statement_timeout"] = to
	}

	return replaySessionSettings(ctx, saved, func(ctx context.Context, sql string) error {
		// Restoration SETs are internal bootstrap, not user queries — keep
		// them out of query history. A SET the user types themselves still
		// records normally (this only suppresses the replay path).
		_, err := sess.Execute(session.WithoutLogging(ctx), models.Query{SQL: sql})
		return err
	}, sess.SettingsSnapshot(), c.g.deps.Common.Logger(), connID)
}

// gucAllowlist gates which GUC settings are replayed on session restore.
// Role is excluded for security (defense against tampered persisted state).
var gucAllowlist = map[string]bool{
	"search_path":       true,
	"statement_timeout": true,
	"timezone":          true,
	"application_name":  true,
}

// replaySessionSettings builds safe SET SQL for each allowed setting and
// executes it. search_path schemas are identifier-quoted; statement_timeout
// is canonicalized; string settings are single-quote escaped. Returns a
// toast hint listing restored settings, or "" when nothing was restored.
func replaySessionSettings(
	ctx context.Context,
	saved map[string]string,
	exec func(ctx context.Context, sql string) error,
	snap *session.SettingsSnapshot,
	log *slog.Logger,
	connID string,
) string {
	var keys []string
	for k := range saved {
		if gucAllowlist[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)

	var restored []string
	for _, key := range keys {
		val := saved[key]
		if val == "" {
			continue
		}

		var sql string
		switch key {
		case "search_path":
			parts := strings.Split(val, ",")
			var quoted []string
			for _, s := range parts {
				s = strings.TrimSpace(s)
				s = strings.Trim(s, `"`)
				if s == "" {
					continue
				}
				quoted = append(quoted, `"`+strings.ReplaceAll(s, `"`, `""`)+`"`)
			}
			if len(quoted) == 0 {
				continue
			}
			sql = "SET search_path TO " + strings.Join(quoted, ", ")
		case "statement_timeout":
			canon, err := session.CanonicalizeStatementTimeout(val)
			if err != nil {
				log.Warn("gui: restore setting: bad statement_timeout", "connection_id", connID, "val", val, "err", err)
				continue
			}
			sql = "SET statement_timeout = '" + canon + "'"
		default:
			sql = "SET " + key + " TO '" + strings.ReplaceAll(val, "'", "''") + "'"
		}

		if err := exec(ctx, sql); err != nil {
			log.Warn("gui: restore setting failed", "connection_id", connID, "key", key, "sql", sql, "err", err)
			continue
		}
		snap.Set(key, val)
		restored = append(restored, key+"="+val)
	}

	if len(restored) == 0 {
		return ""
	}
	return "restored: " + strings.Join(restored, ", ")
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
			// Re-seed the CONNECTION_MANAGER modal list on the UI thread
			// so the new profile shows up. The onComplete callback fires
			// on the worker goroutine; routing through OnUIThread keeps
			// it ordered with the next render frame.
			c.g.OnUIThread(func() error {
				c.g.refreshConnectionManagerRail()
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

// reconnectInvoker adapts ConnectHelper + connectInvoker into the
// narrow ReconnectInvoker surface the ReconnectController consumes
// (hq5.7). PingConnection issues a lightweight pool-level round-trip;
// Reconnect tears down both sessions (schema-rail + query) and
// re-opens with the same profile via the full connectInvoker.Connect
// pathway (which wires the QueryRunner, reloads schemas, etc.).
type reconnectInvoker struct {
	helper *data.ConnectHelper
	inv    *connectInvoker
}

// PingConnection issues a pool-level Ping against the live
// drivers.Connection. Returns an error when the helper is not connected
// or the Ping fails.
func (r *reconnectInvoker) PingConnection(ctx context.Context) error {
	if r.helper == nil {
		return fmt.Errorf("reconnect: no connect helper")
	}
	conn := r.helper.Connection()
	if conn == nil {
		return fmt.Errorf("reconnect: not connected")
	}
	return conn.Ping(ctx)
}

// Reconnect tears down the current connection and re-opens with the
// supplied profile. The full connectInvoker.Connect path wires the
// QueryRunner, loads schemas, and pushes focus.
func (r *reconnectInvoker) Reconnect(ctx context.Context, profile *models.Connection) error {
	if r.helper == nil || r.inv == nil {
		return fmt.Errorf("reconnect: not wired")
	}
	// Tear down the query session FIRST. SQLSession.Close releases its
	// inner pool conn; if we close the pool first (helper.Disconnect) the
	// pool's Close blocks forever waiting for that outstanding conn to be
	// released, deadlocking the reconnect. dbsavvy-txb.
	if r.inv.g != nil && r.inv.g.activeSQLSession != nil {
		_ = r.inv.g.activeSQLSession.Close()
		r.inv.g.activeSQLSession = nil
	}
	// Tear down the schema-rail session + pool. This also satisfies the
	// "data: already connected (call Disconnect first)" guard in Connect.
	r.helper.Disconnect()
	return r.inv.Connect(ctx, profile)
}

// Compile-time assertions: all adapters satisfy their target interfaces.
var (
	_ controllers.SchemaPicker          = schemasPickerAdapter{}
	_ controllers.TablePicker           = tablesPickerAdapter{}
	_ controllers.ActiveConnection      = (*activeConnAdapter)(nil)
	_ controllers.ConnectInvoker        = (*connectInvoker)(nil)
	_ controllers.ConnectionFormInvoker = (*connectionFormInvoker)(nil)
	_ controllers.MenuPushHelper        = (*menuPushHelper)(nil)
	_ controllers.ReconnectInvoker      = (*reconnectInvoker)(nil)
)
