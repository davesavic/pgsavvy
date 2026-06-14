// Package orchestrator wires the dbsavvy TUI: it owns the focus-stack
// tree, the GuiDriver instance, every UI/data helper and controller,
// and the gocui MainLoop entry point.
//
// Lives in a sub-package (rather than pkg/gui) to avoid an import cycle
// — pkg/gui/controllers/helpers/ui imports pkg/gui (for the focus
// stack), so the orchestrator that *uses* those helpers must sit above
// them in the import DAG.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/env"
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// Deps is the dependency bag NewGui consumes. Composed by the entry
// point (pkg/app) from XDG paths, the AppStateStore, and a closure that
// re-loads connections.yml on demand.
type Deps struct {
	// Common is the cross-cutting bag (Log, Tr, UserConfig, AppState, Fs).
	Common *common.Common

	// SetSecretPrompter, when non-nil, receives the TUI masked SSH secret
	// prompter constructed during wireWithDriver. The app entry-point passes
	// pg.SetSecretPrompter here so the driver can prompt for SSH key
	// passphrases / passwords at connect time. A hook (not a direct pg import)
	// keeps the driver out of the GUI import graph — pkg/app stays the single
	// driver-wiring authority, mirroring pg.SetGlobalLogger.
	SetSecretPrompter func(session.SecretPrompter)

	// Store owns AppState mutations + debounced persistence.
	Store *common.AppStateStore

	// ConnectionsPath is the absolute path to connections.yml. Helpers
	// that append new profiles write to this path; the ConnectionsProvider
	// closure reads it on each invocation.
	ConnectionsPath string

	// ConnectionsProvider returns the freshly-loaded connection profiles.
	// Called by the empty-state hook and (in later epics) by the
	// connection picker's refresh path. Nil collapses to an empty slice.
	ConnectionsProvider func() []models.Connection

	// DriverNamesFn returns registered driver names. Defaults to
	// drivers.Names when nil; tests override.
	DriverNamesFn func() []string

	// HistoryProvider opens (and owns the close of) the process-wide
	// *query.History the orchestrator wires into every SQLSession the
	// connectInvoker builds. Called at most once per Gui — the first
	// wireWithDriver pass triggers the open; Gui.Close calls History.Close
	// during shutdown.
	//
	// Nil collapses to the default which opens
	// filepath.Join(env.GetStateDir(), "history.sqlite"). Tests inject a
	// temp-dir variant so they don't litter the XDG state dir.
	HistoryProvider func() (*query.History, error)
}

// matcherToastTTL is how long a Matcher-surfaced toast (swallowed
// handler error or disabled-binding reason) stays before auto-clearing.
const matcherToastTTL = 4 * time.Second

// Option mutates a *Gui at construction time. Functional-option pattern
// used to keep NewGui's signature stable while letting tests inject key
// delay overrides without polluting Deps.
type Option func(*Gui)

// keyDelayOverrides holds explicit overrides for the three Matcher
// delays. A nil pointer (or a zero field) means "fall back to cfg / the
// hard-coded default".
type keyDelayOverrides struct {
	timeoutLen    time.Duration
	ttimeoutLen   time.Duration
	whichKeyDelay time.Duration
}

// WithKeyDelays overrides the Matcher's three timing knobs at NewGui
// construction time. Each duration argument is honored only when
// positive; non-positive arguments fall back to cfg.UserConfig values,
// then to the documented Matcher defaults.
func WithKeyDelays(timeoutLen, ttimeoutLen, whichKeyDelay time.Duration) Option {
	return func(g *Gui) {
		g.keybindingSystem.delayOverrides = &keyDelayOverrides{
			timeoutLen:    timeoutLen,
			ttimeoutLen:   ttimeoutLen,
			whichKeyDelay: whichKeyDelay,
		}
	}
}

// WithClock injects the wall-clock seam used by the busy-spinner
// animation (U8). Nil is ignored (production keeps realClock); tests pass
// a fake to drive Now() and ticks deterministically.
func WithClock(clk Clock) Option {
	return func(g *Gui) {
		if clk != nil {
			g.spinnerState.clock = clk
		}
	}
}

// Gui is the dbsavvy TUI orchestrator. NewGui builds the driver-free
// pieces (focus stack, data helpers). The driver-dependent pieces
// (context registry, ui helpers, controllers, bindings) are built
// lazily by wireWithDriver, called from either initGocui (real
// production wiring) or UseDriverForTest (test-only seam).
type Gui struct {
	deps   Deps
	driver types.GuiDriver

	// prevLiveViews is the live gocui view count at the end of the previous
	// RunLayout frame. When this frame's count is lower a view was torn down
	// (a closed modal / popup / overlay); tcell's incremental Show() does not
	// repaint the cells it vacated, so RunLayout forces a one-shot full
	// Screen.Sync() that frame to evict the orphaned border ghosts.
	prevLiveViews int

	// prevModalBody is the CONNECTION_MANAGER modal's rendered buffer at the
	// end of the previous frame while it was in ModeConnecting. The connect /
	// retry lifecycle churns the body in place (list row -> "Connecting…" ->
	// "already connected" + retry hints) and some transitions draw it one row
	// shifted for a frame; tcell's incremental Show() never re-emits the cells
	// the shifted frame vacated, so the bodies otherwise stack as ghosts that
	// "move up" on every retry. RunLayout forces a one-shot Screen.Sync() on
	// frames where this changes. Empty while the modal is not open in
	// ModeConnecting — the view-count-shrink (close) case is covered by
	// prevLiveViews instead.
	prevModalBody string

	// Focus stack; driver-free.
	tree *gui.ContextTree

	// Data helpers (driver-free).
	connectHelper *data.ConnectHelper
	schemasHelper *data.SchemasHelper
	formHelper    *data.ConnectionFormHelper
	refreshHelper *data.RefreshHelper

	// schemaWarmer owns the background-warmed completion metadata snapshot.
	// Eager-loaded on connect + schema-select; lazily warmed
	// per-table by the completion SchemaSource. Nil until wireEditorCompletion
	// runs. The completion sources read its store synchronously.
	schemaWarmer *data.SchemaWarmer

	// lastWarmErrorToast throttles the user-visible warm-failure toast so a
	// burst of failing completion warms does not spam the status line.
	lastWarmErrorToast time.Time

	// Built by wireWithDriver.
	registry       *guicontext.ContextTree
	controllers    *controllers.Controllers
	confirmHelp    *ui.ConfirmHelper
	promptHelp     *ui.PromptHelper
	searchLineHelp *ui.SearchLineHelper
	choiceHelp     *ui.ChoiceHelper
	toastHelp      *ui.ToastHelper
	tablesHelp     *ui.TablesHelper
	tipHelp        *ui.TipHelper
	resultTabsH    *ui.ResultTabsHelper
	noticeHelp     *ui.NoticeHelper

	// Inline-edit helpers. Built by wireWithDriver
	// alongside the existing UI helpers; pinned on Gui so future bd
	// issues can plumb them through dispatchers
	// without re-instantiating. pendingEditReg is the per-(connID,
	// baseTable) registry that CommitDialogOpen, CellEditor, the discard
	// flows, the quit guard, and the status indicator all route through to
	// land / count edits on the right table's set.
	pendingEditReg  *pendingEditRegistry
	pendingDiscardH *helpers.PendingDiscardHelper
	jumpListH       *ui.ResultJumpList
	fkForwardH      *helpers.FKForwardHelper

	// Keybinding system (built by wireWithDriver).
	keybindingSystem keybindingSystem

	// Connection-lifecycle state surfaced by the activeConnAdapter.
	connectionState connectionState

	// Query-execution state surfaced by the query runtime.
	queryState queryState

	// closed is true once Close has run; idempotent guard.
	closed bool

	// Threading-model + busy-spinner state (DESIGN.md §17, U8). See
	// threading.go for the OnUIThread / OnUIThreadContentOnly / OnWorker
	// methods that consume these fields.
	spinnerState spinnerState
}

// keybindingSystem groups the keybinding-subsystem collaborators built by
// wireWithDriver.
type keybindingSystem struct {
	cmdRegistry *commands.Registry
	matcher     *keys.Matcher
	modeStore   *keys.ModeStore
	whichkey    *keys.WhichKey
	exRegistry  *keys.ExRegistry
	// kbRuntime is the keys.Runtime composite handed to controllers via
	// HelperBag.KbRuntime. Retained on Gui so RunLayout's Tier-4 status
	// pass can hand it to RenderStatusLine without
	// rebuilding the value every frame.
	kbRuntime *keys.Runtime

	// lastWarnings captures the Warning slice returned by the most recent
	// KeybindingService.Build run during wireWithDriver. Surfaced via the
	// Warnings() accessor for the integration smoke test.
	lastWarnings []keys.Warning

	// masterEditors maps each editable context's key to the gocui.Editor
	// installKeyDispatch built for it. RunLayout's Tier-3 popup pass
	// reattaches the editor to the live view-instance each time the
	// context appears on the focus stack — a fresh Push creates a new
	// gocui view, so the editor must be reattached, and
	// SetMasterEditor is idempotent. Today the map holds two entries:
	// COMMAND_LINE (masterEditor) and QUERY_EDITOR (editor.VimEditor).
	masterEditors map[types.ContextKey]gocui.Editor

	// Test overrides for Matcher timing; nil means use cfg + defaults.
	delayOverrides *keyDelayOverrides
}

