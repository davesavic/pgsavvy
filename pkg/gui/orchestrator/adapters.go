package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
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
	// Drop any active rail search on a show-hidden
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
	return a.g.connectionState.activeConnID
}

// connectInvoker is the controllers.ConnectInvoker facade. It calls the
// real data.ConnectHelper.Connect, stashes the active connection ID on
// the Gui so SchemasInvoker can scope its AppState keys, and wires the
// fresh SQLSession into the orchestrator-owned QueryRunner.
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
	// startAttempt / Cancel / Retry seams read and write.
	mu sync.Mutex
	// cancelFn aborts the in-flight UI dial's ctx. Set by startAttempt,
	// cleared+called by Cancel, released by the worker closure on
	// completion. Nil when no UI attempt is in flight.
	cancelFn context.CancelFunc
	// current is the profile of the most recent UI attempt, so Retry can
	// re-dial it from the error state.
	current *models.Connection

	// testGen is a DEDICATED supersession token for the form's test-connection
	// action, kept SEPARATE from connectionState.connectGen so a real connect
	// and an in-progress test never cancel each other. Bumped on every test
	// attempt; the worker's late inline publish is dropped when its captured
	// gen no longer matches (mirrors isStaleConnect, but on its own counter).
	testGen atomic.Uint64
	// testCancelFn aborts the in-flight test dial's ctx. Set when a test
	// attempt starts, cleared+called when a newer test supersedes it. Guarded
	// by mu (the same lock that guards cancelFn/current).
	testCancelFn context.CancelFunc
}

func (c *connectInvoker) Connect(ctx context.Context, profile *models.Connection) error {
	if c == nil || c.helper == nil {
		return nil
	}
	// Supersession token: bump on entry and capture the
	// new value. This is the reconnect / direct-Connect path; UI-initiated
	// attempts go through startAttempt (which bumps on the MainLoop). A
	// later activation bumps it again; on completion we compare via
	// isStaleConnect and drop the result if a newer connect has started,
	// so a slow/timed-out dial can't clobber a more recent connection.
	var gen uint64
	if c.g != nil {
		gen = c.g.connectionState.connectGen.Add(1)
	}
	// T3 AD8: the direct / reconnect path passes a NIL reporter — no modal is
	// open to render a staged checklist, so no stage writes happen here.
	return c.connectWithGen(ctx, profile, gen, nil)
}

// startAttempt is the single shared UI dial path. It MUST be invoked on the
// gocui MainLoop: the connectGen bump and the Cancel bump both run there, so
// Cancel's bump is always strictly higher than the attempt's gen and the
// worker is reliably superseded (the critical cancel
// race fix; do NOT move the bump onto the worker for UI attempts).
func (c *connectInvoker) startAttempt(profile *models.Connection) {
	if c == nil || c.g == nil || profile == nil {
		return
	}
	gen := c.g.connectionState.connectGen.Add(1)
	// Cancel-only, no deadline: the network connect budget lives in the pg
	// driver (connectTimeout), applied AFTER interactive credential prompts so
	// a human typing a passphrase is not charged against the dial budget.
	// cancel still drives Cancel/supersession.
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancelFn = cancel
	c.current = profile
	c.mu.Unlock()
	// Plain setter, MainLoop-safe; also clears any prior error so a retry
	// re-enters the connecting state. Seeds the staged checklist (Tunnel only
	// when the profile carries one; Auth + Objects always) with the first
	// stage Active so a retry starts fresh — no stale ✓/✗ rows (T3). Always
	// writes the modal's ConnectingState sink (standalone
	// CONNECTING retired).
	if sink := c.connectingSink(true); sink != nil {
		sink.SetConnectingStaged(profile.Name, buildStages(profile))
	}
	// T3 AD8: build a LIVE reporter ONLY on the modal/Retry path. It holds c +
	// gen and marshals every stage transition onto the MainLoop with a
	// stale-gen recheck (see connectReporter.Report). The direct/reconnect
	// path (connectInvoker.Connect → connectWithGen) passes a nil reporter, so
	// no stage writes happen there.
	reporter := &connectReporter{c: c, gen: gen}
	c.g.OnWorker(func(_ gocui.Task) error {
		// Release the timeout ctx when the attempt finishes. Idempotent
		// with Cancel (calling a CancelFunc twice is a no-op).
		defer cancel()
		// OnWorker logs a returned error, so the worker-lane breadcrumb is
		// preserved without a separate sink here.
		return c.connectWithGen(ctx, profile, gen, reporter)
	})
}

