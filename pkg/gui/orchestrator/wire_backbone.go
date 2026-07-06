package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/cheatsheet"
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui"
	"github.com/davesavic/pgsavvy/pkg/gui/clipboard"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/presentation"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// wireKeybindingSystem builds the keybinding-system collaborators: the command
// registry, mode store, which-key, ex-registry, the matcher, and the keybinding
// runtime. Extracted verbatim from wireWithDriver.
func (g *Gui) wireKeybindingSystem(cfg *config.UserConfig) error {
	// Build the keybinding-system collaborators.
	g.keybindingSystem.cmdRegistry = commands.NewRegistry()
	g.keybindingSystem.modeStore = keys.NewModeStore()
	g.keybindingSystem.whichkey = keys.NewWhichKey()
	g.keybindingSystem.exRegistry = keys.NewExRegistry()

	// wire the per-session logger into the input-side
	// stores so mode_set / mode_reset / ctx_* events flow through
	// logs.Event. nil-safe — logs.Event short-circuits on nil.
	g.keybindingSystem.modeStore.SetSessionLog(g.deps.Common.Logger())
	if g.tree != nil {
		g.tree.SetSessionLog(g.deps.Common.Logger())
	}

	leader, _ := leaderRunesFromCfg(cfg)
	tlen, ttlen, wdelay := resolveKeyDelays(cfg, g.keybindingSystem.delayOverrides)
	matcher, err := keys.NewMatcher(nil, keys.MatcherConfig{
		Modes:         g.keybindingSystem.modeStore,
		Leader:        leader,
		TimeoutLen:    tlen,
		TtimeoutLen:   ttlen,
		WhichKeyDelay: wdelay,
		Registers:     keys.NewRegisterStore(),
		WhichKey:      g.keybindingSystem.whichkey,
		Log:           g.deps.Common.Logger(),
		// Surface swallowed handler errors and disabled-binding reasons
		// as toasts. Late-bound: g.toastHelp is constructed later in this
		// method, but by the time any key dispatches it is non-nil
		// (without this, handler errors only hit the debug
		// log and apply/commit failures look like silent no-ops).
		Toaster: func(msg string) {
			if g.toastHelp != nil {
				g.toastHelp.Show(msg, matcherToastTTL)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("gui: NewMatcher: %w", err)
	}
	g.keybindingSystem.matcher = matcher
	// wire the per-session logger into the matcher so
	// chord_resolved events flow through logs.Event. nil-safe.
	g.keybindingSystem.matcher.SetSessionLog(g.deps.Common.Logger())
	runtime := keys.NewRuntime(g.keybindingSystem.cmdRegistry, matcher, g.keybindingSystem.modeStore, g.keybindingSystem.whichkey, g.keybindingSystem.exRegistry)
	g.keybindingSystem.kbRuntime = runtime
	return nil
}

// buildCheatsheetCategoryTabs builds the per-Category tab specs + DisplayLeafContext
// leaves the CHEATSHEET container renders for the given focused scope. It reads the
// live TrieSet, generates the scope-filtered output, categorizes it, and renders one
// pre-formatted body per Category (the categorize/render lives here so pkg/gui/context
// stays free of pkg/cheatsheet). Every leaf carries the SAME GuiDriver + CHEATSHEET
// view name as the container so its SetContent targets the shared view.
//
// NEVER returns zero specs: when the matcher hasn't published a TrieSet yet, it
// falls back to a single General tab carrying the CheatsheetEmpty body, preserving
// the always-on-General invariant.
// cheatsheetScope resolves the scope the cheatsheet should be generated for
// from the focused context. A tabbed-rail container (QUERY_RAIL, SCHEMA_RAIL)
// sits on the focus stack as the Current() context, but its editable leaf's
// bindings — including the editor/visual-mode bindings — live under the ACTIVE
// leaf's distinct scope (many-contexts-ONE-view topology). Descending to the
// active leaf makes the cheatsheet reflect what would actually fire (e.g.
// QUERY_EDITOR's Visual mode), mirroring dispatch which keys off the leaf
// scope. A nil top (empty focus stack) collapses to GLOBAL; an empty
// ActiveLeafKey (rail with no tabs yet) falls back to the container key.
func cheatsheetScope(top types.IBaseContext) types.ContextKey {
	if top == nil {
		return types.GLOBAL
	}
	scope := top.GetKey()
	if rail, ok := top.(interface {
		ActiveLeafKey() types.ContextKey
	}); ok {
		if leaf := rail.ActiveLeafKey(); leaf != "" {
			return leaf
		}
	}
	return scope
}

func (g *Gui) buildCheatsheetCategoryTabs(scope types.ContextKey) ([]guicontext.TabSpec, []types.IBaseContext) {
	tr := g.deps.Common.Tr
	viewName := g.registry.Cheatsheet.GetViewName()
	leafDeps := guicontext.Deps{GuiDriver: g.driver}
	newLeaf := func(body string) *guicontext.DisplayLeafContext {
		leafBase := guicontext.NewBaseContext(guicontext.BaseContextOpts{
			Key:      types.CHEATSHEET,
			ViewName: viewName,
			Kind:     types.DISPLAY_CONTEXT,
		})
		return guicontext.NewDisplayLeafContext(leafBase, leafDeps, viewName, body)
	}

	var ts *keys.TrieSet
	if g.keybindingSystem.matcher != nil {
		ts = g.keybindingSystem.matcher.TrieSet()
	}
	if ts == nil {
		// No live trie: a single always-on General tab with the empty body.
		general := cheatsheet.CategoryView{Category: cheatsheet.CategoryGeneral}
		spec := guicontext.TabSpec{
			Label:   cheatsheet.LabelFor(cheatsheet.CategoryGeneral, tr),
			LeafKey: types.ContextKey(string(cheatsheet.CategoryGeneral)),
		}
		return []guicontext.TabSpec{spec}, []types.IBaseContext{newLeaf(cheatsheet.RenderCategory(general, tr))}
	}

	out := cheatsheet.Generate(cheatsheet.GenerateInput{Trie: ts, Scope: scope, Tr: tr})
	cats := cheatsheet.Categorize(out)
	specs := make([]guicontext.TabSpec, 0, len(cats))
	leaves := make([]types.IBaseContext, 0, len(cats))
	for _, cv := range cats {
		// The category name is a NON-DB constant identifier — safe as a LeafKey.
		specs = append(specs, guicontext.TabSpec{
			Label:   cheatsheet.LabelFor(cv.Category, tr),
			LeafKey: types.ContextKey(string(cv.Category)),
		})
		leaves = append(leaves, newLeaf(cheatsheet.RenderCategory(cv, tr)))
	}
	return specs, leaves
}

// whichKeyRows resolves the immediate children of the current (scope, prefix),
// merged across the focused scope and GLOBAL. Returns nil when the
// matcher hasn't published a TrieSet yet or when prefix doesn't resolve in either
// the (mode, scope) or (mode, GLOBAL) trie — the context's HandleRender treats
// nil rows as a silent no-op (see whichkey_context.go:73-76). Extracted from the
// closure in wireWithDriver.
func (g *Gui) whichKeyRows(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow {
	if g.keybindingSystem.matcher == nil || g.keybindingSystem.modeStore == nil {
		return nil
	}
	ts := g.keybindingSystem.matcher.TrieSet()
	if ts == nil {
		return nil
	}
	mode := g.keybindingSystem.modeStore.Get(scope)
	// union the focused scope's continuations with GLOBAL's,
	// mirroring Dispatch's scope→GLOBAL fall-through, so the popup lists
	// every key that would actually fire (e.g. global <leader>1..9 while
	// focused on RESULT_GRID), not just the scope-specific ones.
	rows, ok := ts.ChildrenAtMerged(mode, scope, prefix)
	if !ok {
		return nil
	}
	return rows
}

// rowEndpoint derives the "host/db" suffix shown on a connection-picker row.
// It prefers the parsed DSN endpoint and falls back to the discrete Host/
// Database fields, so a discrete-only connection (no raw DSN) still shows an
// endpoint instead of a blank row. Returns "" when neither yields a host or
// database. SECURITY: only host + database are surfaced (never the raw DSN /
// password); each leaf is run through config.SafeText.
func rowEndpoint(c *models.Connection) string {
	if c == nil {
		return ""
	}
	host, db := session.ParseDSNEndpoint(c.DSN)
	if host == "" && db == "" {
		host, db = c.Host, c.Database
	}
	if host == "" && db == "" {
		return ""
	}
	return config.SafeText(host) + "/" + config.SafeText(db)
}

// wireContextRegistry builds the context registry with hooks closed over the
// driver. Extracted verbatim from wireWithDriver; must run
// after wireKeybindingSystem (reads g.keybindingSystem.matcher / g.keybindingSystem.modeStore / g.keybindingSystem.whichkey).
func (g *Gui) wireContextRegistry(tr *i18n.TranslationSet, provider func() []models.Connection) {
	// Build the context registry with hooks closed over the driver.
	ctxDeps := types.ContextTreeDeps{
		GuiDriver:        g.driver,
		EmptyStateHook:   data.NewEmptyStateHook(tr, provider),
		RailEmptyText:    railEmptyText(tr),
		PresentationHook: presentation.NewPresentationHook(),
		// live active-connection marker. The accessor is
		// read on every render so the "●" marker tracks connect/disconnect.
		PerRowDecorationHook: presentation.NewPerRowDecorationHook(func() string { return g.connectionState.activeConnID }),
		// enrich each picker row with the parsed host/db
		// endpoint. SECURITY: only the discrete host + database fields are
		// surfaced (never the raw DSN / password); each leaf is run through
		// config.SafeText. Malformed/empty DSN → "" → name-only row.
		RowSuffix:    rowEndpoint,
		LimitText:    presentation.NewLimitText(tr),
		ModeStore:    g.keybindingSystem.modeStore,
		WhichKey:     g.keybindingSystem.whichkey,
		WhichKeyRows: g.whichKeyRows,
		// QueryEditorContext.HandleFocusLost calls
		// matcher.Cancel via this minimal interface to keep
		// pkg/gui/context decoupled from pkg/gui/keys.
		Matcher: g.keybindingSystem.matcher,
		// buffer-save dispatch closure. The MainLoop
		// caller already supplies a string snapshot (Buffer.String
		// takes RLock); the worker just writes raw `.sql` text to disk.
		// Common.Fs / Common.StateDir may be nil/empty in test wiring —
		// the closure short-circuits via SaveBufferLines' empty-path
		// guard so this stays safe for fixtures.
		SaveBuffer: g.saveQueryEditorBuffer,
		// runtime-hidden lookup for SchemasContext.
		// renderRows uses this to skip AppState.HiddenSchemas[connID]
		// entries unless showHiddenMode is on. Closure captures the live
		// AppState pointer and the activeConnID; both can be empty in
		// test wiring → empty slice, no filtering applied.
		HiddenSchemasForActiveConn: g.hiddenSchemasForActiveConn,
		// dims schema/table/column/index rails when the session is
		// connection-dead. The closure reads the queryRunner's live state
		// on every render so the transition is visible immediately.
		IsDisconnected: func() bool {
			return g.queryState.queryRunner != nil && g.queryState.queryRunner.IsDisconnected()
		},
		// T3 AD5/AD6a: live spinner frame for the CONNECTION_MANAGER modal's
		// Active connect-stage row, so "⠙ Loading objects…" animates in
		// lock-step with the status-bar spinner.
		SpinnerFrame: g.SpinnerFrame,
		// first-run welcome tip copy. Nil-safe when tr is
		// absent (test fixtures) — the context renders nothing.
		FirstRunTipText: func() (string, string) {
			if tr == nil {
				return "", ""
			}
			return tr.FirstRunTipTitle, tr.FirstRunTipBody
		},
	}
	g.registry = guicontext.NewContextTree(ctxDeps)
	// Wire the session logger into the QUERY_RAIL container so its
	// tab_switch event is emitted (ContextTreeDeps carries no logger; the
	// container exposes a dedicated SetLogger seam mirroring its test
	// wiring). Nil-safe: SetLogger / logs.Event guard a nil logger.
	if g.registry.QueryRail != nil && g.deps.Common != nil {
		g.registry.QueryRail.SetLogger(g.deps.Common.Logger())
	}
	// Wire the session logger into the CHEATSHEET container too so its
	// tab_switch event is emitted (mirrors the QUERY_RAIL wiring above).
	if g.registry.Cheatsheet != nil && g.deps.Common != nil {
		g.registry.Cheatsheet.SetLogger(g.deps.Common.Logger())
	}
	// Wire the session logger into the FK_REVERSE_PICKER container too so its
	// tab_switch event is emitted. The ordinal leafKeys (fk_reverse_<i>) keep
	// the tab_switch log identifier-free.
	if g.registry.FKReversePicker != nil && g.deps.Common != nil {
		g.registry.FKReversePicker.SetLogger(g.deps.Common.Logger())
	}
}

// wireUIHelpers builds the UI helpers that need the driver / registry.
// Extracted verbatim from wireWithDriver.
func (g *Gui) wireUIHelpers(tr *i18n.TranslationSet) {
	// UI helpers that need the driver / registry.
	g.confirmHelp = ui.NewConfirmHelper(g.tree, g.registry.Confirmation)
	g.promptHelp = ui.NewPromptHelper(g.tree, g.registry.Prompt)
	g.searchLineHelp = ui.NewSearchLineHelper(g.tree, g.registry.SearchLine)
	g.choiceHelp = ui.NewChoiceHelper(g.tree, g.registry.Selection)

	// Masked prompts: now that the prompt popup (g.promptHelp), its masker
	// (g.registry.Prompt), and the UI scheduler (g.OnUIThread) are all live,
	// build ONE TUI SecretPromptHelper and hand it to the pg driver via the
	// app-provided hooks. The SSH passphrase prompt and the final
	// database-credential prompt share this single instance (the latter via
	// passwordPromptAdapter), so both surface through the same masked popup.
	if g.deps.SetSecretPrompter != nil || g.deps.SetPasswordPrompter != nil {
		secretPrompter := ui.NewSecretPromptHelper(g.promptHelp, g.registry.Prompt, g.OnUIThread)
		if g.deps.Common != nil {
			secretPrompter.SetLogger(g.deps.Common.Logger())
		}
		if g.deps.SetSecretPrompter != nil {
			g.deps.SetSecretPrompter(secretPrompter)
		}
		if g.deps.SetPasswordPrompter != nil {
			g.deps.SetPasswordPrompter(passwordPromptAdapter{h: secretPrompter})
		}
	}
	g.toastHelp = ui.NewToastHelper(g.driver)
	if g.deps.Common != nil {
		g.toastHelp.SetLogger(g.deps.Common.Logger())
	}
	g.tablesHelp = ui.NewTablesHelper(g.toastHelp, tr)
	g.tipHelp = ui.NewTipHelper(g.tree, g.deps.Store)
}

// wireRefreshHelperDeps wires the RefreshHelper closures over the live
// populateXxxRail helpers + refreshConnectionsRail. Extracted verbatim from
// wireWithDriver.
func (g *Gui) wireRefreshHelperDeps(connectInv *connectInvoker) {
	// wire the RefreshHelper closures over the live
	// populateXxxRail helpers + refreshConnectionsRail. Each closure
	// reloads driver data AND pushes it through the rail context's
	// SetItems. RefreshTables/Columns/Indexes apply a stale-guard
	// against the rail's currently-selected schema/table identifier:
	// if the user navigated away while Load was in flight, the load
	// result is discarded so a stale list never overwrites the new
	// focus's rail.
	g.refreshHelper.SetSchemasRefresher(func(ctx context.Context) error {
		// a manual schemas-rail refresh is the user's signal
		// that on-disk schema/table shape may have changed, so drop the FK
		// metadata cache; B5/B6 navigation will repopulate on demand.
		if g.queryState.activeSQLSession != nil {
			if fkc := g.queryState.activeSQLSession.FKCache(); fkc != nil {
				fkc.InvalidateAll()
			}
		}
		connectInv.populateSchemasRail(ctx)
		return nil
	})
	g.refreshHelper.SetTablesRefresher(func(ctx context.Context, schema string) error {
		if g.registry != nil && g.registry.Schemas != nil {
			cur := schemasPickerAdapter{registry: g.registry.Schemas}.SelectedSchemaName()
			if cur != "" && cur != schema {
				return nil
			}
		}
		connectInv.populateTablesRail(ctx, schema)
		return nil
	})
	g.refreshHelper.SetColumnsRefresher(func(ctx context.Context, schema, table string) error {
		if g.registry != nil && g.registry.Tables != nil {
			t := tablesPickerAdapter{registry: g.registry.Tables}.SelectedTable()
			if t != nil && (t.Schema != schema || t.Name != table) {
				return nil
			}
		}
		// a manual 'r' on the COLUMNS rail is the user's
		// signal that the selected table's shape may have changed externally.
		// Drop its warmed lazy (column+FK) entry and re-warm the schema's eager
		// tier so completion serves fresh metadata. InvalidateTable + LoadEager
		// are idempotent and run on this worker goroutine.
		if g.schemaWarmer != nil && schema != "" {
			if table != "" {
				g.schemaWarmer.InvalidateTable(schema, table)
			}
			g.schemaWarmer.LoadEager(schema)
		}
		connectInv.populateColumnsRail(ctx, schema, table)
		return nil
	})
	g.refreshHelper.SetIndexesRefresher(func(ctx context.Context, schema, table string) error {
		if g.registry != nil && g.registry.Tables != nil {
			t := tablesPickerAdapter{registry: g.registry.Tables}.SelectedTable()
			if t != nil && (t.Schema != schema || t.Name != table) {
				return nil
			}
		}
		connectInv.populateIndexesRail(ctx, schema, table)
		return nil
	})
}

// wireNavDeps builds the NavDeps bundle. Connect is the required (compile-time)
// parameter; the optional pickers/closures are set on the returned struct.
// Extracted verbatim from wireWithDriver.
func (g *Gui) wireNavDeps(connectInv *connectInvoker, tablePicker tablesPickerAdapter) controllers.NavDeps {
	// NavDeps — Connect is the required (compile-time) parameter; the
	// optional pickers/closures are set on the returned struct.
	nav := controllers.NewNavDeps(connectInv)
	nav.Schemas = schemasPickerAdapter{registry: g.registry.Schemas}
	nav.Tables = tablePicker
	nav.ActiveConnection = &activeConnAdapter{g: g}
	nav.SchemasHelper = g.schemasHelper
	nav.ConnectionForm = &connectionFormInvoker{g: g, helper: g.formHelper, prompter: newChainedPrompterAdapter(g.promptHelp, g.choiceHelp, g.OnUIThread)}
	nav.Refresh = g.refreshHelper
	nav.HiddenPatterns = defaultHiddenPatterns
	// reconnect invoker + pick-connection callback.
	reconnInv := &reconnectInvoker{helper: g.connectHelper, inv: connectInv}
	nav.Reconnector = reconnInv
	nav.OnPickConnection = func() error {
		if g.tree == nil || g.registry == nil || g.registry.ConnectionManager == nil {
			return nil
		}
		return g.tree.Push(g.registry.ConnectionManager)
	}
	// Esc in list mode pops the modal when mid-session (stack
	// depth > 1) and is a no-op at startup root (depth <= 1).
	nav.OnCloseConnectionManager = func() {
		if g.tree == nil {
			return
		}
		if len(g.tree.Stack()) <= 1 {
			return // startup root: Esc is a no-op
		}
		// the modal (MAIN_CONTEXT) covered whatever main pane
		// was active when it opened. Restore it on close so focus returns
		// where the user was — the query editor (or a result tab). nil when
		// the user was on a side rail, leaving focus there.
		prevMain := g.tree.TakeEvictedMain()
		if err := g.tree.Pop(); err != nil {
			return
		}
		if prevMain != nil {
			_ = g.tree.Push(prevMain)
		}
	}
	// <CR> on a schema row reloads the TABLES rail via a worker.
	// When the session is disconnected the handler
	// short-circuits into the reconnect dialog instead of attempting
	// a ListTables call (ping-on-interaction).
	nav.OnSchemaActivate = func(schema string) {
		// if disconnected, trigger the reconnect flow instead.
		if g.queryState.queryRunner != nil && g.queryState.queryRunner.IsDisconnected() {
			if g.controllers != nil && g.controllers.Reconnect != nil {
				_ = g.controllers.Reconnect.Reconnect(commands.ExecCtx{})
			}
			return
		}
		if g.deps.Store != nil && g.connectionState.activeConnID != "" {
			g.deps.Store.SetLastSchemaName(g.connectionState.activeConnID, schema)
		}
		g.OnWorker(func(_ gocui.Task) error {
			// make the selected schema the live search_path so
			// unqualified queries resolve against it and the status bar
			// reflects the active schema. On failure (e.g. schema dropped
			// out from under us) keep loading its tables regardless.
			if sess := g.queryState.activeSQLSession; sess != nil {
				sql, value := schemaSearchPathSQL(schema)
				if _, err := sess.Execute(context.Background(), models.Query{SQL: sql}); err != nil {
					logs.Event(g.deps.Common.Logger(), "gui", "schema_search_path_failed",
						slog.String("schema", schema),
						slog.String("err", err.Error()))
				} else {
					g.persistTrackedSetting(context.Background(), "search_path", value)
				}
			}

			connectInv.populateTablesRail(context.Background(), schema)

			// re-warm the completion metadata snapshot for the
			// newly selected schema (table+view + function names) so FROM /
			// function completion in the new schema serves from the store.
			// Already on a worker; LoadEager is synchronous + idempotent.
			if g.schemaWarmer != nil {
				g.schemaWarmer.LoadEager(schema)
			}

			// Switch the consolidated rail to the Tables tab and push the
			// SCHEMA_RAIL container (NOT the leaf) so the user lands on the
			// refreshed tables after picking a schema.
			if g.registry.SchemaRail == nil {
				return nil
			}
			g.registry.SchemaRail.SetActiveTab(guicontext.SchemaRailTabTables)
			return connectInv.g.tree.Push(g.registry.SchemaRail)
		})
	}
	// <CR> on a table row loads the COLUMNS and INDEXES rails for
	// the selected table on a single worker (one composite enqueue
	// prevents double-focus-jumps and stale-
	// load races between the two rails). Both rails are pushed
	// atomically after Load completes; the focus push targets the
	// COLUMNS rail, matching the prior behaviour.
	nav.OnTableActivate = g.buildOnTableActivate(connectInv)
	return nav
}

// wireHelperDeps builds the UIDeps, QueryDeps, and ThreadingDeps bundles.
// Extracted verbatim from wireWithDriver.
func (g *Gui) wireHelperDeps() (controllers.UIDeps, controllers.QueryDeps, controllers.ThreadingDeps) {
	// UIDeps — Confirm and Toast are the required (compile-time)
	// parameters; the remaining popups are set on the returned struct.
	uiDeps := controllers.NewUIDeps(g.confirmHelp, g.toastHelp)
	uiDeps.Prompt = g.promptHelp
	uiDeps.Choice = g.choiceHelp
	uiDeps.Tip = g.tipHelp
	uiDeps.TableDouble = g.tablesHelp
	uiDeps.Menu = &menuPushHelper{tree: g.tree, menu: g.registry.Menu}

	// QueryDeps — all optional; set directly.
	query := controllers.QueryDeps{
		QueryRunner:  g.queryState.queryRunner,
		ResultTabs:   g.resultTabsH,
		EditorBuffer: newEditorBufferAdapter(g.registry.QueryEditor),
		Notice:       g.noticeHelp,
		KbRuntime:    g.keybindingSystem.kbRuntime,
		// PlanController dispatches against the active plan tab's
		// PlanContext. Closing over g.resultTabsH so
		// ActivePlanContext stays in lockstep with whatever the user
		// has currently focused. Nil-safe — returns nil when the
		// helper is unwired or no plan tab is active.
		ActivePlanContextFn: func() *guicontext.PlanContext {
			if g.resultTabsH == nil {
				return nil
			}
			return g.resultTabsH.ActivePlanContext()
		},
		// ConnProfile surfaces the live profile captured at Connect so the
		// query editor can gate mutating statements behind ConfirmWrites /
		// ConfirmDDL. nil until the first successful Connect.
		ConnProfile: func() *models.Connection {
			return g.connectionState.activeConnProfile
		},
		// MetadataInvalidator routes post-run DDL + manual-'r' metadata
		// invalidation to the SchemaWarmer. The warmer is built later in
		// wireEditorCompletion (after this bundle is value-copied into the
		// controllers), so the adapter resolves g.schemaWarmer lazily at
		// call time rather than capturing it here.
		MetadataInvalidator: &metadataInvalidatorAdapter{g: g},
		// FocusResults pushes the active result tab onto the focus stack so
		// the results pane takes focus after a query opens a tab. Mirrors the
		// OnTableActivate push (buildOnTableActivate); Push no-ops when the
		// context is already top. Nil-safe at the call site.
		FocusResults: func() error {
			if g.tree == nil || g.resultTabsH == nil {
				return nil
			}
			target := g.resultTabsH.ActiveContext()
			if target == nil {
				return nil
			}
			return g.tree.Push(target)
		},
	}

	// ThreadingDeps (DESIGN.md §17) — all three closures
	// are required (compile-time) parameters. Bound to the Gui's methods
	// so controllers can schedule UI-thread work and spawn background
	// workers without importing the orchestrator.
	threading := controllers.NewThreadingDeps(g.OnUIThread, g.OnUIThreadContentOnly, g.OnWorker)
	return uiDeps, query, threading
}

// wireInlineEditControllers builds the four inline-edit popup controllers and
// attaches each to its context so their bindings reach the trie via
// AllDefaultBindings. Extracted verbatim from wireWithDriver.
func (g *Gui) wireInlineEditControllers(helperBag controllers.HelperBag) {
	// build the four inline-edit popup controllers and
	// attach each to its context so their bindings reach the trie via
	// AllDefaultBindings. Mirrors the TableInspect path above —
	// constructed here because every controller needs a FocusPopper
	// handle on the focus-stack (*gui.ContextTree), which the controllers
	// package cannot import. A follow-up plumbs the
	// per-controller hooks (apply, dry-run, picker, store, runner) once
	// the apply pipeline and per-table store land.
	if g.registry != nil && g.tree != nil {
		if cellCtx := g.registry.CellEditor; cellCtx != nil {
			// Wire the per-scope ModeSetter so CellEditorContext.HandleFocus
			// can flip the CELL_EDITOR scope into ModeInsert on push — the
			// master Editor's Passthrough then delegates printable runes to
			// gocui.DefaultEditor (TextArea). Mirrors Prompt.SetModes
			// (gui.go ~822). NOTE: ModeInsert, not ModeCommand — the
			// commit/discard chords bind under ModeInsert.
			cellCtx.SetModes(g.keybindingSystem.modeStore)
			cellCtrl := controllers.NewCellEditorController(
				g.deps.Common, helperBag.CoreDeps, helperBag.UIDeps, cellCtx, g.tree, nil, nil,
			)
			cellCtrl.AttachToContext(&cellCtx.BaseContext)
			// picker resolves the active tab's
			// grid + cursor per call; store resolves the per-(connID,
			// baseTable) PendingEditSet via the same helperBag closure the
			// commit dialog uses, keeping both flows on the same set.
			cellCtrl.SetPicker(cellEditorPicker{tabs: g.resultTabsH})
			cellCtrl.SetStore(cellEditorStore{resolve: helperBag.ActivePendingEditSet})
			g.controllers.CellEditor = cellCtrl
		}
		// a single CellApplyHelper
		// instance is shared by the commit-dialog apply/dry-run hooks and
		// the conflict-dialog overwrite hook. The helper is stateless
		// beyond its acquirer; both dialogs route through the same
		// connHelperAcquirer so per-call session resolution stays unified.
		cellApply := helpers.NewCellApplyHelper(helpers.CellApplyDeps{
			Acquirer: connHelperAcquirer{h: g.connectHelper},
		})
		if commitCtx := g.registry.CommitDialog; commitCtx != nil {
			commitCtrl := controllers.NewCommitDialogController(
				g.deps.Common, helperBag.CoreDeps, helperBag.UIDeps, helperBag.EditDeps, commitCtx, g.tree,
			)
			commitCtrl.AttachToContext(&commitCtx.BaseContext)
			g.controllers.CommitDialog = commitCtrl

			// wire the apply / dry-run /
			// show-sql hooks. CellApplyHelper acquires its own session
			// per call via connHelperAcquirer so it does not entangle
			// with the user's main SQLSession transactions.
			cdDeps := commitDialogDeps{
				apply:       cellApply,
				tabs:        g.resultTabsH,
				conflictCtx: g.registry.ConflictDialog,
				tree:        g.tree,
				toast:       g.toastHelp,
				logger:      g.deps.Common.Logger(),
				onUI:        g.OnUIThread,
				evictCounts: g.evictRelationshipCounts,
			}
			commitCtrl.SetApplyHook(commitApplyHook{deps: cdDeps})
			commitCtrl.SetDryRunHook(commitDryRunHook{deps: cdDeps})
			commitCtrl.SetShowSqlHook(commitShowSqlHook{logger: cdDeps.logger})
		}
		if conflictCtx := g.registry.ConflictDialog; conflictCtx != nil {
			conflictCtrl := controllers.NewConflictDialogController(
				g.deps.Common, helperBag.CoreDeps, conflictCtx, g.tree,
			)
			conflictCtrl.AttachToContext(&conflictCtx.BaseContext)
			g.controllers.ConflictDialog = conflictCtrl

			// wire refresh + overwrite hooks.
			// Cancel is intentionally unwired — the controller's default
			// pop already covers the no-mutation Esc path.
			cfDeps := conflictDialogDeps{
				apply:         cellApply,
				tabs:          g.resultTabsH,
				toast:         g.toastHelp,
				activeSetFunc: helperBag.ActivePendingEditSet,
				evictCounts:   g.evictRelationshipCounts,
			}
			conflictCtrl.SetRefreshHook(conflictRefreshHook{deps: cfDeps})
			conflictCtrl.SetOverwriteHook(conflictOverwriteHook{deps: cfDeps})
		}
		if pickerCtx := g.registry.FKReversePicker; pickerCtx != nil {
			pickerCtrl := controllers.NewFKReversePickerController(
				g.deps.Common, helperBag.CoreDeps, controllers.FKReversePickerDeps{
					Context: pickerCtx,
					Tree:    g.tree,
					Runner:  g.queryState.queryRunner,
					Tabs:    g.resultTabsH,
					Jumps:   g.jumpListH,
					Toast:   g.toastHelp,
				},
			)
			pickerCtrl.AttachToContext(&pickerCtx.BaseContext)
			g.controllers.FKReversePicker = pickerCtrl
		}
		if viewerCtx := g.registry.CellViewer; viewerCtx != nil {
			viewerCtrl := controllers.NewCellViewerController(
				g.deps.Common, helperBag.CoreDeps, helperBag.UIDeps, viewerCtx, g.tree, nil,
			)
			viewerCtrl.AttachToContext(&viewerCtx.BaseContext)
			viewerCtrl.SetPicker(cellEditorPicker{tabs: g.resultTabsH})
			viewerCtrl.SetClipboard(clipboard.NewSystemClipboard())
			viewerCtrl.SetCellEditor(g.controllers.CellEditor)
			g.controllers.CellViewer = viewerCtrl
		}
	}
}

// wireActionRegistrations registers every controller's action handlers plus the
// orchestrator-owned commands (cheatsheet, connection-manager, tip dismiss,
// rail switch, command-line). Extracted verbatim from wireWithDriver.
func (g *Gui) wireActionRegistrations(connectInv *connectInvoker) {
	// wire the post-yank highlight seam so a yank tints the
	// yanked span for yankFlashTTL (Neovim on_yank parity). Constructed here
	// (not in the controllers wiring layer) because the concrete helper lives
	// in helpers/ui and the controllers package must not import it; g.driver
	// is set well before this point (toastHelp uses it earlier).
	if g.controllers != nil && g.controllers.VimEditor != nil {
		g.controllers.VimEditor.SetYankFlasher(ui.NewYankFlashHelper(g.driver))
	}

	// Register every controller's action handlers with the registry.
	g.controllers.RegisterActions(g.keybindingSystem.cmdRegistry)

	// TableInspectOpen — `i` on TABLES opens the tabbed
	// popup, sets the target (schema, table), and dispatches column +
	// index refreshes via OnWorker. Re-pressing `i` while the popup is
	// already on top re-targets without a second Push.
	if g.registry != nil && g.registry.TableInspect != nil && g.tree != nil {
		g.registerTableInspectOpen(connectInv)
	}

	// HistoryOpen — `<leader>h` in the QUERY_EDITOR switches the QUERY_RAIL
	// container to the History tab. The leaf lazily loads Recent(N) off-thread
	// on its first activation (and after a query run), refreshing on the UI
	// thread.
	if g.registry != nil && g.registry.History != nil {
		g.registerHistoryOpen()
	}

	// QuerySavedOpen — `<leader>o` in the QUERY_EDITOR switches the QUERY_RAIL
	// container to the Saved Queries tab. The leaf lazily loads queries.yml
	// off-thread on its first activation (and after a save/delete), refreshing
	// on the UI thread.
	if g.registry != nil && g.registry.SavedQuery != nil {
		g.registerSavedQueryOpen()
	}

	// rail highlight+jump search (/ n N <esc>) on SCHEMAS
	// and TABLES. Single action IDs; the handler resolves the focused
	// rail from ctx.Scope. Needs the registry + SearchLine helper.
	if g.registry != nil && g.keybindingSystem.cmdRegistry != nil && g.searchLineHelp != nil {
		g.registerRailSearch()
	}

	// Rail-switch (1-6, Tab) needs the focus tree + context registry,
	// which the Controllers aggregate does not hold; register here. The
	// results-resolver closes over g.resultTabsH so digit 6 / cycle-to-
	// results push the live active tab's IBaseContext onto the focus
	// stack. nil helper → resolver returns nil → digit 6
	// is a silent no-op (e.g. pre-Connect, helper not yet wired).
	resolveResults := func() types.IBaseContext {
		if g.resultTabsH == nil {
			return nil
		}
		return g.resultTabsH.ActiveContext()
	}
	controllers.RegisterRailSwitchActions(g.keybindingSystem.cmdRegistry, g.tree, g.registry, resolveResults)

	// QUERY_RAIL `[`/`]` tab-cycle handlers. The container lives in the
	// context tree; pass it only when wired so the handlers receive a genuine
	// nil interface (not a typed-nil *QueryRailContext) when it is absent.
	var queryRail controllers.QueryRailTabber
	if g.registry != nil && g.registry.QueryRail != nil {
		queryRail = g.registry.QueryRail
	}
	controllers.RegisterQueryRailTabActions(g.keybindingSystem.cmdRegistry, queryRail)

	// Cheatsheet popup: capture the focused scope, build one tab per cheatsheet
	// Category (always-on General + any non-empty buckets), inject them as
	// DisplayLeafContext leaves into the CHEATSHEET container, then push the
	// container onto the focus stack. RunLayout's Tier-3 popup pass (layout.go)
	// renders it on the next frame. Tab cycling + close run through the trie via
	// CheatsheetController bindings. SetScope+SetTabs run UNCONDITIONALLY on every
	// `?` press so a second `?` on a different focus rebuilds the tabs.
	_ = g.keybindingSystem.cmdRegistry.Register(&commands.Command{
		ID:          commands.HelpCheatsheet,
		Description: "Show cheatsheet",
		Tag:         "Help",
		Handler: func(commands.ExecCtx) error {
			if g.registry == nil || g.registry.Cheatsheet == nil {
				return nil
			}
			scope := cheatsheetScope(g.tree.Current())
			g.registry.Cheatsheet.SetScope(scope)
			specs, leaves := g.buildCheatsheetCategoryTabs(scope)
			g.registry.Cheatsheet.SetTabs(specs, leaves)
			logs.Event(g.deps.Common.Logger(), "input", "cheatsheet_open",
				slog.String("scope", string(scope)),
				slog.Int("tab_count", len(specs)),
			)
			return g.tree.Push(g.registry.Cheatsheet)
		},
	})

	// <leader>C opens the CONNECTION_MANAGER modal mid-session.
	// GLOBAL-scoped so it fires from any focused view. The handler pushes
	// the modal onto the focus stack (OnShow populates + restores cursor).
	_ = g.keybindingSystem.cmdRegistry.Register(&commands.Command{
		ID:          commands.ConnectionManagerOpen,
		Description: "Open connection manager",
		Tag:         "Connection",
		Handler: func(commands.ExecCtx) error {
			if g.tree == nil || g.registry == nil || g.registry.ConnectionManager == nil {
				return nil
			}
			return g.tree.Push(g.registry.ConnectionManager)
		},
	})

	// <leader>os opens the SETTINGS modal mid-session.
	_ = g.keybindingSystem.cmdRegistry.Register(&commands.Command{
		ID:          commands.SettingsOpen,
		Description: "Open settings",
		Handler: func(commands.ExecCtx) error {
			if g.tree == nil || g.registry == nil || g.registry.Settings == nil {
				return nil
			}
			_ = g.tree.PopIfTop(types.COMMAND_LINE)
			return g.tree.Push(g.registry.Settings)
		},
	})

	// TipDismiss handler. Pops the FIRST_RUN_TIP popup
	// and stamps StartupTipsSeenAt via AppStateStore.StampStartupTips.
	// The action is wired regardless of whether the tip is currently
	// visible — the popped Pop() error is logged at warn and the dismiss
	// proceeds (AC: "if StampStartupTips fails to persist, tip still
	// dismisses; error logged at warn"). The store's debounced save is
	// fire-and-forget; any persistence failure is captured by the store
	// itself via LastSaveErr + its own slog cat=state event.
	_ = g.keybindingSystem.cmdRegistry.Register(&commands.Command{
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
	// minimal welcome popup), so it cannot rely on a trie binding + the
	// automatic installEscAbortShim the way the CHEATSHEET popup does
	// (CHEATSHEET <esc> is a CheatsheetController trie binding, not a driver
	// SetKeybinding); this view needs the explicit driver shim instead.
	dismissTip := func() error {
		cmd, ok := g.keybindingSystem.cmdRegistry.Get(commands.TipDismiss)
		if !ok || cmd == nil || cmd.Handler == nil {
			return nil
		}
		return cmd.Handler(commands.ExecCtx{})
	}
	_ = g.driver.SetKeybinding(string(types.FIRST_RUN_TIP), gocui.NewKeyName(gocui.KeyEsc), gocui.ModNone, dismissTip)
	_ = g.driver.SetKeybinding(string(types.FIRST_RUN_TIP), gocui.NewKeyName(gocui.KeyEnter), gocui.ModNone, dismissTip)

	// `a` / `?` on the FIRST_RUN_TIP view fulfil the tip's "Press ? ... Press a
	// to add your first connection" copy. Both are otherwise-scoped bindings
	// (CONNECTION_MANAGER `a`, GLOBAL-trie `?`) that never fire while the tip
	// owns input through its direct view shims. Each dismisses the tip, then
	// dispatches the real action. dismissTip's Pop fires the new top's
	// HandleFocus first; the dispatched action runs last so its state wins
	// (e.g. CONNECTION_MANAGER lands in ModeForm, not the HandleFocus-reset
	// ModeList).
	dismissTipThen := func(actionID string) func() error {
		return func() error {
			if err := dismissTip(); err != nil {
				return err
			}
			cmd, ok := g.keybindingSystem.cmdRegistry.Get(actionID)
			if !ok || cmd == nil || cmd.Handler == nil {
				return nil
			}
			return cmd.Handler(commands.ExecCtx{})
		}
	}
	_ = g.driver.SetKeybinding(string(types.FIRST_RUN_TIP), gocui.NewKeyRune('a'), gocui.ModNone, dismissTipThen(commands.ConnectionManagerAdd))
	_ = g.driver.SetKeybinding(string(types.FIRST_RUN_TIP), gocui.NewKeyRune('?'), gocui.ModNone, dismissTipThen(commands.HelpCheatsheet))

	// COMMAND_LINE action commands. The CommandLineContext doubles as
	// the holder (it implements types.IBaseContext + ReadAndClearBuffer).
	cmdDeps := keys.CommandLineCommandDeps{
		Stack:        g.tree,
		Context:      g.registry.CommandLine,
		ExRegistry:   g.keybindingSystem.exRegistry,
		Toaster:      g.toaster,
		CaretToggler: g.caret,
	}
	_ = g.keybindingSystem.cmdRegistry.Register(keys.CommandOpenCommand(cmdDeps))
	_ = g.keybindingSystem.cmdRegistry.Register(keys.CommandCancelCommand(cmdDeps))
	_ = g.keybindingSystem.cmdRegistry.Register(keys.CommandSubmitCommand(cmdDeps))
}

// wireTrie validates the user config and builds the keybinding trie. Returns the
// built TrieSet plus the defaults / service needed by :reload. Extracted verbatim
// from wireWithDriver.
func (g *Gui) wireTrie(cfg *config.UserConfig) (*keys.TrieSet, []*types.ChordBinding, *keys.KeybindingService, error) {
	// validate UserConfig now that cmdRegistry and the
	// context registry are populated. Deviation from AD-2 literal ordering
	// (validate-after-NewGui-before-RunAndHandleError) — registries are
	// built inside wireWithDriver, so validation moves here. AD-2's safety
	// rationale (deferred g.Close fires) is preserved: g.Close is idempotent
	// (gui.go:986-989) and entry_point.go's `defer func() { _ = g.Close() }()`
	// runs regardless of where the error originates.
	if cfg != nil {
		deps := config.ValidationDeps{
			ActionExists: func(id string) bool { return g.keybindingSystem.cmdRegistry.Has(id) },
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
			return nil, nil, nil, fmt.Errorf("config: %d validation error(s)", len(cfgErrs))
		}
		g.deps.Common.Logger().Info("config: validated", "warnings", len(cfgWarns), "cat", "app")
	}

	// Build the trie. `scope: all` expands over the live registry's
	// context keys (filtered by kindOf inside the service), so a new
	// non-popup context is reached without editing pkg/gui/keys.
	knownContexts := make([]types.ContextKey, 0)
	for _, ctx := range g.registry.Flatten() {
		if ctx != nil {
			knownContexts = append(knownContexts, ctx.GetKey())
		}
	}
	svc := keys.NewKeybindingService(knownContexts...)
	defaults := controllers.AllDefaultBindings(g.controllers)
	trieSet, warnings, buildErr := svc.Build(defaults, cfg, g.keybindingSystem.cmdRegistry, g.kindOf)
	if buildErr != nil {
		return nil, nil, nil, fmt.Errorf("gui: Build: %w", buildErr)
	}
	for _, w := range warnings {
		g.deps.Common.Logger().Warn(fmt.Sprintf("keybindings: [%s] %s (%s)", w.Code, w.Message, w.Origin))
	}
	g.keybindingSystem.lastWarnings = warnings
	g.keybindingSystem.matcher.SwapTrieSet(trieSet)
	return trieSet, defaults, svc, nil
}

// wireExCommands registers the vim-style ex-commands (:q / :w / :set / :reset /
// :c / :reload) plus the search-path and statement-timeout prompt runners.
// Extracted verbatim from wireWithDriver.
func (g *Gui) wireExCommands(defaults []*types.ChordBinding, svc *keys.KeybindingService) {
	// :reload ex-command — see registerReloadEx for the LoadUserConfig
	// stub rationale. defaults / svc are wireWithDriver locals; g.keybindingSystem.matcher
	// (== matcher) is used inside the method.
	_ = g.registerReloadEx(defaults, svc)

	// :q / :quit / :q! / :w — vim-style quit/write ex-commands. Handlers
	// live in gui_ex_commands.go.
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "q", Description: "Quit", Handler: g.handleQuitEx})
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "quit", Description: "Quit", Handler: g.handleQuitEx})
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "q!", Description: "Force quit", Handler: g.handleForceQuitEx})
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "w", Description: "Open commit dialog", Handler: g.handleWriteEx})

	// :set — execute SET on the live SQL session. Handler in
	// gui_ex_commands.go.
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "set", Description: "Execute SET on session", Handler: g.handleSetEx})

	// wire the search_path quick-set prompt to the SET handler.
	if g.controllers != nil && g.controllers.SearchPath != nil {
		g.controllers.SearchPath.SetRunner = g.handleSetEx
	}

	// wire the statement timeout prompt to the SET handler + AppState.
	if g.controllers != nil && g.controllers.StatementTimeout != nil {
		g.controllers.StatementTimeout.SetRunner = g.handleSetEx
		g.controllers.StatementTimeout.ActiveConnID = func() string {
			return g.connectionState.activeConnID
		}
		g.controllers.StatementTimeout.PersistTimeout = func(connID, timeout string) {
			if g.deps.Store == nil {
				return
			}
			g.deps.Store.MutateAndSave(func(a *common.AppState) {
				if timeout == "" {
					if a.StatementTimeoutOverride != nil {
						delete(a.StatementTimeoutOverride, connID)
					}
					return
				}
				if a.StatementTimeoutOverride == nil {
					a.StatementTimeoutOverride = make(map[string]string)
				}
				a.StatementTimeoutOverride[connID] = timeout
			})
		}
	}

	// :reset — execute RESET on the live SQL session. Handler in
	// gui_ex_commands.go.
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "reset", Description: "Execute RESET on session", Handler: g.handleResetEx})

	// :c — reject cross-database attach (not supported).
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{
		Name:        "c",
		Description: "Cross-database attach (not supported)",
		Handler:     g.handleCrossDBEx,
	})

	// :tip — re-show the first-run welcome tip on demand (independent of the
	// startup seen-stamp / zero-connections gate). Handler in gui_ex_commands.go.
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "tip", Description: "Show the first-run welcome tip", Handler: g.handleShowTipEx})

	// :settings — open the settings modal on demand.
	_ = g.keybindingSystem.exRegistry.Register(keys.ExCommand{Name: "settings", Description: "Open settings", Handler: g.handleSettingsEx})
}