// queryState groups the query-execution fields surfaced by the query
// runtime. queryRunner is
// built empty in wireWithDriver and stashed in the HelperBag so
// controllers' value-copy of the bag stays valid across Bind / Unbind.
// history is a process-wide singleton opened lazily on first
// wireWithDriver and closed in Gui.Close. activeSQLSession is the
// SQLSession the most recent connectInvoker.Connect built; Close cancels
// any in-flight run via its Close().
type queryState struct {
	queryRunner      *data.QueryRunner
	history          *query.History
	activeSQLSession *session.SQLSession
}

// connectionState groups the connection-lifecycle fields surfaced by the
// activeConnAdapter.
type connectionState struct {
	// Connection state surfaced by the activeConnAdapter.
	activeConnID      string
	activeConnProfile *models.Connection

	// onTableActivate stashes the HelperBag.OnTableActivate closure
	// wireWithDriver installed, so tests can invoke the composite
	// TABLES <CR> path without reaching through AttachControllers.
	onTableActivate func(*models.Table) error

	// connectGen is the supersession token for in-flight Connects.
	// Each connectInvoker.Connect bumps it on entry and
	// captures the new value; on completion it only mutates activeConn /
	// pushes the schemas rail if its captured token is still the latest.
	// A connect that returns after a newer activation (token < current)
	// is stale and drops its result, so a slow/timed-out dial cannot
	// clobber a more recent connection. Atomic so concurrent worker-
	// goroutine Connects don't race the bump.
	connectGen atomic.Uint64
}

// spinnerState groups the busy-counter + busy-spinner ticker fields
// (DESIGN.md §17, U8).
type spinnerState struct {
	// busy is the in-flight-worker counter (atomic; ticked by OnWorker,
	// read by BusyCount for the bottom spinner). MUST stay FIRST:
	// 8-byte alignment for atomic.AddInt64 on 32-bit.
	busy int64

	// workersWG joins live OnWorker goroutines on shutdown so the
	// goleak smoke tests have a deterministic quiescence point.
	workersWG sync.WaitGroup

	// onWorkerSampleCounter implements the AD-20 quiescence-preserving
	// sampling for cat=state worker_start / worker_end emits. Every
	// OnWorker invocation increments it; the counter % 10 == 0 sample
	// gate plus mandatory quiescence-transition emits (busy_before==0 /
	// busy_after==0) together yield 2 + N/10 worker lines per burst.
	// Per AD-20 this MUST be a field on *Gui (not package-level) so
	// concurrent test Guis don't share state.
	onWorkerSampleCounter atomic.Uint64

	// Busy-spinner animation state (U8). The spinner glyph advances off a
	// wall-clock frame counter ticked by a periodic content-only
	// re-render while busy>0, so a single long-running worker still
	// animates.
	//
	//   - clock is the injectable wall-clock + ticker-factory seam (see
	//     clock.go). Defaults to realClock; WithClock overrides for tests.
	//   - spinnerMu is a DEDICATED mutex guarding arm/stop of the ticker.
	//     It is NOT the atomic busy counter: armed on the busy 0->1
	//     transition and stopped on ->0, two concurrent workers could
	//     otherwise double-arm or lose a stop. spinnerMu makes the
	//     exactly-one-ticker invariant hold.
	//   - spinnerTicker is the live Ticker while busy>0 (nil otherwise).
	//   - spinnerStop signals the per-ticker drain goroutine to exit.
	//   - spinnerStart is the wall-clock instant the ticker was armed; the
	//     frame counter is elapsed-since-start / spinnerTickInterval.
	clock         Clock
	spinnerMu     sync.Mutex
	spinnerTicker Ticker
	spinnerStop   chan struct{}
	spinnerStart  time.Time
}

// NewGui builds every collaborator that doesn't depend on the live
// GuiDriver. The driver-dependent wiring (context registry, UI helpers,
// controllers, key/mouse bindings) waits for either initGocui (prod)
// or UseDriverForTest (test).
func NewGui(deps Deps, opts ...Option) *Gui {
	g := &Gui{
		deps:         deps,
		tree:         gui.NewContextTree(),
		spinnerState: spinnerState{clock: realClock{}},
	}
	g.connectHelper = data.NewConnectHelper()
	g.schemasHelper = data.NewSchemasHelper(deps.Common, deps.Store)
	g.formHelper = data.NewConnectionFormHelper(deps.Common, fsFromCommon(deps.Common), deps.ConnectionsPath, deps.DriverNamesFn)
	g.refreshHelper = data.NewRefreshHelper()
	for _, opt := range opts {
		if opt != nil {
			opt(g)
		}
	}
	return g
}

// initGocui constructs the production *gocui.Gui, wraps it in
// *gocuiDriver, and runs wireWithDriver.
func (g *Gui) initGocui() error {
	mouseEnabled := false
	if cfg := g.deps.Common.Cfg(); cfg != nil {
		mouseEnabled = cfg.UI.Mouse.Enabled
	}
	_ = mouseEnabled // mouse mode is configured per-binding via SetViewClickBinding; gocui has no global toggle here.

	ng, err := gocui.NewGui(gocui.NewGuiOpts{
		// OutputTrue enables 24-bit truecolor SGR (48;2;R;G;B) so themed
		// hex backgrounds — e.g. the muted-amber dirty-cell tint — render;
		// tcell downsamples on terminals without truecolor.
		OutputMode:      gocui.OutputTrue,
		SupportOverlaps: false,
	})
	if err != nil {
		return fmt.Errorf("gui: gocui.NewGui: %w", err)
	}
	// Enable bottom-border footers; result tabs render run metadata
	// (row count / state) flush-right on the bottom border via view.Footer.
	// Safe globally: no other view sets a Footer.
	ng.ShowListFooter = true
	g.driver = newGocuiDriver(ng)
	return g.wireWithDriver()
}

// UseDriverForTest injects a test driver and runs wireWithDriver. The
// recorder fake in pkg/gui/internal/testfake calls this to bypass the
// real *gocui.Gui construction.
func (g *Gui) UseDriverForTest(d types.GuiDriver) error {
	if d == nil {
		return fmt.Errorf("gui: UseDriverForTest: nil driver")
	}
	g.driver = d
	return g.wireWithDriver()
}

// wireWithDriver builds the context registry, keybinding-system
// runtime (commands.Registry / Matcher / ModeStore / WhichKey /
// ExRegistry), UI helpers, controllers, registers every binding, and
// pushes the initial CONNECTIONS context.
//
// railEmptyText returns the ContextTreeDeps.RailEmptyText hook: the dim
// empty-state placeholder for the SCHEMAS/TABLES/COLUMNS/INDEXES side rails.
// Text is sourced from the TranslationSet so it stays
// localizable, mirroring the CONNECTIONS EmptyConnectionsHint. Returns "" for
// any other key so an unmapped rail falls through to the prior blank render.
func railEmptyText(tr *i18n.TranslationSet) func(types.ContextKey) string {
	return func(rail types.ContextKey) string {
		if tr == nil {
			return ""
		}
		switch rail {
		case types.SCHEMAS:
			return tr.EmptySchemasHint
		case types.TABLES:
			return tr.EmptyTablesHint
		case types.COLUMNS:
			return tr.EmptyColumnsHint
		case types.INDEXES:
			return tr.EmptyIndexesHint
		default:
			return ""
		}
	}
}

