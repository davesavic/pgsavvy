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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/cheatsheet"
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/env"
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
	"github.com/davesavic/dbsavvy/pkg/tasks"
)

// Deps is the dependency bag NewGui consumes. Composed by the entry
// point (pkg/app) from XDG paths, the AppStateStore, and a closure that
// re-loads connections.yml on demand.
type Deps struct {
	// Common is the cross-cutting bag (Log, Tr, UserConfig, AppState, Fs).
	Common *common.Common

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
		g.delayOverrides = &keyDelayOverrides{
			timeoutLen:    timeoutLen,
			ttimeoutLen:   ttimeoutLen,
			whichKeyDelay: whichKeyDelay,
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

	// Focus stack; driver-free.
	tree *gui.ContextTree

	// Data helpers (driver-free).
	connectHelper *data.ConnectHelper
	schemasHelper *data.SchemasHelper
	formHelper    *data.ConnectionFormHelper
	refreshHelper *data.RefreshHelper

	// Built by wireWithDriver.
	registry    *guicontext.ContextTree
	controllers *controllers.Controllers
	confirmHelp *ui.ConfirmHelper
	promptHelp  *ui.PromptHelper
	choiceHelp  *ui.ChoiceHelper
	toastHelp   *ui.ToastHelper
	tablesHelp  *ui.TablesHelper
	tipHelp     *ui.TipHelper
	resultTabsH *ui.ResultTabsHelper
	noticeHelp  *ui.NoticeHelper

	// Keybinding system (built by wireWithDriver).
	cmdRegistry *commands.Registry
	matcher     *keys.Matcher
	modeStore   *keys.ModeStore
	whichkey    *keys.WhichKey
	exRegistry  *keys.ExRegistry
	// kbRuntime is the keys.Runtime composite handed to controllers via
	// HelperBag.KbRuntime. Retained on Gui so RunLayout's Tier-4 status
	// pass (dbsavvy-tro.3) can hand it to RenderStatusLine without
	// rebuilding the value every frame.
	kbRuntime *keys.Runtime

	// lastWarnings captures the Warning slice returned by the most recent
	// KeybindingService.Build run during wireWithDriver. Surfaced via the
	// Warnings() accessor for the dlp.14 integration smoke test.
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

	// Connection state surfaced by the activeConnAdapter.
	activeConnID string

	// Query runtime (dbsavvy-66p.16). queryRunner is built empty in
	// wireWithDriver and stashed in the HelperBag so controllers' value-
	// copy of the bag stays valid across Bind / Unbind. history is a
	// process-wide singleton opened lazily on first wireWithDriver and
	// closed in Gui.Close. activeSQLSession is the SQLSession the most
	// recent connectInvoker.Connect built; Close cancels any in-flight
	// run via its Close().
	queryRunner      *data.QueryRunner
	history          *query.History
	activeSQLSession *session.SQLSession

	// closed is true once Close has run; idempotent guard.
	closed bool

	// Threading-model state (DESIGN.md §17). See threading.go for the
	// OnUIThread / OnUIThreadContentOnly / OnWorker methods that consume
	// these fields.
	//
	//   - busy is the in-flight-worker counter (atomic; ticked by
	//     OnWorker, read by BusyCount for the bottom spinner).
	//   - workersWG joins live OnWorker goroutines on shutdown so the
	//     goleak smoke tests have a deterministic quiescence point.
	//   - mutexes is the named-mutex bag (RefreshingMutex / PopupMutex /
	//     FetchMutex). Downstream tasks (66p.5/9/12/13/14) plug into it.
	busy      int64
	workersWG sync.WaitGroup
	mutexes   types.Mutexes

	// onWorkerSampleCounter implements the AD-20 quiescence-preserving
	// sampling for cat=state worker_start / worker_end emits. Every
	// OnWorker invocation increments it; the counter % 10 == 0 sample
	// gate plus mandatory quiescence-transition emits (busy_before==0 /
	// busy_after==0) together yield 2 + N/10 worker lines per burst.
	// Per AD-20 this MUST be a field on *Gui (not package-level) so
	// concurrent test Guis don't share state.
	onWorkerSampleCounter atomic.Uint64
}

// NewGui builds every collaborator that doesn't depend on the live
// GuiDriver. The driver-dependent wiring (context registry, UI helpers,
// controllers, key/mouse bindings) waits for either initGocui (prod)
// or UseDriverForTest (test).
func NewGui(deps Deps, opts ...Option) *Gui {
	g := &Gui{
		deps: deps,
		tree: gui.NewContextTree(),
	}
	g.connectHelper = data.NewConnectHelper()
	g.schemasHelper = data.NewSchemasHelper(deps.Common, deps.Store)
	g.formHelper = data.NewConnectionFormHelper(deps.Common, fsFromCommon(deps.Common), deps.ConnectionsPath, deps.DriverNamesFn)
	g.refreshHelper = data.NewRefreshHelper(g.connectHelper)
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
		OutputMode:      gocui.OutputNormal,
		SupportOverlaps: false,
	})
	if err != nil {
		return fmt.Errorf("gui: gocui.NewGui: %w", err)
	}
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

	// Build the keybinding-system collaborators.
	g.cmdRegistry = commands.NewRegistry()
	g.modeStore = keys.NewModeStore()
	g.whichkey = keys.NewWhichKey()
	g.exRegistry = keys.NewExRegistry()

	// dbsavvy-8s2.5: wire the per-session logger into the input-side
	// stores so mode_set / mode_reset / ctx_* events flow through
	// logs.Event. nil-safe — logs.Event short-circuits on nil.
	g.modeStore.SetSessionLog(g.deps.Common.Logger())
	if g.tree != nil {
		g.tree.SetSessionLog(g.deps.Common.Logger())
	}

	leader, _ := leaderRunesFromCfg(cfg)
	tlen, ttlen, wdelay := resolveKeyDelays(cfg, g.delayOverrides)
	matcher, err := keys.NewMatcher(nil, keys.MatcherConfig{
		Modes:         g.modeStore,
		Leader:        leader,
		TimeoutLen:    tlen,
		TtimeoutLen:   ttlen,
		WhichKeyDelay: wdelay,
		Registers:     keys.NewRegisterStore(),
		WhichKey:      g.whichkey,
		Log:           g.deps.Common.Logger(),
	})
	if err != nil {
		return fmt.Errorf("gui: NewMatcher: %w", err)
	}
	g.matcher = matcher
	// dbsavvy-8s2.5: wire the per-session logger into the matcher so
	// chord_resolved events flow through logs.Event. nil-safe.
	g.matcher.SetSessionLog(g.deps.Common.Logger())
	runtime := keys.NewRuntime(g.cmdRegistry, matcher, g.modeStore, g.whichkey, g.exRegistry)
	g.kbRuntime = runtime

	// Cheatsheet render closure. Captures the live matcher + tr so the
	// CheatsheetContext renders the current TrieSet snapshot every time
	// `?` is pressed. Returns the empty string when the matcher hasn't
	// published a TrieSet yet (early bootstrap).
	cheatsheetRender := func(scope types.ContextKey) string {
		if g.matcher == nil {
			return ""
		}
		ts := g.matcher.TrieSet()
		if ts == nil {
			return ""
		}
		out := cheatsheet.Generate(cheatsheet.GenerateInput{
			Trie:  ts,
			Scope: scope,
			Tr:    tr,
		})
		return cheatsheet.Render(out, tr, cheatsheet.ScopeLabel(scope, tr))
	}

	// WhichKey rows resolver (dbsavvy-tro.4). Captures the live matcher +
	// modeStore so WhichKeyContext.HandleRender pulls the immediate
	// children of the current (scope, prefix) on every render frame.
	// Returns nil when the matcher hasn't published a TrieSet yet, when
	// the (mode, scope) tuple has no trie, or when prefix doesn't resolve
	// inside that trie — the context's HandleRender treats nil rows as a
	// silent no-op (see whichkey_context.go:73-76).
	whichKeyRows := func(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow {
		if g.matcher == nil || g.modeStore == nil {
			return nil
		}
		ts := g.matcher.TrieSet()
		if ts == nil {
			return nil
		}
		mode := g.modeStore.Get(scope)
		trie, ok := ts.Get(mode, scope)
		if !ok || trie == nil {
			return nil
		}
		rows, ok := trie.ChildrenAt(prefix)
		if !ok {
			return nil
		}
		return rows
	}

	// Build the context registry with hooks closed over the driver.
	ctxDeps := types.ContextTreeDeps{
		GuiDriver:            g.driver,
		EmptyStateHook:       data.NewEmptyStateHook(tr, provider),
		PresentationHook:     presentation.NewPresentationHook(),
		PerRowDecorationHook: presentation.NewPerRowDecorationHook(),
		LimitText:            presentation.NewLimitText(tr),
		ModeStore:            g.modeStore,
		WhichKey:             g.whichkey,
		WhichKeyRows:         whichKeyRows,
		CheatsheetRender:     cheatsheetRender,
		// dbsavvy-wwd.1: QueryEditorContext.HandleFocusLost calls
		// matcher.Cancel via this minimal interface to keep
		// pkg/gui/context decoupled from pkg/gui/keys.
		Matcher: g.matcher,
		// dbsavvy-wwd.9: buffer-save dispatch closure. The MainLoop
		// caller already supplies a string snapshot (Buffer.String
		// takes RLock); the worker just writes raw `.sql` text to disk.
		// Common.Fs / Common.StateDir may be nil/empty in test wiring —
		// the closure short-circuits via SaveBufferLines' empty-path
		// guard so this stays safe for fixtures.
		SaveBuffer: g.saveQueryEditorBuffer,
		// dbsavvy-56u.4: runtime-hidden lookup for SchemasContext.
		// renderRows uses this to skip AppState.HiddenSchemas[connID]
		// entries unless showHiddenMode is on. Closure captures the live
		// AppState pointer and the activeConnID; both can be empty in
		// test wiring → empty slice, no filtering applied.
		HiddenSchemasForActiveConn: g.hiddenSchemasForActiveConn,
		// dbsavvy-56u.2: first-run welcome tip copy. Nil-safe when tr is
		// absent (test fixtures) — the context renders nothing.
		FirstRunTipText: func() (string, string) {
			if tr == nil {
				return "", ""
			}
			return tr.FirstRunTipTitle, tr.FirstRunTipBody
		},
	}
	g.registry = guicontext.NewContextTree(ctxDeps)

	// UI helpers that need the driver / registry.
	g.confirmHelp = ui.NewConfirmHelper(g.tree, g.registry.Confirmation)
	g.promptHelp = ui.NewPromptHelper(g.tree, g.registry.Prompt)
	g.choiceHelp = ui.NewChoiceHelper(g.tree, g.registry.Selection)
	g.toastHelp = ui.NewToastHelper(g.driver)
	if g.deps.Common != nil {
		g.toastHelp.SetLogger(g.deps.Common.Logger())
	}
	g.tablesHelp = ui.NewTablesHelper(g.toastHelp, tr)
	g.tipHelp = ui.NewTipHelper(g.tree, g.deps.Store)

	// ResultTabsHelper owns the multi-tab pane in the secondary slot.
	// Each tab gets its own ResultBufferManager built against the
	// orchestrator's threading helpers. dbsavvy-66p.12.
	resultTabsDeps := ui.ResultTabsHelperDeps{
		Driver:     g.driver,
		Toast:      g.toastHelp,
		Confirm:    g.confirmHelp,
		Prompt:     g.promptHelp,
		Choice:     g.choiceHelp,
		OnUIThread: g.OnUIThread,
		StreamFactory: func() ui.StreamRunner {
			rbm := tasks.New(g.OnWorker, g.OnUIThreadContentOnly)
			if g.deps.Common != nil {
				rbm.SetLogger(g.deps.Common.Logger())
			}
			return rbm
		},
		// dbsavvy-uv0.6: AppStateStore drives the per-(connID, baseTable)
		// hidden-column persistence used by the <leader>gH overlay.
		Store: g.deps.Store,
	}
	// dbsavvy-uv0.6: focus-stack push/pop closures for the HIDE_OVERLAY
	// popup. The helper holds the overlay state object; PushHideOverlay
	// installs an adapter on the context (so HandleRender reads the
	// helper's body) and pushes the popup; PopHideOverlay pops it.
	if g.registry.HideOverlay != nil && g.tree != nil {
		resultTabsDeps.PushHideOverlay = func() error {
			g.registry.HideOverlay.SetState(hideOverlayStateAdapter{helper: g.resultTabsH})
			return g.tree.Push(g.registry.HideOverlay)
		}
		resultTabsDeps.PopHideOverlay = func() error {
			return g.tree.Pop()
		}
	}
	// dbsavvy-uv0.9: focus-stack push/pop closures for the EXPORT_MENU
	// popup + OnWorker for the export pipeline.
	if g.registry.ExportMenu != nil && g.tree != nil {
		resultTabsDeps.PushExportMenu = func() error {
			g.registry.ExportMenu.SetState(exportMenuStateAdapter{helper: g.resultTabsH})
			return g.tree.Push(g.registry.ExportMenu)
		}
		resultTabsDeps.PopExportMenu = func() error {
			return g.tree.Pop()
		}
	}
	resultTabsDeps.OnWorker = g.OnWorker
	if tr != nil {
		resultTabsDeps.SortPickLabel = tr.Actions.ResultSortPickLabel
	}
	if cfg := g.deps.Common.Cfg(); cfg != nil {
		resultTabsDeps.ResultPageSize = cfg.UI.ResultPageSize
		resultTabsDeps.ReadToEndWarnThreshold = cfg.UI.ReadToEndWarnThreshold
		resultTabsDeps.FilterMaxRegexBytes = cfg.UI.FilterMaxRegexBytes
		resultTabsDeps.MouseDoubleClickMs = cfg.UI.Mouse.DoubleClickMs
		resultTabsDeps.ExportBufferedRowWarnThreshold = cfg.UI.Export.BufferedRowWarnThreshold
		resultTabsDeps.ExportClipboardMaxBytes = cfg.UI.Export.ClipboardMaxBytes
	}
	g.resultTabsH = ui.NewResultTabsHelper(resultTabsDeps)

	// NoticeHelper routes server NOTICE / WARNING messages from streaming
	// queries to the messages panel and a first-of-run toast. The
	// messages sink hops driver.Write onto the UI thread via
	// OnUIThreadContentOnly so the helper itself can run from a worker
	// goroutine (DESIGN.md §17). dbsavvy-66p.13.
	messagesSink := ui.NewDefaultMessagesSink(g.driver, g.OnUIThreadContentOnly)
	g.noticeHelp = ui.NewNoticeHelper(ui.NoticeHelperDeps{
		Sink:     messagesSink,
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
	if g.history == nil {
		hp := g.deps.HistoryProvider
		if hp == nil {
			hp = defaultHistoryProvider
		}
		h, hErr := hp()
		if hErr != nil {
			g.deps.Common.Logger().Warn("gui: history open", "err", hErr)
		} else {
			g.history = h
		}
	}

	// Build the empty QueryRunner shell that survives Connect / Disconnect
	// cycles. connectInvoker.Bind swaps the inner session atomically.
	if g.queryRunner == nil {
		g.queryRunner = data.NewQueryRunner(nil, drivers.Capabilities{})
	}

	connectInv := &connectInvoker{g: g, helper: g.connectHelper, runner: g.queryRunner, history: g.history}

	helperBag := controllers.HelperBag{
		Driver:           g.driver,
		Logger:           g.deps.Common.Logger(),
		Connections:      connectionsPickerAdapter{registry: g.registry.Connections},
		Schemas:          schemasPickerAdapter{registry: g.registry.Schemas},
		Tables:           tablePicker,
		ActiveConnection: &activeConnAdapter{g: g},
		Connect:          connectInv,
		SchemasHelper:    g.schemasHelper,
		ConnectionForm:   &connectionFormInvoker{g: g, helper: g.formHelper, prompter: newChainedPrompterAdapter(g.promptHelp, g.choiceHelp, g.OnUIThread)},
		Confirm:          g.confirmHelp,
		Prompt:           g.promptHelp,
		Choice:           g.choiceHelp,
		Toast:            g.toastHelp,
		Refresh:          g.refreshHelper,
		Tip:              g.tipHelp,
		TableDouble:      g.tablesHelp,
		Menu:             &menuPushHelper{tree: g.tree, menu: g.registry.Menu},
		ResultTabs:       g.resultTabsH,
		// PlanController dispatches against the active plan tab's
		// PlanContext (dbsavvy-uv0.8). Closing over g.resultTabsH so
		// ActivePlanContext stays in lockstep with whatever the user
		// has currently focused. Nil-safe — returns nil when the
		// helper is unwired or no plan tab is active.
		ActivePlanContextFn: func() *guicontext.PlanContext {
			if g.resultTabsH == nil {
				return nil
			}
			return g.resultTabsH.ActivePlanContext()
		},
		Notice:         g.noticeHelp,
		QueryRunner:    g.queryRunner,
		EditorBuffer:   newEditorBufferAdapter(g.registry.QueryEditor),
		HiddenPatterns: defaultHiddenPatterns,
		KbRuntime:      runtime,
		// <CR> on a schema row reloads the TABLES rail via a worker
		// (dbsavvy-04n). The handler runs on the gocui MainLoop; the
		// driver call must hop to the worker queue so MainLoop is not
		// blocked by a slow ListTables. populateTablesRail itself is
		// safe to call from any goroutine — SetItems just mutates the
		// in-memory slice (see refreshConnectionsRail comment).
		OnSchemaActivate: func(schema string) {
			g.OnWorker(func(_ gocui.Task) error {
				connectInv.populateTablesRail(context.Background(), schema)

				// Push the refreshed TABLES context onto the focus stack so the
				// user lands there after picking a schema.
				connectInv.g.tree.Push(g.registry.Tables)
				return nil
			})
		},

		// <CR> on a table row loads the COLUMNS rail for the selected
		// table on a worker (mirrors OnSchemaActivate). The driver
		// LoadColumns call must not block MainLoop; the focus push runs
		// inside the worker after items are set, so the next layout frame
		// sees the populated rail.
		OnTableActivate: func(table *models.Table) error {
			if table == nil {
				return nil
			}
			g.OnWorker(func(_ gocui.Task) error {
				connectInv.populateColumnsRail(context.Background(), table.Schema, table.Name)
				if len(g.registry.Columns.Items()) != 0 {
					_ = g.tree.Push(g.registry.Columns)
				}
				return nil
			})
			return nil
		},

		// Threading helpers (DESIGN.md §17 / dbsavvy-66p.1). Bound to the
		// Gui's methods so controllers can schedule UI-thread work and
		// spawn background workers without importing the orchestrator.
		OnUIThread:            g.OnUIThread,
		OnUIThreadContentOnly: g.OnUIThreadContentOnly,
		OnWorker:              g.OnWorker,
	}
	g.controllers = controllers.AttachControllers(g.registry, g.deps.Common, helperBag)

	// Wire the popup-body state readers now that both the helpers (which
	// own label / active / choices / cursor) and the PromptController
	// (which owns the typed line buffer) exist. Without this, RunLayout
	// SetView's the popup rectangle but ctx.HandleRender's no-op leaves
	// the popup body empty — the user sees an empty box.
	if g.registry.Prompt != nil {
		g.registry.Prompt.SetState(&promptStateAdapter{
			helper: g.promptHelp,
		})
		// Wire the per-scope ModeSetter so PromptContext.HandleFocus can
		// flip the PROMPT scope into ModeCommand on push. Without this
		// the master Editor's Passthrough branch would not delegate to
		// gocui.DefaultEditor and paste / arrow-key edits would silently
		// drop (dbsavvy-7k9, dbsavvy-f5t).
		g.registry.Prompt.SetModes(g.modeStore)
		// Plumb the read-and-clear surface so PromptController.Submit
		// reads the typed value from the view's TextArea (production
		// path) instead of an internal buffer the controller no longer
		// maintains. The Reset hook also routes through here so
		// helper.Prompt(initial=...) re-seeds the TextArea via the
		// context.
		if g.controllers.Prompt != nil {
			g.controllers.Prompt.SetBufferReader(g.registry.Prompt)
		}
	}
	if g.registry.Selection != nil {
		g.registry.Selection.SetState(g.choiceHelp)
	}
	// Wire the gocui caret toggle through PromptHelper's lifecycle.
	// SetViewCursor positions the caret each frame, but gocui's flush
	// only calls Screen.ShowCursor when g.Cursor is true. Without this
	// the PROMPT popup renders its body but no caret appears. Mirrors
	// CommandLineCommandDeps.CaretToggler.
	if g.promptHelp != nil {
		g.promptHelp.SetCaretToggler(func(enabled bool) {
			if g.driver != nil {
				g.driver.SetCaretEnabled(enabled)
			}
		})
	}

	// Register every controller's action handlers with the registry.
	g.controllers.RegisterActions(g.cmdRegistry)

	// Rail-switch (1-6, Tab) needs the focus tree + context registry,
	// which the Controllers aggregate does not hold; register here. The
	// results-resolver closes over g.resultTabsH so digit 6 / cycle-to-
	// results push the live active tab's IBaseContext onto the focus
	// stack (dbsavvy-usj). nil helper → resolver returns nil → digit 6
	// is a silent no-op (e.g. pre-Connect, helper not yet wired).
	resolveResults := func() types.IBaseContext {
		if g.resultTabsH == nil {
			return nil
		}
		return g.resultTabsH.ActiveContext()
	}
	controllers.RegisterRailSwitchActions(g.cmdRegistry, g.tree, g.registry, resolveResults)

	// Cheatsheet popup: capture the focused scope, hand it to the
	// CheatsheetContext, then push the context onto the focus stack.
	// RunLayout's Tier-3 popup pass (layout.go) renders the popup on
	// the next layout frame; <esc> pops it back to the previous context.
	_ = g.cmdRegistry.Register(&commands.Command{
		ID:          commands.HelpCheatsheet,
		Description: "Show cheatsheet",
		Tag:         "Help",
		Handler: func(commands.ExecCtx) error {
			if g.registry == nil || g.registry.Cheatsheet == nil {
				return nil
			}
			scope := types.GLOBAL
			if top := g.tree.Current(); top != nil {
				scope = top.GetKey()
			}
			g.registry.Cheatsheet.SetScope(scope)
			return g.tree.Push(g.registry.Cheatsheet)
		},
	})

	// <esc> on the CHEATSHEET view pops it off the focus stack so the
	// user returns to the prior context (e.g. MENU or TABLES) intact.
	// Installed directly via the driver because CHEATSHEET is a
	// DISPLAY_CONTEXT and does not flow through the Matcher.
	_ = g.driver.SetKeybinding(string(types.CHEATSHEET), gocui.NewKeyName(gocui.KeyEsc), gocui.ModNone, func() error {
		return g.tree.Pop()
	})

	// dbsavvy-56u.2: TipDismiss handler. Pops the FIRST_RUN_TIP popup
	// and stamps StartupTipsSeenAt via AppStateStore.StampStartupTips.
	// The action is wired regardless of whether the tip is currently
	// visible — the popped Pop() error is logged at warn and the dismiss
	// proceeds (AC: "if StampStartupTips fails to persist, tip still
	// dismisses; error logged at warn"). The store's debounced save is
	// fire-and-forget; any persistence failure is captured by the store
	// itself via LastSaveErr + its own slog cat=state event.
	_ = g.cmdRegistry.Register(&commands.Command{
		ID:          commands.TipDismiss,
		Description: "Dismiss first-run tip",
		Handler: func(commands.ExecCtx) error {
			if g.deps.Store != nil {
				g.deps.Store.StampStartupTips()
			}
			if g.tree != nil {
				if err := g.tree.Pop(); err != nil && err != gui.ErrPopAtBottom {
					if g.deps.Common != nil {
						logs.Event(g.deps.Common.Logger(), "gui", "first_run_tip_pop_failed",
							slog.String("err", err.Error()),
						)
					}
				}
			}
			if g.deps.Common != nil {
				logs.Event(g.deps.Common.Logger(), "gui", "first_run_tip_dismissed")
			}
			return nil
		},
	})

	// <esc> / <cr> on the FIRST_RUN_TIP view dispatch TipDismiss directly
	// via the driver. FIRST_RUN_TIP carries no controller bindings (it's a
	// minimal welcome popup); the driver shim mirrors the CHEATSHEET <esc>
	// pattern above.
	dismissTip := func() error {
		cmd, ok := g.cmdRegistry.Get(commands.TipDismiss)
		if !ok || cmd == nil || cmd.Handler == nil {
			return nil
		}
		return cmd.Handler(commands.ExecCtx{})
	}
	_ = g.driver.SetKeybinding(string(types.FIRST_RUN_TIP), gocui.NewKeyName(gocui.KeyEsc), gocui.ModNone, dismissTip)
	_ = g.driver.SetKeybinding(string(types.FIRST_RUN_TIP), gocui.NewKeyName(gocui.KeyEnter), gocui.ModNone, dismissTip)

	// COMMAND_LINE action commands. The CommandLineContext doubles as
	// the holder (it implements types.IBaseContext + ReadAndClearBuffer).
	toaster := func(msg string) {
		if g.toastHelp != nil {
			g.toastHelp.Show(msg, 3*time.Second)
		}
	}
	caret := func(enabled bool) {
		if g.driver != nil {
			g.driver.SetCaretEnabled(enabled)
		}
	}
	cmdDeps := keys.CommandLineCommandDeps{
		Stack:        g.tree,
		Context:      g.registry.CommandLine,
		ExRegistry:   g.exRegistry,
		Toaster:      toaster,
		CaretToggler: caret,
	}
	_ = g.cmdRegistry.Register(keys.CommandOpenCommand(cmdDeps))
	_ = g.cmdRegistry.Register(keys.CommandCancelCommand(cmdDeps))
	_ = g.cmdRegistry.Register(keys.CommandSubmitCommand(cmdDeps))

	// kindOf classifies a ContextKey by walking the registry; used by
	// Build to expand `scope: all` and by :reload.
	kindOf := func(k types.ContextKey) types.ContextKind {
		for _, ctx := range g.registry.Flatten() {
			if ctx != nil && ctx.GetKey() == k {
				return ctx.GetKind()
			}
		}
		return types.GLOBAL_CONTEXT
	}

	// dbsavvy-56u.3: validate UserConfig now that cmdRegistry and the
	// context registry are populated. Deviation from AD-2 literal ordering
	// (validate-after-NewGui-before-RunAndHandleError) — registries are
	// built inside wireWithDriver, so validation moves here. AD-2's safety
	// rationale (deferred g.Close fires) is preserved: g.Close is idempotent
	// (gui.go:986-989) and entry_point.go's `defer func() { _ = g.Close() }()`
	// runs regardless of where the error originates.
	if cfg != nil {
		deps := config.ValidationDeps{
			ActionExists: func(id string) bool { return g.cmdRegistry.Has(id) },
			ScopeExists:  g.scopeExistsPredicate(),
		}
		cfgWarns, cfgErrs := config.ValidateUserConfig(cfg, deps)
		for _, w := range cfgWarns {
			fmt.Fprintf(os.Stderr, "config: warning: %s\n", w)
		}
		if len(cfgErrs) > 0 {
			for _, e := range cfgErrs {
				fmt.Fprintf(os.Stderr, "config: %s\n", e)
			}
			return fmt.Errorf("config: %d validation error(s)", len(cfgErrs))
		}
		g.deps.Common.Logger().Info("config: validated", "warnings", len(cfgWarns), "cat", "app")
	}

	// Build the trie.
	svc := keys.NewKeybindingService()
	defaults := controllers.AllDefaultBindings(g.controllers)
	trieSet, warnings, buildErr := svc.Build(defaults, cfg, g.cmdRegistry, kindOf)
	if buildErr != nil {
		return fmt.Errorf("gui: Build: %w", buildErr)
	}
	for _, w := range warnings {
		g.deps.Common.Logger().Warn(fmt.Sprintf("keybindings: [%s] %s (%s)", w.Code, w.Message, w.Origin))
	}
	g.lastWarnings = warnings
	matcher.SwapTrieSet(trieSet)

	// :reload ex-command. The LoadUserConfig closure is a minimal-viable
	// stub: it returns the currently-loaded config rather than re-reading
	// from disk. A real on-disk reload requires plumbing the bootstrap
	// path through Deps; that lands in a follow-up. The AC only asks
	// that :reload triggers exactly one matcher.SwapTrieSet — the stub
	// satisfies that contract.
	reloadDeps := keys.ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) {
			if c := g.deps.Common.Cfg(); c != nil {
				return c, nil
			}
			return config.GetDefaultConfig(), nil
		},
		Defaults: defaults,
		Registry: g.cmdRegistry,
		KindOf:   kindOf,
		Service:  svc,
		Matcher:  matcher,
		Toaster:  toaster,
		Log:      g.deps.Common.Logger(),
	}
	_ = g.exRegistry.Register(keys.ReloadCommand(reloadDeps))

	// :q / :quit — vim-style quit ex-commands. Return gocui.ErrQuit so
	// the submit dispatcher can propagate it up through the gocui main
	// loop. CommandSubmitCommand recognises ErrQuit specifically and
	// skips its default toast-and-swallow path.
	quitExHandler := func(_ []string, _ commands.ExecCtx) error {
		return gocui.ErrQuit
	}
	_ = g.exRegistry.Register(keys.ExCommand{Name: "q", Description: "Quit", Handler: quitExHandler})
	_ = g.exRegistry.Register(keys.ExCommand{Name: "quit", Description: "Quit", Handler: quitExHandler})

	// Master Editor on editable views (today only COMMAND_LINE) +
	// per-key SetKeybinding shims on every non-editable view.
	if err := g.installKeyDispatch(trieSet); err != nil {
		return err
	}

	// Mouse wiring is gated on cfg.UI.Mouse.Enabled.
	if cfg.UI.Mouse.Enabled {
		if err := ui.WireMouse(ui.MouseWiringDeps{
			Driver:      g.driver,
			Log:         g.deps.Common.Logger(),
			Tree:        g.tree,
			Registry:    g.registry,
			Matcher:     matcher,
			TableDouble: g.tablesHelp,
			TablePicker: tablePicker,
		}); err != nil {
			return fmt.Errorf("gui: wire mouse: %w", err)
		}
	}

	// Cancel any pending matcher partial / which-key on focus change.
	// SetCurrentView is plumbed inline by RunLayout (Tier 4 final step)
	// rather than via a swap hook, so it can't race the Layout pass's
	// SetViewOnTop loop.
	g.tree.RegisterSwapHook(matcher.Cancel)
	g.tree.RegisterSwapHook(g.whichkey.Hide)

	// Cancel the active result-tab stream when the user navigates out
	// of the QueryEditor / result-tab pane while a query is still
	// Running. dbsavvy-66p.17.
	installResultTabsSwapHook(g.tree, g.resultTabsH)

	// Seed the CONNECTIONS rail from the on-disk profiles before the
	// first render frame, so the rail is non-empty when its empty-state
	// hook reports renderEmpty=false.
	g.refreshConnectionsRail()

	// Push the initial CONNECTIONS context.
	if err := g.tree.Push(g.registry.Connections); err != nil {
		return err
	}

	// dbsavvy-56u.2: push the first-run welcome tip on top of CONNECTIONS
	// when the user has never dismissed it AND has no profiles. The
	// FIRST_RUN_TIP context is a PERSISTENT_POPUP so subsequent popup
	// pushes do not auto-evict it. The dismiss action (TipDismiss) pops
	// it and stamps StartupTipsSeenAt.
	if g.registry.FirstRunTip != nil &&
		data.ShouldShowFirstRunTip(g.deps.Store, g.deps.ConnectionsProvider) {
		if g.deps.Common != nil {
			logs.Event(g.deps.Common.Logger(), "gui", "first_run_tip_shown")
		}
		if err := g.tree.Push(g.registry.FirstRunTip); err != nil {
			return err
		}
	}
	return nil
}