// wireKeyDispatch installs the master editor / per-key dispatch, wires the mouse,
// registers focus-swap hooks, seeds the CONNECTION_MANAGER startup root, and
// pushes the first-run tip. Extracted verbatim from wireWithDriver.
func (g *Gui) wireKeyDispatch(trieSet *keys.TrieSet, cfg *config.UserConfig, tablePicker tablesPickerAdapter) error {
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
			Matcher:     g.keybindingSystem.matcher,
			TableDouble: g.tablesHelp,
			TablePicker: tablePicker,
			RailActiveTabIsTables: func() bool {
				return g.registry != nil && g.registry.SchemaRail != nil &&
					g.registry.SchemaRail.ActiveTab() == guicontext.SchemaRailTabTables
			},
		}); err != nil {
			return fmt.Errorf("gui: wire mouse: %w", err)
		}

		// Consolidated-rail native tab clicks. gocui dispatches a click on the
		// border-row tab labels through SetTabClickBinding with the precomputed
		// tab index (NOT via the body-relative ViewMouseBinding path). The
		// callback fires off the MainLoop, so the active-tab mutation is
		// marshalled back through OnUIThread (which also schedules a re-layout
		// so the new marker/colours paint). Installed once here.
		if g.registry != nil && g.registry.SchemaRail != nil {
			rail := g.registry.SchemaRail
			_ = g.driver.SetTabClickBinding(guicontext.SchemaRailViewName, func(idx int) error {
				g.OnUIThread(func() error {
					rail.SetActiveTab(idx)
					return nil
				})
				return nil
			})
		}

		// QUERY_RAIL container native tab clicks. Same shape as the schema-rail
		// binding above: gocui dispatches a border-row tab click through
		// SetTabClickBinding with the precomputed index; the callback fires off
		// the MainLoop, so the active-tab switch is marshalled back through
		// OnUIThread (which also schedules the re-layout that repaints the new
		// marker/colours). Installed once here.
		if g.registry != nil && g.registry.QueryRail != nil {
			queryRail := g.registry.QueryRail
			_ = g.driver.SetTabClickBinding(guicontext.QueryRailViewName, func(idx int) error {
				g.OnUIThread(func() error {
					queryRail.SetActiveTab(idx)
					return nil
				})
				return nil
			})
		}
	}

	// Cancel any pending matcher partial / which-key on focus change.
	// SetCurrentView is plumbed inline by RunLayout (Tier 4 final step)
	// rather than via a swap hook, so it can't race the Layout pass's
	// SetViewOnTop loop.
	g.tree.RegisterSwapHook(g.keybindingSystem.matcher.Cancel)
	g.tree.RegisterSwapHook(g.keybindingSystem.whichkey.Hide)

	// Cancel the active result-tab stream when the user navigates out
	// of the QueryEditor / result-tab pane while a query is still
	// Running.
	installResultTabsSwapHook(g.tree, g.resultTabsH)

	// Seed the CONNECTION_MANAGER modal's list from the on-disk profiles
	// before the first render frame.
	g.refreshConnectionManagerRail()
	g.restoreConnectionManagerCursor()

	// Push the CONNECTION_MANAGER modal as the startup root context.
	if err := g.tree.Push(g.registry.ConnectionManager); err != nil {
		return err
	}

	// push the first-run welcome tip on top of the modal
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