// SetManager is called FIRST (before any binding registration) because
// gocui.Gui.SetManager wipes g.keybindings, g.views, and g.currentView
// in its body — calling it after Register would silently delete every
// binding we just installed and leave the TUI unresponsive to input.
func (g *Gui) wireWithDriver() error {
	if g.driver == nil {
		return fmt.Errorf("gui: wireWithDriver: nil driver")
	}

	// Install the Manager (g.Layout) before anything that touches the
	// runtime's binding/view tables.
	g.driver.SetManager(g)

	cfg := g.deps.Common.Cfg()
	if cfg == nil {
		cfg = config.GetDefaultConfig()
	}
	tr := g.deps.Common.Tr
	provider := g.deps.ConnectionsProvider
	if provider == nil {
		provider = func() []models.Connection { return nil }
	}

	// Build the keybinding-system collaborators (matcher + runtime). MUST run
	// before wireContextRegistry — the registry's ctxDeps.Matcher reads
	// g.keybindingSystem.matcher, which this sets.
	if err := g.wireKeybindingSystem(cfg); err != nil {
		return err
	}

	// Build the context registry with hooks closed over the driver.
	g.wireContextRegistry(tr, provider)

	// UI helpers that need the driver / registry.
	g.wireUIHelpers(tr)

	// NoticeHelper routes server NOTICE / WARNING messages from streaming
	// queries to a first-of-run toast.
	g.noticeHelp = ui.NewNoticeHelper(ui.NoticeHelperDeps{
		Toaster:  g.toastHelp,
		OnWorker: g.OnWorker,
		Tr:       tr,
	})

	tablePicker := tablesPickerAdapter{registry: g.registry.Tables}

	// Open the per-process query history on the first wireWithDriver. The
	// open is best-effort — a sqlite open failure (e.g. read-only home)
	// degrades to "no history" rather than blocking the TUI from coming
	// up. The Warn line gives the operator a thread to pull on. Subsequent
	// wireWithDriver calls (test seam re-runs) reuse the open handle.
	if g.queryState.history == nil {
		hp := g.deps.HistoryProvider
		if hp == nil {
			hp = defaultHistoryProvider
		}
		h, hErr := hp()
		if hErr != nil {
			g.deps.Common.Logger().Warn("gui: history open", "err", hErr)
		} else {
			g.queryState.history = h
		}
	}

	// Build the empty QueryRunner shell that survives Connect / Disconnect
	// cycles. connectInvoker.Bind swaps the inner session atomically.
	if g.queryState.queryRunner == nil {
		g.queryState.queryRunner = data.NewQueryRunner(nil, drivers.Capabilities{})
	}

	// Instantiate the inline-edit helpers. Each is built
	// once at boot and pinned on *Gui so subsequent re-wires
	// can extend the dispatch surface without rebuilding the state.
	//
	// The discard helper resolves staged edits through the per-(connID,
	// baseTable) registry via AllSets, so DiscardAll / BlockQuitIfPending
	// see every table's edits rather than a single shared set.
	if g.pendingEditReg == nil {
		g.pendingEditReg = newPendingEditRegistry()
	}
	g.pendingDiscardH = helpers.NewPendingDiscardHelper(helpers.PendingDiscardDeps{
		AllSets: func() []*models.PendingEditSet {
			if g.pendingEditReg == nil {
				return nil
			}
			return g.pendingEditReg.All()
		},
		Confirm: g.confirmHelp,
		Toast:   g.toastHelp,
	})
	g.jumpListH = ui.NewResultJumpList()

	// Build the ResultTabsHelper and the wiring that
	// depends on it (QueryRunner preempter, tab lifecycle callbacks,
	// FK-forward helper). Runs after g.queryRunner and g.jumpListH exist.
	g.wireResultTabs(tr)

	connectInv := &connectInvoker{g: g, helper: g.connectHelper, runner: g.queryState.queryRunner, history: g.queryState.history}

	// Wire the RefreshHelper closures over the live
	// populateXxxRail helpers + refreshConnectionsRail.
	g.wireRefreshHelperDeps(connectInv)

	// CoreDeps — fail-fast: NewCoreDeps panics if driver or logger is
	// nil. wireWithDriver has already rejected a nil driver above
	// (gui.go: "nil driver"), and Common.Logger() never returns nil, so
	// this constructor cannot panic in production wiring; it converts the
	// "forgot to wire a core dep" programmer error into an immediate
	// crash rather than a silently dead keybinding.
	core := controllers.NewCoreDeps(g.driver, g.deps.Common.Logger())

	// NavDeps + UIDeps / QueryDeps / ThreadingDeps bundles.
	nav := g.wireNavDeps(connectInv, tablePicker)
	uiDeps, query, threading := g.wireHelperDeps()

	edit := g.wireEditDeps()

	helperBag := controllers.HelperBag{
		CoreDeps:      core,
		NavDeps:       nav,
		UIDeps:        uiDeps,
		QueryDeps:     query,
		ThreadingDeps: threading,
		EditDeps:      edit,
	}
	g.controllers = controllers.AttachControllers(g.registry, g.deps.Common, helperBag)

	g.wirePopupStates(helperBag, connectInv)

	// Build the four inline-edit popup controllers and
	// attach each to its context so their bindings reach the trie via
	// AllDefaultBindings.
	g.wireInlineEditControllers(helperBag)

	g.wireEditorCompletion()

	// Register every controller's action handlers + the orchestrator-owned
	// commands (cheatsheet, connection-manager, tip dismiss, rail switch,
	// command-line).
	g.wireActionRegistrations(connectInv)

	// Validate the user config + build the keybinding trie.
	trieSet, defaults, svc, err := g.wireTrie(cfg)
	if err != nil {
		return err
	}

	// Register the vim-style ex-commands (:q / :w / :set / :reset / :c /
	// :reload) + the search-path / statement-timeout prompt runners.
	g.wireExCommands(defaults, svc)

	// Master Editor / per-key dispatch, mouse wiring, focus-swap hooks,
	// CONNECTION_MANAGER startup root, first-run tip.
	if err := g.wireKeyDispatch(trieSet, cfg, tablePicker); err != nil {
		return err
	}
	return nil
}

// registerTableInspectOpen registers the TableInspectOpen action handler.
// `i` on the TABLES rail invokes this; it:
//
//   - Snapshots the selected table's (schema, name) — guards TOCTOU.
//   - Either pushes the TABLE_INSPECT popup onto the focus stack, OR
//     (when the popup is already on top) re-targets it without a second
//     Push (AD-24 re-open semantics).
//   - Marks the context loading and fans out TWO OnWorker dispatches —
//     one for columns, one for indexes. Whichever finishes second flips
//     loading=false on the UI thread.
//
// schemaSearchPathSQL builds the SET statement and the human-readable
// snapshot value for making schema the active search_path, keeping public as
// a fallback so unqualified references to objects in public (extension
// functions, helpers) still resolve. The public fallback is omitted when the
// selected schema is already public, avoiding a redundant "public, public".
// The schema identifier is double-quote escaped to prevent SQL injection
// (mirrors replaySessionSettings quoting).
func schemaSearchPathSQL(schema string) (sql, displayValue string) {
	quoted := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if schema == "public" {
		return "SET search_path TO " + quoted, schema
	}
	return "SET search_path TO " + quoted + ", public", schema + ", public"
}

// persistTrackedSetting records a successfully-applied session setting in the
// snapshot and AppState so it survives reconnects and shows in the status bar,
// and refreshes the schema rail when search_path changed. Must be called on a
// worker goroutine. Shared by the :set handler and schema-rail selection.
func (g *Gui) persistTrackedSetting(ctx context.Context, settingKey, settingValue string) {
	if g.queryState.activeSQLSession != nil {
		g.queryState.activeSQLSession.SettingsSnapshot().Set(settingKey, settingValue)
	}

	if connID := g.connectionState.activeConnID; connID != "" && g.deps.Store != nil {
		g.deps.Store.MutateAndSave(func(a *common.AppState) {
			if a.LastSessionSettings == nil {
				a.LastSessionSettings = make(map[string]map[string]string)
			}
			if a.LastSessionSettings[connID] == nil {
				a.LastSessionSettings[connID] = make(map[string]string)
			}
			a.LastSessionSettings[connID][settingKey] = settingValue
		})
	}

	if settingKey == "search_path" && g.refreshHelper != nil {
		_ = g.refreshHelper.RefreshSchemas(ctx)
	}
}

func (g *Gui) registerTableInspectOpen(connectInv *connectInvoker) {
	inspectCtx := g.registry.TableInspect
	columnsCtx := g.registry.Columns
	indexesCtx := g.registry.Indexes

	_ = g.keybindingSystem.cmdRegistry.Register(&commands.Command{
		ID:          commands.TableInspectOpen,
		Description: "Open table inspect",
		Handler: func(_ commands.ExecCtx) error {
			if g.registry == nil || g.registry.Tables == nil {
				return nil
			}
			sel := g.registry.Tables.SelectedItem()
			tbl, ok := sel.(*models.Table)
			if !ok || tbl == nil {
				return nil
			}
			sch, tname := tbl.Schema, tbl.Name

			// Re-open semantics (AD-24): if popup already on top, re-target.
			cur := g.tree.Current()
			if cur != nil && cur.GetKey() == types.TABLE_INSPECT {
				inspectCtx.SetTarget(sch, tname)
				if s := inspectCtx.State(); s != nil {
					s.SetActive(0)
				}
			} else {
				state := popup.NewTabbedPopup([]popup.Tab{
					{Title: "Columns", Panel: controllers.NewColumnsPanel(columnsCtx)},
					{Title: "Indexes", Panel: controllers.NewIndexesPanel(indexesCtx)},
				})
				inspectCtx.SetTarget(sch, tname)
				inspectCtx.SetState(state)
				if err := g.tree.Push(inspectCtx); err != nil {
					return err
				}
			}

			inspectCtx.SetLoading(true)
			var ack atomic.Int32
			ack.Store(2)
			done := func() {
				if ack.Add(-1) == 0 {
					g.OnUIThreadContentOnly(func() error {
						inspectCtx.SetLoading(false)
						return nil
					})
				}
			}
			g.OnWorker(func(_ gocui.Task) error {
				defer done()
				connectInv.populateColumnsRail(context.Background(), sch, tname)
				return nil
			})
			g.OnWorker(func(_ gocui.Task) error {
				defer done()
				connectInv.populateIndexesRail(context.Background(), sch, tname)
				return nil
			})
			return nil
		},
	})
}

// historyRecentLimit caps the window of recent rows the HISTORY popup
// loads. 500 is generous for a browse-and-pick surface (v1 has no
// filter) while bounding the single-statement read + render cost.
const historyRecentLimit = 500