// refreshConnectionsRail re-loads the connection profiles from
// Deps.ConnectionsProvider and pushes them into ConnectionsContext.items
// so the next render frame draws the rows. Safe to call from any
// goroutine — SideListContext.SetItems mutates an in-memory slice;
// view writes happen in the next Layout pass.
func (g *Gui) refreshConnectionsRail() {
	if g.registry == nil || g.registry.Connections == nil {
		return
	}
	provider := g.deps.ConnectionsProvider
	if provider == nil {
		g.registry.Connections.SetItems(nil)
		return
	}
	profiles := provider()
	items := make([]any, len(profiles))
	for i := range profiles {
		p := profiles[i]
		items[i] = &p
	}
	g.registry.Connections.SetItems(items)
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

	g.masterEditors = map[types.ContextKey]gocui.Editor{}

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
					g.masterEditors[key] = editor.NewVimEditor(g.registry.QueryEditor, g.matcher, key)
				}
			default:
				g.masterEditors[key] = NewMasterEditor(ngocui, g.matcher, key, WithSessionLog(g.deps.Common.Logger()))
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
	}

	// GLOBAL trie's root keys: install with empty viewname so they
	// fire regardless of which view holds focus. gocui treats viewname
	// == "" as a global binding.
	if err := g.installShimsForScope(trieSet, types.GLOBAL, ""); err != nil {
		return err
	}

	// RESULT_GRID master editor (dbsavvy-usj). The context is a
	// StubContext (no static view), so the Flatten loop above skipped
	// it; build the editor here so RunLayout's Tier-1.5 pass can attach
	// it to whichever dynamic result_tab_<slot> view is currently
	// active. SetMasterEditor is idempotent — reattach per frame is
	// cheap, and re-pushes between tabs do not strand a stale editor on
	// the prior view (gocui's per-view Editor pointer is replaced on
	// attach, and result_tab views never become editable text targets).
	g.masterEditors[types.RESULT_GRID] = NewMasterEditor(ngocui, g.matcher, types.RESULT_GRID, WithSessionLog(g.deps.Common.Logger()))
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
// Bug history (dbsavvy-tro.7): previously this loop only walked
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
			sk := shimKey{view: view, gk: gk, gmod: gmod}
			if _, dup := seen[sk]; dup {
				continue
			}
			seen[sk] = struct{}{}
			dispatchKey := k
			handler := func() error {
				_, err := g.matcher.Dispatch(scope, dispatchKey)
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
func (g *Gui) RunAndHandleError() error {
	if err := g.initGocui(); err != nil {
		return err
	}
	err := g.driver.MainLoop()
	if err == nil || err == gocui.ErrQuit {
		return nil
	}
	return err
}

// Close runs the M15c shutdown sequence (epic dbsavvy-8s2 AD-8 revised):
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
	// Drain any in-flight OnWorker goroutines before the store/driver
	// teardown so the goleak smoke tests see a quiescent goroutine pool
	// (DESIGN.md §17). Safe to call when no workers were ever spawned —
	// sync.WaitGroup.Wait on a zero counter returns immediately.
	g.workersWG.Wait()
	var firstErr error
	// Close the active SQLSession FIRST so an in-flight Stream gets
	// cancelled (SQLSession.Close cancels the live RunHandle and waits
	// briefly for it to terminate) before the history writer drains.
	// Without this ordering a finishing run could push one more
	// historyEntry into a channel whose receiver has already exited.
	if g.activeSQLSession != nil {
		if err := g.activeSQLSession.Close(); err != nil {
			firstErr = err
		}
		g.activeSQLSession = nil
	}
	// Unbind so any controller that still holds the runner sees
	// HasSession() == false. Also resets the runner's `last` handle so
	// Cancel after Close is a silent no-op.
	if g.queryRunner != nil {
		g.queryRunner.Unbind()
	}
	if g.history != nil {
		if err := g.history.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		g.history = nil
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

// saveQueryEditorBuffer is the SaveBuffer closure bound into ContextTreeDeps
// for dbsavvy-wwd.9. The MainLoop caller (QueryEditorContext.HandleFocusLost)
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

// hiddenSchemasForActiveConn returns AppState.HiddenSchemas[activeConnID]
// for SchemasContext.renderRows (dbsavvy-56u.4). Nil / empty Common,
// AppState, or active connection ID collapse to a nil slice so the
// context applies no runtime filter — matching the test-wiring contract.
func (g *Gui) hiddenSchemasForActiveConn() []string {
	if g == nil || g.deps.Common == nil {
		return nil
	}
	state := g.deps.Common.AppState
	if state == nil {
		return nil
	}
	connID := g.activeConnID
	if connID == "" {
		return nil
	}
	return state.HiddenSchemas[connID]
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
	return controllers.HelperBag{
		Connect:      &connectInvoker{g: g, helper: g.connectHelper, runner: g.queryRunner, history: g.history},
		QueryRunner:  g.queryRunner,
		EditorBuffer: newEditorBufferAdapter(qec),
	}
}

// ActiveSQLSessionForTest returns the SQLSession the most recent Connect
// installed, or nil. Test-only.
func (g *Gui) ActiveSQLSessionForTest() *session.SQLSession { return g.activeSQLSession }

// ChoiceHelperForTest returns the ChoiceHelper wired by wireWithDriver,
// or nil before that pass ran. Test accessor used by m47.4 wiring tests
// to confirm the ChainedPrompter adapter has a real picker behind it.
func (g *Gui) ChoiceHelperForTest() *ui.ChoiceHelper { return g.choiceHelp }

// PromptHelperForTest returns the PromptHelper wired by wireWithDriver,
// or nil before that pass ran. Test accessor used by m47.6 end-to-end
// integration tests to quiesce on Active() between popup steps.
func (g *Gui) PromptHelperForTest() *ui.PromptHelper { return g.promptHelp }

// SeedPromptBufferForTest writes s into the PROMPT context's test-mode
// buffer. Post-dbsavvy-fq9 the PROMPT view is editable: in production
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
func (g *Gui) CommandRegistry() *commands.Registry { return g.cmdRegistry }

// ExRegistry returns the ex-command registry. Test accessor.
func (g *Gui) ExRegistry() *keys.ExRegistry { return g.exRegistry }

// Matcher returns the active Matcher. Test accessor.
func (g *Gui) Matcher() *keys.Matcher { return g.matcher }

// WhichKey returns the WhichKey notifier. Test accessor — dlp.14 reads
// Visible() to assert the popup mechanic.
func (g *Gui) WhichKey() *keys.WhichKey { return g.whichkey }

// ModeStore returns the ModeStore. Test accessor — dlp.14 toggles modes
// to exercise the mode-conditional dispatch paths.
func (g *Gui) ModeStore() *keys.ModeStore { return g.modeStore }

// Warnings returns the Warning slice captured during the most recent
// wireWithDriver Build pass. Test accessor used by the dlp.14 smoke
// walkthrough to assert ambient warnings.
func (g *Gui) Warnings() []keys.Warning { return g.lastWarnings }

// ToastHelper returns the toast helper. Test accessor — dlp.14 reads
// History() to assert reload / toast emissions.
func (g *Gui) ToastHelper() *ui.ToastHelper { return g.toastHelp }

// ResultTabsHelper returns the live result-tabs helper, or nil before
// wireWithDriver runs. Test accessor — dbsavvy-66p.12 smoke walks
// through Open/Pin/eviction via this surface.
func (g *Gui) ResultTabsHelper() *ui.ResultTabsHelper { return g.resultTabsH }

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