// buildStages returns the connect-lifecycle checklist for profile: the Tunnel
// row only when the profile carries an SSH tunnel (the driver emits StageTunnel
// only in that case), then Auth, then Objects. The list is all-Pending with the
// FIRST stage flipped Active so the initial render already shows progress (T3).
// Labels are constant English (the modal does not thread i18n); the Objects
// row is relabelled to "Loaded" on success so its done line reads
// "✓ Loaded N schemas".
func buildStages(profile *models.Connection) []guicontext.Stage {
	var stages []guicontext.Stage
	if profile != nil && profile.SSHTunnel != nil {
		stages = append(stages, guicontext.Stage{ID: guicontext.StageTunnel, Label: "SSH tunnel"})
	}
	stages = append(stages,
		guicontext.Stage{ID: guicontext.StageAuth, Label: "Authenticated"},
		guicontext.Stage{ID: guicontext.StageObjects, Label: "Loading objects…"},
	)
	stages[0].Status = guicontext.StageActive
	return stages
}

// connectReporter is the LIVE drivers.ProgressReporter handed to
// helper.Connect on the modal/Retry path (T3 AD8). Report runs on the WORKER
// goroutine that drives Driver.Open, so it MUST NOT touch ConnectingState
// directly: it marshals every transition onto the MainLoop via
// runOnUIThread and re-checks isStaleConnect(gen) INSIDE the closure — the
// same double-guard routeConnectError uses — so a superseded attempt's late
// Report never writes a newer attempt's modal (T3 AD4). The gen is captured at
// construction; a nil c/sink collapses to a no-op.
type connectReporter struct {
	c   *connectInvoker
	gen uint64
}

// Report maps a driver ConnectStage onto the modal checklist, performing each
// stage's Done+Active transition atomically in ONE marshalled, gen-guarded
// closure (T3 AD4):
//   - StageTunnel       → Tunnel Done, Auth Active.
//   - StageAuthenticated → Auth Done ONLY. Objects is activated LATE, just
//     before loadSchemaItems (T3 AD3), so a wireQueryRuntimeIO/AcquireSession
//     failure in the gap is not mis-rendered as "✗ Loading objects".
func (r *connectReporter) Report(stage drivers.ConnectStage) {
	if r == nil || r.c == nil {
		return
	}
	r.c.runOnUIThread(func() error {
		if r.c.isStaleConnect(r.gen) {
			return nil
		}
		sink := r.c.connectingSink(true)
		if sink == nil {
			return nil
		}
		r.apply(sink, stage)
		return nil
	})
}

// apply performs the stage's transitions on sink. Runs INSIDE Report's
// gen-guarded UI closure. A guard clause per known stage keeps the dispatch
// flat (no if/else chain); an unknown stage is a no-op.
func (r *connectReporter) apply(sink connectingStateSink, stage drivers.ConnectStage) {
	switch stage {
	case drivers.StageTunnel:
		sink.MarkStageDone(guicontext.StageTunnel, "")
		sink.MarkStageActive(guicontext.StageAuth)
	case drivers.StageAuthenticated:
		sink.MarkStageDone(guicontext.StageAuth, "")
	}
}

// schemaLoadTimeout caps the schema load that backs the Objects stage (T3
// AD7). It is intentionally separate from the pg driver's connectTimeout: a
// hung ListSchemas must fail into the Objects-✗ path rather than leave the
// modal stuck on "Loading objects…" forever. The attempt ctx is its parent so
// Esc cancel still propagates. A package var (not const) so the staged-progress
// test can shrink it to exercise the timeout path deterministically.
var schemaLoadTimeout = 15 * time.Second

// markStageActiveStaged marshals a single stage-Active transition onto the
// MainLoop with a stale-gen recheck INSIDE the closure (T3 AD3/AD4). A nil
// reporter (direct / reconnect path) is a no-op: those paths have no modal
// checklist to drive (AD8). Mirrors connectReporter.Report's marshalling.
func (c *connectInvoker) markStageActiveStaged(gen uint64, reporter drivers.ProgressReporter, id guicontext.StageID) {
	if reporter == nil {
		return
	}
	c.runOnUIThread(func() error {
		if c.isStaleConnect(gen) {
			return nil
		}
		sink := c.connectingSink(true)
		if sink == nil {
			return nil
		}
		sink.MarkStageActive(id)
		return nil
	})
}