// registerHistoryOpen registers the HistoryOpen action handler. When the
// store is unopened (g.history nil) the handler is a no-op (no popup,
// no toast — the editor stays put). Otherwise it pushes the HISTORY
// popup on the UI thread, loads Recent(N) on a worker goroutine, and
// publishes the rows back on the UI thread via OnUIThreadContentOnly.
// SetRows / Push are NEVER called from the worker (data race under
// -race). Mirrors registerTableInspectOpen's threading shape.
func (g *Gui) registerHistoryOpen() {
	historyCtx := g.registry.History

	_ = g.keybindingSystem.cmdRegistry.Register(&commands.Command{
		ID:          commands.HistoryOpen,
		Description: "Open query history",
		Handler: func(_ commands.ExecCtx) error {
			if g.queryState.history == nil {
				return nil
			}

			// Re-open semantics: if the popup is already on top, reload in
			// place rather than stacking a second copy.
			cur := g.tree.Current()
			if cur == nil || cur.GetKey() != types.HISTORY {
				historyCtx.SetRows(nil)
				if err := g.tree.Push(historyCtx); err != nil {
					return err
				}
			}

			store := g.queryState.history
			g.OnWorker(func(_ gocui.Task) error {
				rows, err := store.Recent(context.Background(), historyRecentLimit)
				if err != nil {
					g.deps.Common.Logger().Warn("gui: history recent", "err", err)
					return nil
				}
				g.OnUIThreadContentOnly(func() error {
					historyCtx.SetRows(rows)
					return nil
				})
				return nil
			})
			return nil
		},
	})
}

// railForScope returns the focused side-rail SideListContext for a
// rail-search scope, or nil for any other scope.
func (g *Gui) railForScope(scope types.ContextKey) *guicontext.SideListContext {
	if g.registry == nil {
		return nil
	}
	switch scope {
	case types.TABLES:
		if g.registry.Tables != nil {
			return &g.registry.Tables.SideListContext
		}
	case types.SCHEMAS:
		if g.registry.Schemas != nil {
			return &g.registry.Schemas.SideListContext
		}
	}
	return nil
}

// setRailMatchCount pushes the cur/total slot into the SearchLine strip
// (visible only while the input is open). Empty when the search is inactive.
func (g *Gui) setRailMatchCount(rail *guicontext.SideListContext) {
	if g.searchLineHelp == nil || rail == nil {
		return
	}
	_, cur, total, active := rail.SearchStatus()
	if !active {
		g.searchLineHelp.SetMatchCount("")
		return
	}
	g.searchLineHelp.SetMatchCount(fmt.Sprintf("%d/%d", cur, total))
}

// openRailSearch opens the SearchLine input bound to rail. Mirrors the
// grid SearchPrompt: incremental OnChange drives SetSearch; <cr> is
// land-only (OnAccept no-op, search stays active); <esc> cancels
// (CursorRestore puts the cursor back, then ClearSearch).
func (g *Gui) openRailSearch(rail *guicontext.SideListContext) error {
	if g.searchLineHelp == nil || rail == nil {
		return nil
	}
	return g.searchLineHelp.Open(ui.SearchLineOpts{
		OnChange: func(query string) {
			rail.SetSearch(query)
			g.setRailMatchCount(rail)
		},
		OnAccept:       func(string) {},
		OnCancel:       func() { rail.ClearSearch() },
		CursorSnapshot: func() any { return rail.Cursor() },
		CursorRestore: func(snap any) {
			if i, ok := snap.(int); ok {
				rail.SetCursor(i)
			}
		},
	})
}

