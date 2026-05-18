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
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/cheatsheet"
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
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
	toastHelp   *ui.ToastHelper
	tablesHelp  *ui.TablesHelper
	tipHelp     *ui.TipHelper

	// Keybinding system (built by wireWithDriver).
	cmdRegistry *commands.Registry
	matcher     *keys.Matcher
	modeStore   *keys.ModeStore
	whichkey    *keys.WhichKey
	exRegistry  *keys.ExRegistry

	// lastWarnings captures the Warning slice returned by the most recent
	// KeybindingService.Build run during wireWithDriver. Surfaced via the
	// Warnings() accessor for the dlp.14 integration smoke test.
	lastWarnings []keys.Warning

	// commandLineEditor is the master gocui.Editor instance bound to the
	// COMMAND_LINE view. Built by installKeyDispatch and (re-)attached by
	// RunLayout's Tier-3 popup pass each time COMMAND_LINE appears on the
	// focus stack: a fresh Push creates a new gocui view, so the editor
	// must be reattached to that view-instance.
	commandLineEditor gocui.Editor

	// Test overrides for Matcher timing; nil means use cfg + defaults.
	delayOverrides *keyDelayOverrides

	// Connection state surfaced by the activeConnAdapter.
	activeConnID string

	// closed is true once Close has run; idempotent guard.
	closed bool
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
		Log:           g.deps.Common.Log,
	})
	if err != nil {
		return fmt.Errorf("gui: NewMatcher: %w", err)
	}
	g.matcher = matcher
	runtime := keys.NewRuntime(g.cmdRegistry, matcher, g.modeStore, g.whichkey, g.exRegistry)

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

	// Build the context registry with hooks closed over the driver.
	ctxDeps := types.ContextTreeDeps{
		GuiDriver:            g.driver,
		EmptyStateHook:       data.NewEmptyStateHook(tr, provider),
		PresentationHook:     presentation.NewPresentationHook(),
		PerRowDecorationHook: presentation.NewPerRowDecorationHook(),
		LimitText:            presentation.NewLimitText(tr),
		ModeStore:            g.modeStore,
		WhichKey:             g.whichkey,
		CheatsheetRender:     cheatsheetRender,
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
		Refresh:          g.refreshHelper,
		Tip:              g.tipHelp,
		TableDouble:      g.tablesHelp,
		Menu:             &menuPushHelper{tree: g.tree, menu: g.registry.Menu},
		HiddenPatterns:   defaultHiddenPatterns,
		KbRuntime:        runtime,
	}
	g.controllers = controllers.AttachControllers(g.registry, g.deps.Common, helperBag)

	// Register every controller's action handlers with the registry.
	g.controllers.RegisterActions(g.cmdRegistry)

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

	// Build the trie.
	svc := keys.NewKeybindingService()
	defaults := controllers.AllDefaultBindings(g.controllers)
	trieSet, warnings, buildErr := svc.Build(defaults, cfg, g.cmdRegistry, kindOf)
	if buildErr != nil {
		return fmt.Errorf("gui: Build: %w", buildErr)
	}
	if g.deps.Common.Log != nil {
		for _, w := range warnings {
			g.deps.Common.Log.Warnf("keybindings: [%s] %s (%s)", w.Code, w.Message, w.Origin)
		}
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
		Log:      g.deps.Common.Log,
	}
	_ = g.exRegistry.Register(keys.ReloadCommand(reloadDeps))

	// Master Editor on editable views (today only COMMAND_LINE) +
	// per-key SetKeybinding shims on every non-editable view.
	if err := g.installKeyDispatch(trieSet); err != nil {
		return err
	}

	// Mouse wiring is gated on cfg.UI.Mouse.Enabled.
	if cfg.UI.Mouse.Enabled {
		if err := ui.WireMouse(ui.MouseWiringDeps{
			Driver:      g.driver,
			Log:         g.deps.Common.Log,
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

	// Push the initial CONNECTIONS context.
	return g.tree.Push(g.registry.Connections)
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
			// Stash the master Editor on the Gui. RunLayout's Tier-3 popup
			// pass attaches it to the COMMAND_LINE view-instance every
			// frame the context is on the focus stack — re-Push creates a
			// fresh view, and gocui's SetMasterEditor is idempotent.
			g.commandLineEditor = NewMasterEditor(ngocui, g.matcher, key)
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
	return nil
}

// installShimsForScope walks every (mode, scope) trie and registers one
// SetKeybinding per top-level Key. The handler routes the key through
// matcher.Dispatch under the supplied scope. Duplicate (view, key, mod)
// registrations are tolerated — gocui returns nil and the second
// handler shadows the first; our Matcher dispatches by scope so the
// shadowing handler still hits the right binding.
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
		for _, k := range trie.RootKeys() {
			gk, gmod, err := keys.ChordKeyToGocui(k)
			if err != nil {
				continue
			}
			sk := shimKey{view: view, gk: gk, gmod: gmod}
			if _, dup := seen[sk]; dup {
				continue
			}
			seen[sk] = struct{}{}
			rootKey := k
			handler := func() error {
				_, err := g.matcher.Dispatch(scope, rootKey)
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

// Close runs the M15c shutdown sequence: Flush → Close store → Close
// driver. Idempotent.
func (g *Gui) Close() error {
	if g.closed {
		return nil
	}
	g.closed = true
	var firstErr error
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
	return firstErr
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