// markObjectsTerminalStaged emits the terminal Objects row (T3 AD6b/AD7) in its
// OWN gen-guarded closure, submitted BEFORE the later publish closure that pops
// the modal. OnUIThread is FIFO, so the terminal row is applied to the sink
// before popModalOnSuccess — ordering is guaranteed. Whether it gets its own
// PAINT is best-effort: gocui batches queued UI events into a single flush, so
// on a fast connect (little worker work between the two submissions) the
// "✓ Loaded N schemas" frame may collapse into the same flush as the pop. On a
// slow/remote connect (the case this feature targets) the worker latency
// between submissions makes the terminal frame observable. Either way the state
// is never wrong — at worst a cosmetic frame is skipped.
// On success the Objects row is relabelled "Loaded" and marked Done with the
// "N schemas" detail so it reads "✓ Loaded N schemas"; on failure/timeout it is
// marked Failed (✗) while the connect itself still succeeds with an empty rail.
// A nil reporter is a no-op (direct / reconnect path, AD8).
func (c *connectInvoker) markObjectsTerminalStaged(gen uint64, reporter drivers.ProgressReporter, schemaItems []any, schemaOK bool) {
	if reporter == nil {
		return
	}
	c.runOnUIThread(func() error {
		if c.isStaleConnect(gen) {
			return nil
		}
		sink := c.connectingSink(true)
		if sink == nil {
			return nil
		}
		if !schemaOK {
			sink.MarkStageFailed(guicontext.StageObjects)
			return nil
		}
		sink.SetStageLabel(guicontext.StageObjects, "Loaded")
		sink.MarkStageDone(guicontext.StageObjects, schemaCountLabel(len(schemaItems)))
		return nil
	})
}

// schemaCountLabel renders the count suffix for the Objects done row:
// "1 schema" (singular) or "N schemas" (plural / zero), so the line reads
// "✓ Loaded 0 schemas" for an empty list (T3 success criteria).
func schemaCountLabel(n int) string {
	if n == 1 {
		return "1 schema"
	}
	return fmt.Sprintf("%d schemas", n)
}

// startModalAttempt is the CONNECTION_MANAGER modal's connect entry point.
// It marks the attempt as modal-origin (so the connecting body
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

// testConnection dials the IN-PROGRESS (unsaved) profile and publishes the
// pass/fail result INLINE in the connection form, WITHOUT touching the live
// active session (it bypasses ConnectHelper.Connect entirely and uses the
// decoupled drivers.Get → Open primitive, immediately closing the returned
// Connection). It does NOT pop the modal and is fully independent of save.
//
// Threading + supersession mirror startAttempt but on a DEDICATED test
// generation (c.testGen) + cancel (c.testCancelFn) so a real connect and a
// test never cancel each other. MUST be invoked on the MainLoop: the testGen
// bump runs there so a later test attempt's bump is strictly higher and the
// in-flight worker is reliably superseded. The blocking dial runs on the
// tracked OnWorker pool (no bare goroutine); the inline publish marshals back
// via OnUIThread guarded by the testGen staleness check.
func (c *connectInvoker) testConnection(profile *models.Connection) {
	if c == nil || c.g == nil || profile == nil {
		return
	}
	logger := c.g.deps.Common.Logger()
	logs.Event(logger, "db", "conn_test",
		slog.String("phase", "attempt"),
		slog.String("redacted_dsn", session.RedactConnectionString(profile.DSN)),
	)

	gen := c.testGen.Add(1)
	// Cancel-only, no deadline: the pg driver's connectTimeout bounds the
	// network phase; cancel drives supersession. Supersede any prior in-flight
	// test so two `t` presses don't race two dials onto the same form.
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.testCancelFn != nil {
		c.testCancelFn()
	}
	c.testCancelFn = cancel
	c.mu.Unlock()

	c.g.OnWorker(func(_ gocui.Task) error {
		defer cancel()
		err := dialProbe(ctx, profile)
		c.publishTestResult(gen, profile, err)
		return err
	})
}