// registerRailSearch wires the four rail-search action handlers. Each
// resolves the focused rail from ctx.Scope so one handler serves both
// SCHEMAS and TABLES. <esc>-clear is a no-op when no search is active
// (mirrors the RESULT_GRID <esc> precedent; there is no global <esc>
// focus-pop on the rails to fall through to).
func (g *Gui) registerRailSearch() {
	reg := g.keybindingSystem.cmdRegistry
	_ = reg.Register(&commands.Command{
		ID: commands.RailSearchPrompt, Description: "Search rail",
		Handler: func(ctx commands.ExecCtx) error { return g.openRailSearch(g.railForScope(ctx.Scope)) },
	})
	_ = reg.Register(&commands.Command{
		ID: commands.RailSearchNext, Description: "Next rail match",
		Handler: func(ctx commands.ExecCtx) error {
			rail := g.railForScope(ctx.Scope)
			if rail == nil {
				return nil
			}
			rail.NextMatch()
			g.setRailMatchCount(rail)
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID: commands.RailSearchPrev, Description: "Prev rail match",
		Handler: func(ctx commands.ExecCtx) error {
			rail := g.railForScope(ctx.Scope)
			if rail == nil {
				return nil
			}
			rail.PrevMatch()
			g.setRailMatchCount(rail)
			return nil
		},
	})
	_ = reg.Register(&commands.Command{
		ID: commands.RailSearchClear, Description: "Clear rail search",
		Handler: func(ctx commands.ExecCtx) error {
			rail := g.railForScope(ctx.Scope)
			if rail == nil || !rail.SearchActive() {
				return nil // no-op when inactive
			}
			rail.ClearSearch()
			g.setRailMatchCount(rail)
			return nil
		},
	})
}

// defaultTablePreviewLimit bounds the row count of the SELECT issued
// when <CR> activates a table on the TABLES rail, keeping
// the preview cheap on large tables.
const defaultTablePreviewLimit = 100

// buildOnTableActivate wires the TABLES <CR> handler: run a bounded
// "SELECT * FROM <qualified table>" through the QueryEditorController's
// run path and push the active result tab onto the focus stack so the
// results panel takes focus. The SQL is fully qualified
// via pg.QuoteQualified, so schema resolution against the SCHEMAS rail
// is irrelevant here. Nil-safe: no controller / no session → no-op.
func (g *Gui) buildOnTableActivate(_ *connectInvoker) func(*models.Table) error {
	fn := func(table *models.Table) error {
		if table == nil || g.controllers == nil || g.controllers.QueryEditor == nil {
			return nil
		}
		if g.deps.Store != nil && g.connectionState.activeConnID != "" {
			g.deps.Store.SetLastTableName(g.connectionState.activeConnID, table.Name)
		}
		sql := fmt.Sprintf("SELECT * FROM %s LIMIT %d",
			pg.QuoteQualified(table.Schema, table.Name), defaultTablePreviewLimit)
		if !g.controllers.QueryEditor.RunSQL(sql) {
			return nil
		}
		if g.tree == nil || g.resultTabsH == nil {
			return nil
		}
		if target := g.resultTabsH.ActiveContext(); target != nil {
			return g.tree.Push(target)
		}
		return nil
	}
	g.connectionState.onTableActivate = fn
	return fn
}

// refreshConnectionManagerRail reloads the connection profiles from
// Deps.ConnectionsProvider into the CONNECTION_MANAGER modal's row slice
// mirroring refreshConnectionsRail. The SetItems write is a
// plain in-memory mutation; the only caller (HandleFocus on push) runs on the
// MainLoop, so it serialises with render reads without an OnUIThread bounce.
func (g *Gui) refreshConnectionManagerRail() {
	if g.registry == nil || g.registry.ConnectionManager == nil {
		return
	}
	provider := g.deps.ConnectionsProvider
	if provider == nil {
		g.registry.ConnectionManager.SetItems(nil)
		return
	}
	profiles := provider()
	items := make([]any, len(profiles))
	for i := range profiles {
		p := profiles[i]
		items[i] = &p
	}
	g.registry.ConnectionManager.SetItems(items)
}

// saveConnectionForm persists the validated connection form. It
// is the OnSaveConnection seam: append for an add, UpdateConnection(oldName,
// conn) for an edit. conn carries the form-untouched fields (Password,
// SSHTunnel, …) verbatim, so writing it whole preserves them. On success it
// reloads the modal list from disk and returns nil (the controller flips to
// ModeList). On failure it stamps the inline form error and returns the err so
// the controller stays in ModeForm.
//
// This runs on the MainLoop (the Confirm keybinding dispatch), so the
// disk-read in refreshConnectionManagerRail serialises with render reads — no
// worker-write violation.
func (g *Gui) saveConnectionForm(conn models.Connection, isEdit bool, originalName string) error {
	fs := fsFromCommon(g.deps.Common)
	write := func() error { return config.AppendConnection(fs, g.deps.ConnectionsPath, conn) }
	if isEdit {
		write = func() error {
			return config.UpdateConnection(fs, g.deps.ConnectionsPath, originalName, conn)
		}
	}
	if err := write(); err != nil {
		g.registry.ConnectionManager.FormSetError(g.deps.Common.Tr.SaveConnectionFailed)
		return err
	}
	g.refreshConnectionManagerRail()
	return nil
}

// deleteConnectionFromModal is the OnDeleteConnection callback wired from
// the CONNECTION_MANAGER modal. If the deleted connection is the
// currently active session, it tears down the live session first (preempt
// in-flight result tabs, close the SQL session, unbind the query runner, clear
// active-conn state). Then it removes the profile from connections.yml via
// config.DeleteConnection and refreshes the modal list. Runs on the MainLoop.
func (g *Gui) deleteConnectionFromModal(connName string) error {
	if connName == g.connectionState.activeConnID {
		if g.resultTabsH != nil {
			g.resultTabsH.PreemptInFlight()
		}
		if g.queryState.activeSQLSession != nil {
			_ = g.queryState.activeSQLSession.Close()
			g.queryState.activeSQLSession = nil
		}
		if g.queryState.queryRunner != nil {
			g.queryState.queryRunner.Unbind()
		}
		if g.connectHelper != nil {
			g.connectHelper.Disconnect()
		}
		// Drop warmed completion metadata for the now-closed
		// connection so a later Connect to a different profile cannot read a
		// prior connection's entries before its own warm lands.
		if g.schemaWarmer != nil {
			g.schemaWarmer.Reset()
		}
		// Drop the relationship panel's per-row preview + estimate caches so a
		// later Connect cannot read a prior connection's values.
		if g.controllers != nil && g.controllers.RelationshipPanel != nil {
			g.controllers.RelationshipPanel.ClearCaches()
		}
		g.connectionState.activeConnID = ""
		g.connectionState.activeConnProfile = nil
	}

	fs := fsFromCommon(g.deps.Common)
	if err := config.DeleteConnection(fs, g.deps.ConnectionsPath, connName); err != nil {
		return err
	}
	g.refreshConnectionManagerRail()
	return nil
}

// connectionNames returns the snapshot of all profile names for the
// add/edit form's uniqueness check. Empty when no provider is
// wired.
func (g *Gui) connectionNames() []string {
	if g.deps.ConnectionsProvider == nil {
		return nil
	}
	profiles := g.deps.ConnectionsProvider()
	out := make([]string, 0, len(profiles))
	for i := range profiles {
		out = append(out, profiles[i].Name)
	}
	return out
}

// restoreConnectionManagerCursor positions the modal cursor on the profile
// whose Name matches the persisted LastConnectionID, mirroring
// restoreConnectionsCursor. No-op when the registry/Store is unwired or no
// match lives in the list.
func (g *Gui) restoreConnectionManagerCursor() {
	if g == nil || g.registry == nil || g.registry.ConnectionManager == nil {
		return
	}
	if g.deps.Store == nil {
		return
	}
	last := g.deps.Store.LastConnectionIDSnapshot()
	if last == "" {
		return
	}
	for i, it := range g.registry.ConnectionManager.Items() {
		conn, ok := it.(*models.Connection)
		if !ok || conn == nil {
			continue
		}
		if conn.Name == last {
			g.registry.ConnectionManager.SetCursor(i)
			return
		}
	}
}

// installKeyDispatch wires the dispatch path:
//
//   - For editable views (Context.GetKey().IsEditable() returns true,
//     i.e. COMMAND_LINE), install a master gocui.Editor that routes
//     every keystroke through the Matcher.
//   - For non-editable views, install one SetKeybinding per top-level
//     trie-root Key (per Mode bit and per Scope including GLOBAL) so
//     gocui dispatches the single key into the Matcher.
//   - GLOBAL bindings are also installed with empty viewname so they
//     fire from any focused view.
//
// Note: NewMasterEditor needs the underlying *gocui.Gui to schedule
// pending-buffer flushes onto the MainLoop. For the recorder-driver
// path (testfake) we pass nil; the in-flight flush path is only
// relevant for ModeInsert (out of scope for this epic).
func (g *Gui) installKeyDispatch(trieSet *keys.TrieSet) error {
	var ngocui *gocui.Gui
	if real, ok := g.driver.(*gocuiDriver); ok {
		ngocui = real.Gocui()
	}

	g.keybindingSystem.masterEditors = map[types.ContextKey]gocui.Editor{}

	for _, ctx := range g.registry.Flatten() {
		if ctx == nil || ctx.GetKind() == types.STUB {
			continue
		}
		key := ctx.GetKey()
		view := ctx.GetViewName()

		if key.IsEditable() {
			if view == "" {
				continue
			}
			// Stash the editable view's master Editor. RunLayout's Tier-3
			// popup pass attaches it to the live view-instance every frame
			// the context is on the focus stack — re-Push creates a fresh
			// view, and gocui's SetMasterEditor is idempotent.
			switch key {
			case types.QUERY_EDITOR:
				if g.registry.QueryEditor != nil {
					ve := editor.NewVimEditor(g.registry.QueryEditor, g.keybindingSystem.matcher, key, editor.WithSessionLog(g.deps.Common.Logger()), editor.WithGuiDriver(g.driver), editor.WithEmergencyQuit(g.emergencyQuit))
					// Wire the Tab/Enter popup-navigation seam
					// to the controller so the insert path can drive the
					// completion popup.
					if g.controllers != nil && g.controllers.VimEditor != nil {
						ve.SetCompletionKey(g.controllers.VimEditor.CompletionKey)
						// As-you-type auto-trigger, gated at boot
						// by editor.autocomplete (default true). When false we do
						// NOT install the callback, so typing never opens the
						// popup — manual <c-x><c-o> stays available regardless
						// (it routes through RefilterOrTrigger, not this seam).
						if cfg := g.deps.Common.Cfg(); cfg != nil && cfg.Editor.Autocomplete {
							ve.SetAutoCompleter(g.controllers.VimEditor.AutoTrigger)
						}
						// Table-accept alias insertion
						// is DEFAULT-ON; opt out via `editor.autocomplete_alias:
						// false`. The controller defaults the flag on, so we only
						// need to flip it off here.
						if cfg := g.deps.Common.Cfg(); cfg != nil && !cfg.Editor.AutocompleteAlias {
							g.controllers.VimEditor.SetAliasOnAccept(false)
						}
					}
					g.keybindingSystem.masterEditors[key] = ve
				}
			case types.SEARCH_LINE:
				// The SEARCH_LINE editor fires the
				// per-keystroke onChange seam so SearchPrompt's OnChange
				// drives live grid.SetSearch as the user types.
				opts := []MasterEditorOption{WithSessionLog(g.deps.Common.Logger()), WithEmergencyQuit(g.emergencyQuit)}
				if g.searchLineHelp != nil {
					opts = append(opts, WithOnPassthroughEdit(g.searchLineHelp.OnChange))
				}
				g.keybindingSystem.masterEditors[key] = NewMasterEditor(ngocui, g.keybindingSystem.matcher, key, opts...)
			default:
				g.keybindingSystem.masterEditors[key] = NewMasterEditor(ngocui, g.keybindingSystem.matcher, key, WithSessionLog(g.deps.Common.Logger()), WithEmergencyQuit(g.emergencyQuit))
			}
			continue
		}

		// Non-editable: install per-key SetKeybinding shims for every
		// trie root child at this scope (across all modes).
		if view == "" {
			continue
		}
		if err := g.installShimsForScope(trieSet, key, view); err != nil {
			return err
		}
		// Escape is never a trie-reachable key, so the loop
		// above installs no shim for it on a non-editable view. Without a
		// shim gocui drops Escape (a non-editable view has no Editor to
		// receive it), so the Matcher never sees it and a pending leader
		// chord — the which-key overlay — can never be aborted from a list
		// rail. Install an explicit Esc shim that routes into the Matcher;
		// its existing chord-abort path drops the pending prefix and hides
		// the overlay. Editable views are unaffected: their Editor already
		// delivers Escape to the Matcher.
		if err := g.installEscAbortShim(key, view); err != nil {
			return err
		}
		// R5: un-rebindable emergency Ctrl-C exit. A non-editable view
		// only receives keys it has a shim for; if the user removed/moved
		// the app.quit <c-c> binding, gocui would otherwise drop Ctrl-C
		// here. This unconditional shim guarantees Ctrl-C always reaches
		// the clean-quit path regardless of config.
		if err := g.installEmergencyQuitShim(view); err != nil {
			return err
		}
	}

	// GLOBAL trie's root keys: install with empty viewname so they
	// fire regardless of which view holds focus. gocui treats viewname
	// == "" as a global binding.
	if err := g.installShimsForScope(trieSet, types.GLOBAL, ""); err != nil {
		return err
	}
	// R5: global emergency Ctrl-C shim (view==""). Fires from any focused
	// view that has no view-specific Ctrl-C shim — config-independent.
	if err := g.installEmergencyQuitShim(""); err != nil {
		return err
	}

	// RESULT_GRID master editor. The context is a
	// StubContext (no static view), so the Flatten loop above skipped
	// it; build the editor here so RunLayout's Tier-1.5 pass can attach
	// it to whichever dynamic result_tab_<slot> view is currently
	// active. SetMasterEditor is idempotent — reattach per frame is
	// cheap, and re-pushes between tabs do not strand a stale editor on
	// the prior view (gocui's per-view Editor pointer is replaced on
	// attach, and result_tab views never become editable text targets).
	g.keybindingSystem.masterEditors[types.RESULT_GRID] = NewMasterEditor(ngocui, g.keybindingSystem.matcher, types.RESULT_GRID, WithSessionLog(g.deps.Common.Logger()), WithEmergencyQuit(g.emergencyQuit))
	// PLAN master editor. Plan tabs share the dynamic
	// result_tab_<slot> view with grid tabs, but their PLAN-scoped bindings
	// (o/H/<CR>/j/k/i) must dispatch under PLAN, not RESULT_GRID. Like
	// RESULT_GRID this context has no static view, so the Flatten loop skips
	// it; build the editor here and let RunLayout's Tier-1.5 pass pick it for
	// plan tabs by the active tab's context key.
	g.keybindingSystem.masterEditors[types.PLAN] = NewMasterEditor(ngocui, g.keybindingSystem.matcher, types.PLAN, WithSessionLog(g.deps.Common.Logger()), WithEmergencyQuit(g.emergencyQuit))
	return nil
}

// installShimsForScope walks every (mode, scope) trie and registers one
// SetKeybinding per Key reachable in the trie — root keys AND every
// chord-trailing key. The handler routes the key through matcher.Dispatch
// under the supplied scope; the Matcher tracks pending state internally,
// so a per-key shim suffices to resolve multi-step chords.
//
// Duplicate (view, key, mod) registrations are tolerated — gocui returns
// nil and the second handler shadows the first; our Matcher dispatches
// by scope so the shadowing handler still hits the right binding.
//
// Bug history: previously this loop only walked
// trie.RootKeys(). For a chord `<leader>q` (Space + q), only Space got a
// shim. After Space was consumed (Matcher returned Pending), gocui had
// no shim for q and silently dropped it — the leaf never fired and
// gocui.ErrQuit never propagated. Fix: enumerate ReachableKeys() so
// every chord-reachable key has a shim.
func (g *Gui) installShimsForScope(trieSet *keys.TrieSet, scope types.ContextKey, view string) error {
	if trieSet == nil {
		return nil
	}
	seen := map[shimKey]struct{}{}
	var firstErr error
	trieSet.Walk(func(tk keys.TrieSetKey, trie *keys.ChordTrie) {
		if firstErr != nil {
			return
		}
		if tk.Scope != scope {
			return
		}
		for _, k := range trie.ReachableKeys() {
			gk, gmod, err := keys.ChordKeyToGocui(k)
			if err != nil {
				continue
			}
			// R5: RESERVE Ctrl-C. Never install a trie shim for Ctrl-C, even
			// if a user bound <c-c> to some action in this non-editable
			// scope. installEmergencyQuitShim is then the SOLE Ctrl-C handler
			// per view, so the un-rebindable emergency quit wins regardless of
			// gocui's registration order (first-registered-wins, gocui
			// gui.go:1546). A competing user <c-c> handler is simply never
			// registered at this seam.
			if gk == ctrlCGocuiKey && gmod == ctrlCGocuiMod {
				continue
			}
			sk := shimKey{view: view, gk: gk, gmod: gmod}
			if _, dup := seen[sk]; dup {
				continue
			}
			seen[sk] = struct{}{}
			dispatchKey := k
			handler := func() error {
				_, err := g.keybindingSystem.matcher.Dispatch(scope, dispatchKey)
				return err
			}
			if err := g.driver.SetKeybinding(view, gk, gmod, handler); err != nil {
				firstErr = fmt.Errorf("gui: SetKeybinding(view=%q, key=%v): %w", view, k, err)
				return
			}
		}
	})
	return firstErr
}

// installEscAbortShim registers a single Escape SetKeybinding on a
// non-editable view whose handler routes Esc through matcher.Dispatch
// under that view's scope. Escape is not a trie-reachable key, so
// installShimsForScope never registers it; without this shim gocui drops
// Escape on a non-editable view and a pending leader chord (which-key
// overlay) can never be aborted from a list rail.
//
// When a chord is pending, Dispatch drops it and hides the which-key
// overlay; when idle, Esc is an unmatched fall-through (a harmless no-op),
// matching the prior behaviour where gocui silently dropped it. If the
// scope's trie already binds Esc (e.g. the menu popup's close action) the
// duplicate (view, Esc) registration is tolerated and resolves to the same
// Dispatch(scope, Esc) call.
func (g *Gui) installEscAbortShim(scope types.ContextKey, view string) error {
	escKey := keys.Key{Special: keys.KeyEsc}
	gk, gmod, err := keys.ChordKeyToGocui(escKey)
	if err != nil {
		return nil
	}
	handler := func() error {
		_, derr := g.keybindingSystem.matcher.Dispatch(scope, escKey)
		return derr
	}
	return g.driver.SetKeybinding(view, gk, gmod, handler)
}

// ctrlCKey is the decoded chord-trie Key for Ctrl-C. Under tcell raw mode
// ISIG is cleared, so keyboard Ctrl-C arrives as a KeyCtrlC KEY event
// (not SIGINT) and is decoded by KeyFromGocui into this exact value.
var ctrlCKey = keys.Key{Code: 'c', Mod: keys.ModCtrl}

// ctrlCGocuiKey / ctrlCGocuiMod are the gocui (Key, Modifier) encoding of
// Ctrl-C, used to RESERVE Ctrl-C at the non-editable shim layer
// (installShimsForScope skips it) so installEmergencyQuitShim is the single
// Ctrl-C handler per view. Computed once from ctrlCKey via the same
// ChordKeyToGocui path installEmergencyQuitShim uses.
var ctrlCGocuiKey, ctrlCGocuiMod, _ = keys.ChordKeyToGocui(types.ChordKey{Code: 'c', Mod: types.ChordModCtrl})

// emergencyQuit is the un-rebindable Ctrl-C escape hatch (R5). It routes
// straight to the existing clean-quit path (QuitController.Quit — the same
// guarded flow the default app.quit binding fires), BYPASSING the
// keybinding trie entirely. Because the interception lives in code (the
// editor Ctrl-C guard + installEmergencyQuitShim) rather than in the
// user-replaceable trie, a user config can neither remove nor shadow it:
// pressing Ctrl-C always quits, so <c-c> is no longer user-remappable. The
// pending-edit / open-transaction guards in QuitController.Quit still
// apply — the emergency aspect is config-independence, not guard-bypass.
//
// Returns nil when no controller is wired (test rigs that never built
// controllers), so the caller treats Ctrl-C as a harmless no-op rather
// than panicking.
func (g *Gui) emergencyQuit() error {
	if g.controllers == nil || g.controllers.Quit == nil {
		return nil
	}
	return g.controllers.Quit.Quit(commands.ExecCtx{})
}

// installEmergencyQuitShim registers an unconditional Ctrl-C SetKeybinding
// on view whose handler is emergencyQuit. Mirrors installEscAbortShim:
// Ctrl-C may or may not have a trie-reachable shim depending on user
// config, so this guarantees gocui always has a handler for it on every
// non-editable view (and globally with view=="") — independent of whether
// the user kept, moved, or removed the app.quit binding. The handler
// returns gocui.ErrQuit (or nil when a guard fires), which gocui's run
// loop unwinds.
//
// This is the SOLE Ctrl-C handler per view: installShimsForScope RESERVES
// Ctrl-C (early-continue, skipping any trie shim for it), so no competing
// user <c-c> handler is ever registered on a non-editable view. The
// emergency path therefore wins regardless of gocui's first-registered-wins
// scan order (gocui gui.go:1546) — the guarantee no longer depends on
// registration order.
func (g *Gui) installEmergencyQuitShim(view string) error {
	gk, gmod, err := keys.ChordKeyToGocui(types.ChordKey{Code: 'c', Mod: types.ChordModCtrl})
	if err != nil {
		return nil
	}
	return g.driver.SetKeybinding(view, gk, gmod, g.emergencyQuit)
}

// shimKey deduplicates (view, key, mod) tuples within a single
// installShimsForScope invocation. The same root Key may appear under
// multiple modes for the same scope; gocui has no mode dimension so we
// only need one SetKeybinding per (view, key, mod).
type shimKey struct {
	view string
	gk   types.Key
	gmod types.Modifier
}

// RunAndHandleError is the production entry. It builds the driver,
// installs the manager, and runs the gocui main loop. gocui.ErrQuit
// from MainLoop is the normal shutdown path and collapses to nil.
//
// gocui's MainLoop has no panic recovery of its own, so a panic in an
// event handler / Layout / flush unwinds straight through here to the Go
// runtime, which prints the stack to stderr — lost the moment the TUI tears
// down and the terminal scrollback is reset. The deferred guard records the
// panic value + full goroutine stack to the session log (which the deferred
// Close in the caller flushes during unwind) and then re-panics, preserving
// the exact crash semantics while leaving a durable post-mortem breadcrumb.
//
// NOTE: this only covers panics on the MainLoop goroutine. A panic on a
// worker goroutine (OnWorker) still aborts the process without this log.
func (g *Gui) RunAndHandleError() (err error) {
	if initErr := g.initGocui(); initErr != nil {
		return initErr
	}
	defer func() {
		if r := recover(); r != nil {
			logPanicStack(g.deps.Common.Logger(), r)
			panic(r)
		}
	}()
	err = g.driver.MainLoop()
	if err == nil || err == gocui.ErrQuit {
		return nil
	}
	return err
}

// logPanicStack records a recovered panic value and the current goroutine
// stack to the session log under cat=app, evt=panic. Extracted from the
// RunAndHandleError guard so the formatting is unit-testable without
// triggering the surrounding recover / re-panic. No-op on a nil logger.
func logPanicStack(logger *slog.Logger, recovered any) {
	if logger == nil {
		return
	}
	logs.Event(logger, "app", "panic",
		slog.Any("value", recovered),
		slog.String("stack", string(debug.Stack())),
	)
}

// Close runs the M15c shutdown sequence:
//  1. workersWG.Wait
//  2. activeSQLSession.Close
//  3. queryRunner.Unbind
//  4. history.Close
//  5. store.Flush + store.Close
//  6. driver.Close (gocui TUI driver)
//  7. LogCloser.Close (2 s deadline; AD-16)
//
// Idempotent.
func (g *Gui) Close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	// Flush the query editor buffer to disk synchronously. The MainLoop
	// has already exited so HandleFocusLost was never fired — save the
	// buffer directly rather than dispatching via OnWorker.
	g.flushQueryEditorBuffer()
	// Stop in-flight result-tab streams BEFORE draining workers. A
	// ResultBufferManager task that has reached EOF parks forever in its
	// post-EOF chan loop (result_buffer_manager.go) and only exits when
	// its per-task stopCh fires. Without firing it first, the
	// g.workersWG.Wait() below blocks on that parked OnWorker goroutine
	// and Close() never returns (the shutdown hang fixed here).
	// PreemptInFlight stops every StateRunning/StateQueued tab and blocks
	// until each worker has run its cleanup + workersWG.Done.
	if g.resultTabsH != nil {
		g.resultTabsH.PreemptInFlight()
	}
	// U8: stop the busy-spinner ticker UNCONDITIONALLY here — BEFORE the
	// workersWG.Wait() below and BEFORE driver.Close(). The ticker drain
	// goroutine is registered on workersWG and only exits on spinnerStop;
	// if busy>0 at shutdown the ticker is still armed, so stopping it first
	// (a) lets workersWG.Wait() complete instead of hanging on the parked
	// drain goroutine, and (b) guarantees no OnUIThreadContentOnly fires
	// into a torn-down MainLoop after driver.Close(). stopSpinner is
	// idempotent (nil ticker → no-op) so the never-armed path is safe.
	g.stopSpinner()
	// Drain any in-flight OnWorker goroutines before the store/driver
	// teardown so the goleak smoke tests see a quiescent goroutine pool
	// (DESIGN.md §17). Safe to call when no workers were ever spawned —
	// sync.WaitGroup.Wait on a zero counter returns immediately.
	g.spinnerState.workersWG.Wait()
	var firstErr error
	// Close the active SQLSession FIRST so an in-flight Stream gets
	// cancelled (SQLSession.Close cancels the live RunHandle and waits
	// briefly for it to terminate) before the history writer drains.
	// Without this ordering a finishing run could push one more
	// historyEntry into a channel whose receiver has already exited.
	if g.queryState.activeSQLSession != nil {
		if err := g.queryState.activeSQLSession.Close(); err != nil {
			firstErr = err
		}
		g.queryState.activeSQLSession = nil
	}
	// Unbind so any controller that still holds the runner sees
	// HasSession() == false. Also resets the runner's `last` handle so
	// Cancel after Close is a silent no-op.
	if g.queryState.queryRunner != nil {
		g.queryState.queryRunner.Unbind()
	}
	if g.queryState.history != nil {
		if err := g.queryState.history.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		g.queryState.history = nil
	}
	if g.deps.Store != nil {
		if err := g.deps.Store.Flush(); err != nil {
			firstErr = err
		}
		if err := g.deps.Store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if g.driver != nil {
		if err := g.driver.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Step 7 — close the per-session log file LAST so any error emitted
	// by the steps above lands in the log (AD-8 revised). Wrapped in a
	// 2 s deadline (AD-16); on timeout the fd is force-closed.
	if g.deps.Common != nil && g.deps.Common.LogCloser != nil {
		closer := g.deps.Common.LogCloser
		var cerr error
		if lc, ok := closer.(logs.LogCloser); ok {
			cerr = lc.CloseWithDeadline(2 * time.Second)
		} else {
			cerr = closer.Close()
		}
		if cerr != nil && firstErr == nil {
			firstErr = cerr
		}
	}
	return firstErr
}

// defaultHistoryProvider opens the per-user history sqlite at the XDG
// state dir. Used when Deps.HistoryProvider is nil (production wiring).
func defaultHistoryProvider() (*query.History, error) {
	return query.New(filepath.Join(env.GetStateDir(), "history.sqlite"))
}

// saveQueryEditorBuffer is the SaveBuffer closure bound into ContextTreeDeps.
// The MainLoop caller (QueryEditorContext.HandleFocusLost)
// has already taken Buffer.String() under the buffer's RLock, so content is
// an immutable string. The closure dispatches the actual fs write to a
// worker so HandleFocusLost returns immediately and the gocui MainLoop is
// never blocked on disk I/O. Empty Common / Fs / StateDir is a silent
// no-op via SaveBufferLines' empty-path guard, which keeps test wiring
// (no Common at construction) safe.
func (g *Gui) saveQueryEditorBuffer(connID, uuid, content string) {
	if g == nil || g.deps.Common == nil {
		return
	}
	fs := g.deps.Common.Fs
	stateDir := g.deps.Common.StateDir
	if fs == nil || stateDir == "" || connID == "" || uuid == "" {
		return
	}
	g.OnWorker(func(_ gocui.Task) error {
		if err := editor.SaveBufferContent(fs, stateDir, connID, uuid, content); err != nil {
			g.deps.Common.Logger().Warn("gui: save query-editor buffer", "err", err)
		}
		return nil
	})
}

// flushQueryEditorBuffer saves the query editor buffer to disk
// synchronously. Called from Close() where the MainLoop has already
// exited — HandleFocusLost never fired so saveBufferIfDirty never ran.
// A synchronous write is safe (and desirable) here because there is no
// UI thread to block.
func (g *Gui) flushQueryEditorBuffer() {
	if g.registry == nil || g.registry.QueryEditor == nil {
		return
	}
	buf := g.registry.QueryEditor.Buffer()
	if buf == nil || !buf.Dirty {
		return
	}
	if g.deps.Common == nil {
		return
	}
	fs := g.deps.Common.Fs
	stateDir := g.deps.Common.StateDir
	connID := buf.ConnectionID
	uuid := buf.UUID
	if fs == nil || stateDir == "" || connID == "" || uuid == "" {
		return
	}
	content := buf.String()
	if err := editor.SaveBufferContent(fs, stateDir, connID, uuid, content); err != nil {
		g.deps.Common.Logger().Warn("gui: flush query-editor buffer on close", "err", err)
	}
	buf.Dirty = false
}

// hiddenSchemasForActiveConn returns the hidden-schemas list for the active
// connection via the Store snapshot accessor.
// Nil Store or empty activeConnID collapse to nil so the context applies no
// runtime filter — matching the test-wiring contract.
func (g *Gui) hiddenSchemasForActiveConn() []string {
	if g == nil || g.deps.Store == nil {
		return nil
	}
	connID := g.connectionState.activeConnID
	if connID == "" {
		return nil
	}
	return g.deps.Store.HiddenSchemasSnapshot(connID)
}

// HelperBagForTest returns the HelperBag the most recent wireWithDriver
// installed on the controllers, by reconstructing the surface from the
// orchestrator's own fields. Test-only — used by wiring_query_test.go to
// assert that connectInvoker.Connect causes HelperBag.QueryRunner to
// flip to HasSession() == true. Returns the zero HelperBag before any
// wireWithDriver pass has run.
func (g *Gui) HelperBagForTest() controllers.HelperBag {
	var qec *guicontext.QueryEditorContext
	if g.registry != nil {
		qec = g.registry.QueryEditor
	}
	connectInv := &connectInvoker{g: g, helper: g.connectHelper, runner: g.queryState.queryRunner, history: g.queryState.history}
	return controllers.HelperBag{
		NavDeps: controllers.NavDeps{
			Connect: connectInv,
		},
		QueryDeps: controllers.QueryDeps{
			QueryRunner:  g.queryState.queryRunner,
			EditorBuffer: newEditorBufferAdapter(qec),
		},
	}
}

// CompletionSourcesForTest returns the source list of the completion engine
// wired by wireEditorCompletion, or nil when the VimEditor
// controller or its engine is unwired. Test-only.
func (g *Gui) CompletionSourcesForTest() []editor.Source {
	if g.controllers == nil || g.controllers.VimEditor == nil {
		return nil
	}
	eng := g.controllers.VimEditor.CompletionEngineForTest()
	if eng == nil {
		return nil
	}
	return eng.Sources()
}

// ActiveSQLSessionForTest returns the SQLSession the most recent Connect
// installed, or nil. Test-only.
func (g *Gui) ActiveSQLSessionForTest() *session.SQLSession { return g.queryState.activeSQLSession }

// ActiveConnIDForTest returns the active connection ID set by the most
// recent successful Connect. Test-only — used by the
// supersession test to assert a stale connect did not clobber it.
func (g *Gui) ActiveConnIDForTest() string { return g.connectionState.activeConnID }

// BumpConnectGenForTest advances the supersession token, simulating a
// newer activation arriving while a prior connect is still in flight.
// Test-only.
func (g *Gui) BumpConnectGenForTest() { g.connectionState.connectGen.Add(1) }

// LastConnectionIDForTest returns the persisted AppState.LastConnectionID,
// or "" when the Store is unwired. Test-only — the cancel-supersession test
// asserts a cancel-after-dial-success does NOT stamp it.
func (g *Gui) LastConnectionIDForTest() string {
	if g.deps.Store == nil {
		return ""
	}
	return g.deps.Store.LastConnectionIDSnapshot()
}

// PopulateIndexesRailForTest invokes the side-effect of <CR>-on-TABLES
// against the connectInvoker built by wireWithDriver: it loads indexes
// for (schema, table) via the live ConnectHelper and pushes them into
// IndexesContext. Test-only — exercised by adapters_test.go to assert
// the INDEXES-rail population path.
func (g *Gui) PopulateIndexesRailForTest(schema, table string) {
	if g == nil {
		return
	}
	inv := &connectInvoker{g: g, helper: g.connectHelper, runner: g.queryState.queryRunner, history: g.queryState.history}
	inv.populateIndexesRail(context.Background(), schema, table)
}

// PopulateColumnsRailForTest mirrors PopulateIndexesRailForTest for the
// COLUMNS rail. Test-only.
func (g *Gui) PopulateColumnsRailForTest(schema, table string) {
	if g == nil {
		return
	}
	inv := &connectInvoker{g: g, helper: g.connectHelper, runner: g.queryState.queryRunner, history: g.queryState.history}
	inv.populateColumnsRail(context.Background(), schema, table)
}

// OnWorkerCountForTest returns the cumulative OnWorker invocation count
// since wireWithDriver. Backed by onWorkerSampleCounter (also used by
// AD-20 sampling).
func (g *Gui) OnWorkerCountForTest() uint64 {
	if g == nil {
		return 0
	}
	return g.spinnerState.onWorkerSampleCounter.Load()
}

// WaitForWorkersForTest blocks until every OnWorker goroutine launched
// before this call has finished. Test-only quiescence helper that
// piggybacks on the workersWG used by Close.
func (g *Gui) WaitForWorkersForTest() {
	if g == nil {
		return
	}
	g.spinnerState.workersWG.Wait()
}

// ChoiceHelperForTest returns the ChoiceHelper wired by wireWithDriver,
// or nil before that pass ran. Test accessor used by wiring tests
// to confirm the ChainedPrompter adapter has a real picker behind it.
func (g *Gui) ChoiceHelperForTest() *ui.ChoiceHelper { return g.choiceHelp }

// PromptHelperForTest returns the PromptHelper wired by wireWithDriver,
// or nil before that pass ran. Test accessor used by end-to-end
// integration tests to quiesce on Active() between popup steps.
func (g *Gui) PromptHelperForTest() *ui.PromptHelper { return g.promptHelp }

// SeedPromptBufferForTest writes s into the PROMPT context's test-mode
// buffer. The PROMPT view is now editable: in production
// gocui.DefaultEditor writes user keystrokes directly into v.TextArea
// and PromptContext.Buffer() reads from there. Under the recorder
// driver (integration tests) SetView returns nil, so the TextArea
// branch is unreachable — tests use this helper to inject the typed
// value before dispatching <cr> via FeedKey.
func (g *Gui) SeedPromptBufferForTest(s string) {
	if g.registry.Prompt != nil {
		g.registry.Prompt.SetBuffer(s)
	}
}

// QuitOnSignal asks the gocui MainLoop to exit cleanly by enqueueing a
// gocui.ErrQuit-returning closure on the Update queue.
func (g *Gui) QuitOnSignal() {
	if g.driver == nil {
		return
	}
	g.driver.Update(func() error { return gocui.ErrQuit })
}

// ContextTree returns the focus stack. Test accessor.
func (g *Gui) ContextTree() *gui.ContextTree { return g.tree }

// Registry returns the context registry. Test accessor.
func (g *Gui) Registry() *guicontext.ContextTree { return g.registry }

// Controllers returns the controller bundle. Test accessor.
func (g *Gui) Controllers() *controllers.Controllers { return g.controllers }

// CommandRegistry returns the commands.Registry. Test accessor.
func (g *Gui) CommandRegistry() *commands.Registry { return g.keybindingSystem.cmdRegistry }

// ExRegistry returns the ex-command registry. Test accessor.
func (g *Gui) ExRegistry() *keys.ExRegistry { return g.keybindingSystem.exRegistry }

// Matcher returns the active Matcher. Test accessor.
func (g *Gui) Matcher() *keys.Matcher { return g.keybindingSystem.matcher }

// WhichKey returns the WhichKey notifier. Test accessor — reads
// Visible() to assert the popup mechanic.
func (g *Gui) WhichKey() *keys.WhichKey { return g.keybindingSystem.whichkey }

// ModeStore returns the ModeStore. Test accessor — toggles modes
// to exercise the mode-conditional dispatch paths.
func (g *Gui) ModeStore() *keys.ModeStore { return g.keybindingSystem.modeStore }

// Warnings returns the Warning slice captured during the most recent
// wireWithDriver Build pass. Test accessor used by the smoke
// walkthrough to assert ambient warnings.
func (g *Gui) Warnings() []keys.Warning { return g.keybindingSystem.lastWarnings }

// ToastHelper returns the toast helper. Test accessor — reads
// History() to assert reload / toast emissions.
func (g *Gui) ToastHelper() *ui.ToastHelper { return g.toastHelp }

// ResultTabsHelper returns the live result-tabs helper, or nil before
// wireWithDriver runs. Test accessor — the smoke test walks
// through Open/Pin/eviction via this surface.
func (g *Gui) ResultTabsHelper() *ui.ResultTabsHelper { return g.resultTabsH }

// MasterEditorForTest returns the gocui.Editor installed for key by the
// most recent installKeyDispatch pass, or nil. Test accessor —
// asserts the QUERY_EDITOR VimEditor has (or has not) the
// as-you-type auto-completer wired per editor.autocomplete.
func (g *Gui) MasterEditorForTest(key types.ContextKey) gocui.Editor {
	if g.keybindingSystem.masterEditors == nil {
		return nil
	}
	return g.keybindingSystem.masterEditors[key]
}

// leaderRunesFromCfg extracts the leader / localleader runes from cfg,
// using the same fallbacks as keys.KeybindingService.Build.
func leaderRunesFromCfg(cfg *config.UserConfig) (rune, rune) {
	leader := ' '
	localLeader := ','
	if cfg == nil {
		return leader, localLeader
	}
	if cfg.Leader != "" {
		for _, r := range cfg.Leader {
			leader = r
			break
		}
	}
	if cfg.LocalLeader != "" {
		for _, r := range cfg.LocalLeader {
			localLeader = r
			break
		}
	}
	return leader, localLeader
}

// resolveKeyDelays merges the (optional) test overrides with the config
// values and finally hardcoded defaults. Positive override fields win;
// zero / negative fields fall through to cfg, then to the documented
// defaults (1s / 50ms / 300ms).
func resolveKeyDelays(cfg *config.UserConfig, overrides *keyDelayOverrides) (time.Duration, time.Duration, time.Duration) {
	tlen := 1 * time.Second
	ttlen := 50 * time.Millisecond
	wdelay := 300 * time.Millisecond
	if cfg != nil {
		if cfg.TimeoutLen > 0 {
			tlen = cfg.TimeoutLen
		}
		if cfg.TtimeoutLen > 0 {
			ttlen = cfg.TtimeoutLen
		}
		if cfg.WhichKeyDelay > 0 {
			wdelay = cfg.WhichKeyDelay
		}
	}
	if overrides != nil {
		if overrides.timeoutLen > 0 {
			tlen = overrides.timeoutLen
		}
		if overrides.ttimeoutLen > 0 {
			ttlen = overrides.ttimeoutLen
		}
		if overrides.whichKeyDelay > 0 {
			wdelay = overrides.whichKeyDelay
		}
	}
	return tlen, ttlen, wdelay
}

// defaultHiddenPatterns is the SchemasInvoker.UnhideSchema input — the
// pg-driver builtin schemas are the only patterns this epic recognises.
func defaultHiddenPatterns() ([]string, []string) {
	return pg.BuiltinHiddenSchemas, nil
}

// fsFromCommon extracts the afero.Fs from Common; returns nil if
// Common is nil (a malformed Deps; downstream code nil-checks).
func fsFromCommon(c *common.Common) afero.Fs {
	if c == nil {
		return nil
	}
	return c.Fs
}

// scopeExistsPredicate returns the ScopeExists predicate used by
// config.ValidateUserConfig. A scope string is valid if it is one of:
//   - "" or "global" (collapsed to GLOBAL by KeybindingService),
//   - "all" (pseudo-scope expanded by KeybindingService.scopesFor),
//   - any ContextKey registered in g.registry (matched via ByKey).
func (g *Gui) scopeExistsPredicate() func(string) bool {
	return func(s string) bool {
		switch s {
		case "", "global", "all":
			return true
		}
		if g.registry == nil {
			return false
		}
		return g.registry.ByKey(types.ContextKey(s)) != nil
	}
}
