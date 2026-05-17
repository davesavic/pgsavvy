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
	"fmt"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
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
}

// Gui is the dbsavvy TUI orchestrator. NewGui builds the driver-free
// pieces (focus stack, OneshotArm, data helpers). The driver-dependent
// pieces (context registry, ui helpers, controllers, bindings) are
// built lazily by wireWithDriver, called from either initGocui (real
// production wiring) or UseDriverForTest (test-only seam).
type Gui struct {
	deps   Deps
	driver types.GuiDriver

	// Focus stack and one-shot arm; driver-free.
	tree *gui.ContextTree
	arm  *keys.OneshotArm

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
	toastHelp   *ui.ToastHelper
	tablesHelp  *ui.TablesHelper
	tipHelp     *ui.TipHelper

	// Connection state surfaced by the activeConnAdapter.
	activeConnID string

	// closed is true once Close has run; idempotent guard.
	closed bool
}

// NewGui builds every collaborator that doesn't depend on the live
// GuiDriver. The driver-dependent wiring (context registry, UI helpers,
// controllers, key/mouse bindings) waits for either initGocui (prod)
// or UseDriverForTest (test).
func NewGui(deps Deps) *Gui {
	g := &Gui{
		deps: deps,
		tree: gui.NewContextTree(),
		arm:  keys.NewOneshotArm(0),
	}
	g.connectHelper = data.NewConnectHelper()
	g.schemasHelper = data.NewSchemasHelper(deps.Common, deps.Store)
	g.formHelper = data.NewConnectionFormHelper(deps.Common, fsFromCommon(deps.Common), deps.ConnectionsPath, deps.DriverNamesFn)
	g.refreshHelper = data.NewRefreshHelper(g.connectHelper)
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

// wireWithDriver builds the context registry, UI helpers that need the
// driver, controllers, registers every keyboard binding, optionally
// wires mouse bindings, registers the swap-hook arm-cancel and pushes
// the initial CONNECTIONS context.
func (g *Gui) wireWithDriver() error {
	if g.driver == nil {
		return fmt.Errorf("gui: wireWithDriver: nil driver")
	}

	tr := g.deps.Common.Tr
	provider := g.deps.ConnectionsProvider
	if provider == nil {
		provider = func() []models.Connection { return nil }
	}

	// Build the registry with hooks closed over the driver.
	ctxDeps := types.ContextTreeDeps{
		GuiDriver:            g.driver,
		EmptyStateHook:       data.NewEmptyStateHook(tr, provider),
		PresentationHook:     presentation.NewPresentationHook(),
		PerRowDecorationHook: presentation.NewPerRowDecorationHook(),
		LimitText:            presentation.NewLimitText(tr),
	}
	g.registry = guicontext.NewContextTree(ctxDeps)

	// UI helpers that need the driver / registry.
	g.confirmHelp = ui.NewConfirmHelper(g.tree, g.registry.Confirmation)
	g.promptHelp = ui.NewPromptHelper(g.tree, g.registry.Prompt)
	g.toastHelp = ui.NewToastHelper(g.driver)
	g.tablesHelp = ui.NewTablesHelper(g.toastHelp, tr)
	g.tipHelp = ui.NewTipHelper(g.tree, g.deps.Store)

	tablePicker := tablesPickerAdapter{registry: g.registry.Tables}

	helperBag := controllers.HelperBag{
		Driver:           g.driver,
		Logger:           g.deps.Common.Log,
		Connections:      connectionsPickerAdapter{registry: g.registry.Connections},
		Schemas:          schemasPickerAdapter{registry: g.registry.Schemas},
		Tables:           tablePicker,
		ActiveConnection: &activeConnAdapter{g: g},
		Connect:          &connectInvoker{g: g, helper: g.connectHelper},
		SchemasHelper:    g.schemasHelper,
		ConnectionForm:   &connectionFormInvoker{g: g, helper: g.formHelper, prompter: stubPrompter{}},
		Confirm:          g.confirmHelp,
		Prompt:           g.promptHelp,
		Toast:            g.toastHelp,
		OneShot:          g.arm,
		Refresh:          g.refreshHelper,
		Tip:              g.tipHelp,
		TableDouble:      g.tablesHelp,
		Menu:             &menuPushHelper{tree: g.tree, menu: g.registry.Menu},
		HiddenPatterns:   defaultHiddenPatterns,
		ProvideLeader:    g.provideLeader,
	}
	g.controllers = controllers.AttachControllers(g.registry, g.deps.Common, helperBag)

	// Register every controller-published binding via keys.Register.
	for _, ctx := range g.registry.Flatten() {
		for _, kb := range ctx.GetKeybindings(types.KeybindingsOpts{}) {
			if err := keys.Register(g.driver, g.deps.Common.Log, kb.ViewName, kb.Key, kb.Mod, kb.Handler, kb.Description); err != nil {
				return fmt.Errorf("gui: register %q on %q: %w", kb.Description, kb.ViewName, err)
			}
		}
	}

	// Mouse wiring is gated on cfg.UI.Mouse.Enabled.
	if cfg := g.deps.Common.Cfg(); cfg != nil && cfg.UI.Mouse.Enabled {
		if err := ui.WireMouse(ui.MouseWiringDeps{
			Driver:      g.driver,
			Log:         g.deps.Common.Log,
			Tree:        g.tree,
			Registry:    g.registry,
			OneShot:     g.arm,
			TableDouble: g.tablesHelp,
			TablePicker: tablePicker,
		}); err != nil {
			return fmt.Errorf("gui: wire mouse: %w", err)
		}
	}

	// Cancel any pending one-shot arm whenever the focus stack changes.
	g.tree.RegisterSwapHook(g.arm.Cancel)

	// Push the initial CONNECTIONS context.
	return g.tree.Push(g.registry.Connections)
}

// RunAndHandleError is the production entry. It builds the driver,
// installs the manager, and runs the gocui main loop. gocui.ErrQuit
// from MainLoop is the normal shutdown path and collapses to nil.
func (g *Gui) RunAndHandleError() error {
	if err := g.initGocui(); err != nil {
		return err
	}
	g.driver.SetManager(g)
	err := g.driver.MainLoop()
	if err == nil || err == gocui.ErrQuit {
		return nil
	}
	return err
}

// Close runs the M15c shutdown sequence: Flush → Close store → Close
// driver. Idempotent: subsequent calls are no-ops.
func (g *Gui) Close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	var firstErr error
	if g.deps.Store != nil {
		if err := g.deps.Store.Flush(); err != nil && firstErr == nil {
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
	return firstErr
}

// QuitOnSignal asks the gocui MainLoop to exit cleanly by enqueueing a
// gocui.ErrQuit-returning closure on the Update queue. Safe to call
// from a non-MainLoop goroutine (e.g. signal handler).
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

// provideLeader resolves the leader prefix from the live config.
func (g *Gui) provideLeader() string {
	if cfg := g.deps.Common.Cfg(); cfg != nil && cfg.Leader != "" {
		return cfg.Leader
	}
	return "<space>"
}

// defaultHiddenPatterns is the SchemasInvoker.UnhideSchema input — the
// pg-driver builtin schemas are the only patterns this epic recognises.
// Profile-level patterns land with the connection-profile expansion in
// a later epic.
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