// dialProbe performs the decoupled reachability+auth dial for the test action:
// drivers.Get → factory → Open (which opens any SSH tunnel, creates the pool,
// pings, and runs SELECT version(), closing the tunnel on every error path) →
// immediately Close the returned Connection so NO session is retained. It never
// touches the live ConnectHelper, pool, or active session. Returns the dial
// error verbatim (the caller redacts before surfacing/logging).
func dialProbe(ctx context.Context, profile *models.Connection) error {
	factory, err := drivers.Get(profile.Driver)
	if err != nil {
		return err
	}
	drv, err := factory(ctx)
	if err != nil {
		return err
	}
	conn, err := drv.Open(ctx, *profile, nil)
	if err != nil {
		return err
	}
	// Success: keep no session. Close the freshly-opened Connection (which
	// closes the pool and any SSH tunnel) and discard it.
	return conn.Close()
}

// publishTestResult marshals the test-connection outcome onto the UI thread and
// stamps the inline pass/fail line on the form, guarded by the testGen
// staleness check (mirrors connectInvoker.isStaleConnect on the dedicated test
// counter). A superseded test (the form closed, switched mode, or a newer test
// started) publishes NOTHING. The outcome is logged with the DSN redacted.
func (c *connectInvoker) publishTestResult(gen uint64, profile *models.Connection, dialErr error) {
	logger := c.g.deps.Common.Logger()
	if dialErr != nil {
		logs.Event(logger, "db", "conn_test",
			slog.String("phase", "outcome"),
			slog.Bool("ok", false),
			slog.String("redacted_dsn", session.RedactConnectionString(profile.DSN)),
			slog.String("err", session.RedactConnectionString(dialErr.Error())),
		)
	} else {
		logs.Event(logger, "db", "conn_test",
			slog.String("phase", "outcome"),
			slog.Bool("ok", true),
			slog.String("redacted_dsn", session.RedactConnectionString(profile.DSN)),
		)
	}

	c.runOnUIThread(func() error {
		if c.testStale(gen) {
			return nil
		}
		cm := c.g.registry.ConnectionManager
		if cm == nil || cm.Mode() != guicontext.ModeForm {
			return nil
		}
		if dialErr != nil {
			cm.FormSetError(testFailMessage(dialErr))
			return nil
		}
		cm.FormSetStatus("connection ok")
		return nil
	})
}

// testStale reports whether a test-connection that captured gen has been
// superseded by a newer test attempt. Always false when g is nil (test wiring
// without a Gui), matching isStaleConnect.
func (c *connectInvoker) testStale(gen uint64) bool {
	if c == nil || c.g == nil {
		return false
	}
	return c.testGen.Load() != gen
}

// testFailMessage builds the user-facing inline failure line for a failed test
// dial. Credentials are redacted then control bytes stripped before reaching
// the form (mirrors publishConnectError), so a raw DSN/password never surfaces.
func testFailMessage(err error) string {
	return config.SafeText(session.RedactConnectionString("test failed: " + err.Error()))
}

// connectingStateSink is the narrow connecting/error write surface shared by
// the standalone CONNECTING screen and the modal's ConnectingState.
type connectingStateSink interface {
	// SetConnectingStaged enters the connecting phase for name and REPLACES
	// the stage checklist so a retry starts fresh (no stale ✓/✗ rows). T3
	// migrated the modal off the zero-arg SetConnecting shim.
	SetConnectingStaged(name string, stages []guicontext.Stage)
	// MarkStageActive / MarkStageDone / MarkStageFailed transition a single
	// checklist row. Driven by the live ProgressReporter and the schema-load
	// boundary, all marshalled onto the MainLoop with a stale-gen recheck
	// (T3 AD4/AD7). SetStageLabel relabels the Objects row to its terminal
	// text ("Loaded") so the done line reads "✓ Loaded N schemas".
	MarkStageActive(id guicontext.StageID)
	MarkStageDone(id guicontext.StageID, detail string)
	MarkStageFailed(id guicontext.StageID)
	SetStageLabel(id guicontext.StageID, label string)
	SetError(msg string)
}

// connectingSink returns the write target for the connecting/error body:
// always the CONNECTION_MANAGER modal's ConnectingState (standalone
// CONNECTING was retired). The modal parameter is retained
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
// the in-flight worker finds itself stale and publishes nothing.
func (c *connectInvoker) Cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cf := c.cancelFn
	c.cancelFn = nil
	c.mu.Unlock()
	if c.g != nil {
		c.g.connectionState.connectGen.Add(1)
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

// teardownForSwitch disconnects the live connection when the incoming profile
// differs from the one currently active, so connectWithGen can re-Connect
// through the shared ConnectHelper (which rejects a second Connect while a
// Session is open). It is a no-op on a first connect (no active profile) and
// on a same-profile re-select. Runs on the worker — the same lane, and the
// same teardown ordering, as reconnectInvoker.Reconnect.
func (c *connectInvoker) teardownForSwitch(profile *models.Connection) {
	if c.g == nil || profile == nil {
		return
	}
	active := c.g.connectionState.activeConnID
	if active == "" || active == profile.Name {
		return
	}
	// Close the query session FIRST — it releases its pooled conn; closing the
	// pool (helper.Disconnect) before that deadlocks waiting on the still-held
	// conn.
	if c.g.queryState.activeSQLSession != nil {
		_ = c.g.queryState.activeSQLSession.Close()
		c.g.queryState.activeSQLSession = nil
	}
	c.helper.Disconnect()
}

func (c *connectInvoker) connectWithGen(ctx context.Context, profile *models.Connection, gen uint64, reporter drivers.ProgressReporter) error {
	// --- WORKER PHASE: all blocking I/O runs here (Connect itself runs on
	// the worker goroutine — connections_controller.go schedules it via
	// OnWorker). Nothing in this phase writes GUI state the MainLoop reads;
	// results are collected into locals and published in the single
	// OnUIThread closure below.
	//
	// Switching to a different profile mid-session (e.g.
	// <leader>C → pick another connection) must tear the live connection down
	// first — the shared ConnectHelper rejects a second Connect with "already
	// connected" while a Session is open. No-op on a first connect or a
	// same-profile re-select.
	c.teardownForSwitch(profile)
	// T3 AD8: thread the live reporter (modal/Retry path) or nil (direct /
	// reconnect) through to the driver. The driver emits StageTunnel /
	// StageAuthenticated which the reporter marshals onto the modal checklist.
	conn, _, err := c.helper.Connect(ctx, profile, reporter)
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
	// T3 AD3: Objects is activated LATE — here, immediately before the schema
	// load — NOT on the auth boundary. A wireQueryRuntimeIO/AcquireSession
	// failure in the gap above would otherwise be mis-rendered as
	// "✗ Loading objects". Gen-guarded + marshalled like every stage write
	// (no ConnectingState mutation off the MainLoop); a nil reporter (direct /
	// reconnect path) skips it entirely (AD8).
	c.markStageActiveStaged(gen, reporter, guicontext.StageObjects)

	// T3 AD7: give the schema load its OWN timeout budget, separate from the
	// pg driver's connectTimeout, so a hung ListSchemas fails into the
	// Objects-✗ path instead of an infinite "Loading objects…". Derived from
	// the attempt ctx so Esc cancel still propagates.
	schemaCtx, cancelSchema := context.WithTimeout(ctx, schemaLoadTimeout)
	schemaItems, schemaOK := c.loadSchemaItems(schemaCtx)
	cancelSchema()

	// T3 AD6b + AD7: emit the TERMINAL Objects row in its OWN gen-guarded
	// closure submitted BEFORE the LATER publish closure that runs
	// popModalOnSuccess. FIFO ordering guarantees the terminal row is applied
	// before the pop; a separate paint of the "✓ Loaded N schemas" / "✗" frame
	// is best-effort (gocui may batch both closures into one flush on a fast
	// connect — see markObjectsTerminalStaged). On success the row reads
	// "✓ Loaded N schemas" (N = visible/filtered count); on failure/timeout it
	// goes ✗ but the connect still SUCCEEDS with an empty rail (no errMsg set).
	// nil reporter → no-op (AD8).
	c.markObjectsTerminalStaged(gen, reporter, schemaItems, schemaOK)

	editorBuf, editorOK := c.loadQueryEditorBuffer(profile)

	// Direct-load saved schema/table state on worker.
	savedSchemaIdx, tableItems, savedTableIdx := c.loadSavedSchemaTableState(
		ctx, profile, schemaItems, schemaOK)

	// Stamp LastConnectionID and prepend the profile to
	// the LIFO RecentConnectionIDs ring (deduped, capped at 10). Persisted
	// AFTER wireQueryRuntimeIO succeeds so a wiring rollback does not leave
	// a debounced write pointing at a profile that failed to connect.
	// MutateAndSave is independently synchronized and touches no gocui view
	// state, so it stays on the worker.
	// Stale-gated: a cancel-after-successful-dial bumps
	// gen, so a superseded attempt must NOT stamp persisted state.
	if profile != nil && c.g != nil && c.g.deps.Store != nil && !c.isStaleConnect(gen) {
		name := profile.Name
		c.g.deps.Store.MutateAndSave(func(a *common.AppState) {
			a.LastConnectionID = name
			a.RecentConnectionIDs = common.PushRecentConnectionID(a.RecentConnectionIDs, name)
		})
	}

	// replay persisted session settings (search_path,
	// statement_timeout, timezone, application_name) on the fresh session.
	// Runs on the worker — the toast hint is published in the UI closure.
	// Stale-gated: a superseded attempt must NOT replay
	// SET commands on the doomed session.
	var restoreHint string
	if profile != nil && rt.sqlSess != nil && !c.isStaleConnect(gen) {
		restoreHint = c.restoreSessionSettings(ctx, rt.sqlSess, profile.Name)
	}

	// --- PUBLISH PHASE: a SINGLE OnUIThread closure performs every write
	// the MainLoop reads (activeConn, SQLSession, QueryRunner.Bind, the
	// SCHEMAS rail items, the editor buffer, the focus push) so they
	// serialise with render-frame reads. The stale-gen
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
		// Clear result tabs from the outgoing connection: they are bound
		// to the old session and would otherwise display stale results
		// after the switch to a different database. Runs before the rail/
		// editor pushes below, which displace the dangling result-tab
		// MAIN_CONTEXT off the focus stack. No-op on a first connect.
		if c.g != nil && c.g.resultTabsH != nil {
			c.g.resultTabsH.CloseAll()
		}
		c.publishQueryRuntime(rt)
		c.publishQueryEditorBuffer(editorBuf, editorOK)
		c.publishSchemaItems(schemaItems, schemaOK)
		c.setActiveConn(profile)
		// Eagerly warm the completion metadata snapshot for
		// the active schema (table+view names + per-connection function names)
		// off the UI thread, so the first FROM/function completion serves from
		// the store without a driver round-trip. LoadEager is idempotent and
		// routes through the ConnectHelper serialized worker.
		c.warmEagerSchema()
		// A modal-origin connect renders its connecting body
		// inside the CONNECTION_MANAGER modal. On success pop the modal (and
		// reset it back to list mode for a later re-open) BEFORE pushing the
		// schemas/tables rails so the user lands in restored navigation with
		// the modal gone.
		c.popModalOnSuccess()
		// Restore schema cursor + publish tables.
		if savedSchemaIdx >= 0 && c.g.registry != nil && c.g.registry.Schemas != nil {
			c.g.registry.Schemas.SetCursor(savedSchemaIdx)
		}
		if tableItems != nil && c.g.registry != nil && c.g.registry.Tables != nil {
			c.g.registry.Tables.SetItems(tableItems)
			if savedTableIdx >= 0 {
				c.g.registry.Tables.SetCursor(savedTableIdx)
			}
		}
		// show restore toast on the UI thread.
		if restoreHint != "" && c.g.toastHelp != nil {
			c.g.toastHelp.Show(restoreHint, 4*time.Second)
		}
		// Land focus in the query editor on connection open,
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
// goroutine.
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
// a Gui).
func (c *connectInvoker) isStaleConnect(gen uint64) bool {
	if c == nil || c.g == nil {
		return false
	}
	return c.g.connectionState.connectGen.Load() != gen
}

// routeConnectError marshals the failure onto the UI thread and paints it
// on the CONNECTING screen via publishConnectError. Used by the dial-error
// branch (the wiring-rollback branch already owns a UI closure and calls
// publishConnectError inline). Stale-gen guarded both before scheduling and
// inside the closure (the gen could be bumped between the two), so a
// superseded worker never paints the live screen.
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
// surface a raw err).
func (c *connectInvoker) publishConnectError(gen uint64, err error) {
	if err == nil || c.isStaleConnect(gen) {
		return
	}
	if c.g == nil || c.g.tree == nil {
		return
	}
	// The error must only paint the screen that is actually top of the
	// focus stack — a cancel/retry may have popped it, and writing a dead
	// screen would be a leak (always CONNECTION_MANAGER).
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
// resets it to list mode after a modal-origin connect succeeds.
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
// short phrase; every other error is surfaced verbatim. The
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
// path).
func (c *connectInvoker) setActiveConn(profile *models.Connection) {
	if c == nil || c.g == nil {
		return
	}
	if profile == nil {
		c.g.connectionState.activeConnID = ""
		c.g.connectionState.activeConnProfile = nil
		return
	}
	c.g.connectionState.activeConnID = profile.Name
	c.g.connectionState.activeConnProfile = profile
}

// populateSchemasRail loads the schema list via ConnectHelper.LoadSchemas
// and pushes the visible subset (built-in / profile-hidden patterns
// filtered out) onto the SchemasContext so the SCHEMAS rail draws rows
// on the next layout frame. Without this hook the rail stays empty
// after a successful connect even though the driver is ready.
//
// Best-effort: a LoadSchemas error is logged and swallowed — the user
// still has the open connection and can retry by re-pressing <cr>.
// Empty registry / context (test wiring) collapses to a silent no-op.
//
// This runs on a WORKER goroutine, so the publishSchemaItems
// call (a mutex-free SetItems write the MainLoop reads every frame) is
// marshaled onto the UI thread via runOnUIThread, mirroring the
// load-on-worker / publish-on-UI-thread split. The other
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
// worker goroutine; publishSchemaItems does the write on the UI thread.
// The bool reports whether a slice was produced (false
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
// table cursor index (-1 if not found). All failures are no-ops.
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
// those reads. A false ok (load skipped/failed) is a
// no-op, leaving the existing rail intact.
func (c *connectInvoker) publishSchemaItems(items []any, ok bool) {
	if c == nil || !ok || c.g == nil || c.g.registry == nil || c.g.registry.Schemas == nil {
		return
	}
	c.g.registry.Schemas.SetItems(items)
}

// warmEagerSchema eagerly loads the completion metadata snapshot (table+view
// names for the active schema + per-connection function names) on a worker
// goroutine, so the first FROM / function completion after connect serves from
// the store without a blocking driver call. The SchemaWarmer's LoadEager
// already routes through the ConnectHelper serialized worker and is idempotent;
// running it here off the UI thread keeps connect snappy. No-op when the warmer
// is unwired or no schema is selected.
func (c *connectInvoker) warmEagerSchema() {
	if c == nil || c.g == nil || c.g.schemaWarmer == nil {
		return
	}
	schema := schemasPickerAdapter{registry: c.g.registry.Schemas}.SelectedSchemaName()
	if schema == "" {
		return
	}
	warmer := c.g.schemaWarmer
	c.g.OnWorker(func(_ gocui.Task) error {
		warmer.LoadEager(schema)
		return nil
	})
}

// populateTablesRail loads the table list for schema via
// ConnectHelper.LoadTables and pushes the result onto TablesContext so
// the TABLES rail draws rows on the next layout frame. Wired to the
// SCHEMAS-rail <CR> handler via HelperBag.OnSchemaActivate.
//
// Best-effort: a LoadTables error is logged and swallowed; the existing
// TablesContext.items are left intact so a transient failure does not
// blank a previously-loaded list. An empty schema name is a silent
// no-op (matches the picker's empty-list contract).
//
// This runs on a WORKER goroutine, so the SetItems publish (a
// mutex-free items+cursor write the MainLoop reads every frame) is marshaled
// onto the UI thread via runOnUIThread to serialise with render-frame reads,
// mirroring the load-on-worker / publish-on-UI-thread split.
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
// This runs on a WORKER goroutine, so the SetItems publish (a
// mutex-free items+cursor write the MainLoop reads every frame) is marshaled
// onto the UI thread via runOnUIThread to serialise with render-frame reads,
// mirroring the load-on-worker / publish-on-UI-thread split.
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
// composite worker.
//
// Best-effort: a LoadIndexes error is logged and swallowed; the existing
// IndexesContext.items are left intact so a transient failure does not
// blank a previously-loaded list. Empty schema/table is a silent no-op.
//
// This runs on a WORKER goroutine, so the SetItems publish (a
// mutex-free items+cursor write the MainLoop reads every frame) is marshaled
// onto the UI thread via runOnUIThread to serialise with render-frame reads,
// mirroring the load-on-worker / publish-on-UI-thread split.
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

// loadQueryEditorBuffer is the I/O phase of the post-Connect
// hook. It resolves (or generates) the persistent buffer UUID for the
// active connection via AppStateStore.GetOrCreateBufferUUID and loads the
// on-disk buffer (or a fresh empty Buffer when missing). The UUID lookup
// MUST go through the store (not Common.AppState, which is an unwired
// empty literal that never reaches disk) so the same UUID
// is reused across runs and previously-persisted .sql files are picked up
// instead of orphaned. It performs NO context write (SetBuffer) so it is
// safe to run on the worker goroutine; the disk read does not block the
// MainLoop. publishQueryEditorBuffer does the write on the UI thread.
// Missing Common / Store / registry / profile are silent
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
// thread to serialise with those reads. The swapped
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
// wiring); the publish phase then no-ops.
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
// serialise with the publication. Runs on the worker goroutine.
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
// (nil sqlSess) is a no-op.
func (c *connectInvoker) publishQueryRuntime(rt queryRuntime) {
	if c == nil || rt.sqlSess == nil {
		return
	}
	if c.runner != nil {
		c.runner.Bind(rt.sqlSess, rt.caps)
	}
	if c.g != nil {
		c.g.queryState.activeSQLSession = rt.sqlSess
	}
}

// restoreSessionSettings reads persisted session settings from AppState and
// replays allowed SET commands on the freshly opened SQLSession. Returns a
// human-readable hint listing restored settings (empty when nothing restored).
// Failures are logged and skipped — a partial restore is better than aborting
// the connect. Runs on the worker goroutine (I/O phase).
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
// renders the rendered body each frame.
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
// The typed buffer is no longer combined here: the
// PROMPT view is now editable and the runtime source of truth for the
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
// narrow ReconnectInvoker surface the ReconnectController consumes.
// PingConnection issues a lightweight pool-level round-trip;
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
	// released, deadlocking the reconnect.
	if r.inv.g != nil && r.inv.g.queryState.activeSQLSession != nil {
		_ = r.inv.g.queryState.activeSQLSession.Close()
		r.inv.g.queryState.activeSQLSession = nil
	}
	// Tear down the schema-rail session + pool. This also satisfies the
	// "data: already connected (call Disconnect first)" guard in Connect.
	r.helper.Disconnect()
	// Drop ALL warmed completion metadata from the prior
	// connection AND the warmer's per-key cooldown/in-flight bookkeeping, so no
	// stale entry survives the reconnect and a table that was in cooldown at
	// disconnect is not suppressed on the new connection. The following Connect
	// re-runs LoadEager (warmEagerSchema) on the new session, repopulating the
	// eager tier; lazy entries re-warm on demand.
	if r.inv.g != nil && r.inv.g.schemaWarmer != nil {
		r.inv.g.schemaWarmer.Reset()
	}
	return r.inv.Connect(ctx, profile)
}

// metadataInvalidatorAdapter satisfies controllers.SchemaMetadataInvalidator by
// forwarding to the SchemaWarmer, resolved lazily because the warmer is
// constructed (wireEditorCompletion) after the QueryDeps bundle this adapter
// lives in is value-copied into the controllers. A nil warmer (pre-wire, or a
// build that omits completion) makes every method a no-op.
type metadataInvalidatorAdapter struct{ g *Gui }

func (a *metadataInvalidatorAdapter) InvalidateSchema(schema string) {
	if a == nil || a.g == nil || a.g.schemaWarmer == nil {
		return
	}
	a.g.schemaWarmer.InvalidateSchema(schema)
}

func (a *metadataInvalidatorAdapter) InvalidateTable(schema, table string) {
	if a == nil || a.g == nil || a.g.schemaWarmer == nil {
		return
	}
	a.g.schemaWarmer.InvalidateTable(schema, table)
}

// Compile-time assertions: all adapters satisfy their target interfaces.
var (
	_ controllers.SchemaMetadataInvalidator = (*metadataInvalidatorAdapter)(nil)
	_ controllers.SchemaPicker              = schemasPickerAdapter{}
	_ controllers.TablePicker               = tablesPickerAdapter{}
	_ controllers.ActiveConnection          = (*activeConnAdapter)(nil)
	_ controllers.ConnectInvoker            = (*connectInvoker)(nil)
	_ controllers.ConnectionFormInvoker     = (*connectionFormInvoker)(nil)
	_ controllers.MenuPushHelper            = (*menuPushHelper)(nil)
	_ controllers.ReconnectInvoker          = (*reconnectInvoker)(nil)
)
