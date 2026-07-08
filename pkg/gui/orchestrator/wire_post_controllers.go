package orchestrator

import (
	"fmt"
	"time"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/clipboard"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// wireEditDeps builds the EditDeps inline-edit helper bundle.
// All optional.
func (g *Gui) wireEditDeps() controllers.EditDeps {
	return controllers.EditDeps{
		PendingDiscard: g.pendingDiscardH,
		JumpList:       g.jumpListH,
		FKForward:      g.fkForwardH,
		// gD picker open — resolves through g.controllers at dispatch
		// time so the closure works despite the controllers aggregate
		// being filled in AttachControllers AFTER this HelperBag is
		// composed.
		OpenFKReversePicker: func(entries []controllers.ReverseEntry, origin controllers.FKReverseOriginTab, row, col int) bool {
			if g.controllers.FKReversePicker == nil {
				return false
			}
			return g.controllers.FKReversePicker.Open(entries, origin, row, col)
		},
		// Reverse-FK resolver — routes each lookup through the active
		// SQLSession's FKCache so per-Connect rotation is invisible to
		// the picker handler. Extracted to a named
		// method so the parked-stream preempt is regression-testable
		// symmetric with the forward adapter.
		ReverseFKLookup: g.lookupReverseFK,
		// ActivePendingEditSet resolves the per-(connID, baseTable) set
		// from the registry using the currently-active tab's identity.
		// Returns nil when no tab is active OR the tab has no row
		// identity (non-table-backed result).
		ActivePendingEditSet: func() *models.PendingEditSet {
			if g.resultTabsH == nil || g.pendingEditReg == nil {
				return nil
			}
			tab := g.resultTabsH.Active()
			if tab == nil {
				return nil
			}
			connID, ri := tab.Identity()
			if connID == "" || ri.BaseTable == "" || !ri.HasRowIdentity {
				return nil
			}
			set := g.pendingEditReg.For(connID, ri.BaseTable)
			if set != nil {
				if gv := tab.Grid(); gv != nil {
					// ri.BaseTable comes from SQL-text parsing, so it carries
					// no schema for an unqualified `SELECT ... FROM tbl`.
					// Backfill the catalog-resolved schema editability
					// introspection stored on the grid, otherwise the
					// apply-path UPDATE is unqualified and fails to resolve on
					// a fresh pooled session whose search_path doesn't include
					// the table's schema.
					if set.Table.Schema == "" {
						if sch := gv.IdentitySchema(); sch != "" {
							set.Table.Schema = sch
						}
					}
					// Point the grid at this exact set so the dirty-cell
					// renderer shows staged values. The pointer is stable per
					// (connID, baseTable) and the cell editor stages into the
					// same instance via this resolver, so a staged edit
					// reflects on the next render.
					gv.SetPendingEdits(set)
				}
			}
			return set
		},
		// ActiveConnectionProfile surfaces the live profile captured at
		// connectInvoker.Connect. nil until the first successful Connect.
		ActiveConnectionProfile: func() *models.Connection {
			return g.connectionState.activeConnProfile
		},
	}
}

// wirePopupStates wires the popup-body state readers and the popup
// controllers (CONNECTION_MANAGER modal, Prompt, Selection, SearchLine,
// TableInspect, History, Cheatsheet) now that g.controllers exists. The
// four inline-edit popup controllers are wired separately.
func (g *Gui) wirePopupStates(helperBag controllers.HelperBag, connectInv *connectInvoker) {
	// wire the CONNECTION_MANAGER modal's list + in-modal-connect
	// closures now that both the modal context and connectInvoker exist. The
	// handlers run on the MainLoop (keybinding dispatch), so the connectInvoker
	// gen seams stay serialised. The connect lifecycle renders INSIDE the modal
	// (no standalone CONNECTING push); a successful publish pops the modal.
	if g.controllers != nil && g.controllers.ConnectionManager != nil && g.registry != nil && g.registry.ConnectionManager != nil {
		// Populate rows + restore the last-used cursor each time the modal
		// gains focus.
		g.registry.ConnectionManager.SetOnShow(func() {
			g.refreshConnectionManagerRail()
			g.restoreConnectionManagerCursor()
		})
		g.controllers.ConnectionManager.SetDeps(controllers.ConnectionManagerDeps{
			Ctx:     g.registry.ConnectionManager,
			Connect: connectInv.startModalAttempt,
			Retry:   connectInv.Retry,
			CancelConnecting: func() {
				connectInv.Cancel()
				g.registry.ConnectionManager.SetMode(guicontext.ModeList)
			},
			// add/edit form wiring. Prompt drives the per-field
			// PROMPT popup; ExistingNames + DriversFn back validation + the
			// driver selector. OnSaveConnection persists the
			// validated profile (append for add, update for edit) and refreshes
			// the modal list from disk. The full conn carries the
			// form-untouched fields (Password, SSHTunnel, …) so the rewrite
			// preserves them.
			Prompt:             g.promptHelp,
			ExistingNames:      g.connectionNames,
			DriversFn:          drivers.Names,
			OnSaveConnection:   g.saveConnectionForm,
			OnDeleteConnection: g.deleteConnectionFromModal,
			// paste-DSN: read the host clipboard and surface the
			// dropped-password warning via a toast.
			ReadClipboard: clipboard.NewSystemClipboard().Read,
			// test-connection: dial the in-progress (unsaved) profile via the
			// decoupled drivers.Get → Open primitive and publish pass/fail
			// inline, without disturbing the live active session. The closure
			// owns the dedicated test-gen/cancel/stale guard + worker dispatch.
			TestConnection: connectInv.testConnection,
			ShowToast: func(msg string) {
				if g.toastHelp != nil {
					g.toastHelp.Show(msg, 3*time.Second)
				}
			},
			StackDepth: func() int {
				if g.tree == nil {
					return 1
				}
				return len(g.tree.Stack())
			},
		})
	}

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
		// drop.
		g.registry.Prompt.SetModes(g.keybindingSystem.modeStore)
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
	// the SEARCH_LINE search input gets the same caret
	// toggle through SearchLineHelper's lifecycle, and a SEARCH_LINE
	// controller for its <cr>/<esc> bindings attached to the context.
	if g.searchLineHelp != nil {
		g.searchLineHelp.SetCaretToggler(func(enabled bool) {
			if g.driver != nil {
				g.driver.SetCaretEnabled(enabled)
			}
		})
	}
	if g.registry != nil && g.registry.SearchLine != nil && g.controllers != nil {
		searchCtrl := controllers.NewSearchLineController(
			g.deps.Common, helperBag.CoreDeps, g.searchLineHelp, g.registry.SearchLine,
		)
		searchCtrl.AttachToContext(&g.registry.SearchLine.BaseContext)
		g.controllers.SearchLine = searchCtrl
	}

	// build the TABLE_INSPECT popup controller and attach
	// it to its context so its bindings reach the trie via
	// AllDefaultBindings (the bundle is consumed two blocks down at
	// trie-build time). Constructed here — not in AttachControllers —
	// because it needs a Pop-capable handle on the focus-stack
	// (*gui.ContextTree), which the controllers package must not import.
	if g.registry != nil && g.registry.TableInspect != nil && g.tree != nil {
		inspectCtx := g.registry.TableInspect
		inspectCtrl := controllers.NewTableInspectController(
			g.deps.Common, helperBag.CoreDeps, inspectCtx, g.tree,
		)
		inspectCtrl.AttachToContext(&inspectCtx.BaseContext)
		g.controllers.TableInspect = inspectCtrl
	}

	// switchToEditorTab flips the QUERY_RAIL container back to the editor tab.
	// Shared by the HISTORY/SAVED_QUERY leaf controllers' <cr>/<esc> handlers:
	// the list leaves are container tabs, not popups, so they switch tabs
	// rather than pop the focus stack. Nil-safe when the container is unwired.
	switchToEditorTab := func() {
		if g.registry == nil || g.registry.QueryRail == nil {
			return
		}
		g.registry.QueryRail.SetActiveTab(controllers.QueryRailEditorTab)
	}

	// build the HISTORY leaf controller and attach it to its context so its
	// j/k/gg/G/<cr>/<esc> bindings reach the trie via AllDefaultBindings.
	// Constructed here — not in AttachControllers — because it closes over the
	// QUERY_RAIL container (switchToEditorTab) which the controllers package
	// must not import. <cr> inserts the SQL then switches to the editor tab;
	// <esc> switches to the editor tab. Neither pops the focus stack. The
	// editor buffer adapter receives the inserted SQL.
	if g.registry != nil && g.registry.History != nil {
		historyCtx := g.registry.History
		historyCtrl := controllers.NewHistoryController(
			g.deps.Common, helperBag.CoreDeps, historyCtx,
			newEditorBufferAdapter(g.registry.QueryEditor), switchToEditorTab,
		)
		historyCtrl.AttachToContext(&historyCtx.BaseContext)
		g.controllers.History = historyCtrl
	}

	// build the SAVED_QUERY leaf controller and attach it to its context so
	// its j/k/gg/G/<cr>/dd/<esc> bindings reach the trie via
	// AllDefaultBindings. Constructed here — not in AttachControllers —
	// because it closes over the QUERY_RAIL container (switchToEditorTab). <cr>
	// inserts the SQL then switches to the editor tab; <esc> switches to the
	// editor tab; neither pops. The editor buffer adapter receives the inserted
	// SQL; fs + QueriesPath address queries.yml for the dd delete/refresh; the
	// Confirm helper gates the delete.
	if g.registry != nil && g.registry.SavedQuery != nil {
		savedCtx := g.registry.SavedQuery
		savedCtrl := controllers.NewSavedQueryController(
			g.deps.Common, helperBag.CoreDeps, helperBag.UIDeps, savedCtx,
			newEditorBufferAdapter(g.registry.QueryEditor), switchToEditorTab,
			fsFromCommon(g.deps.Common), g.deps.QueriesPath,
		)
		savedCtrl.AttachToContext(&savedCtx.BaseContext)
		g.controllers.SavedQuery = savedCtrl
	}

	// build the CHEATSHEET popup controller and attach it
	// to the context so the [, ], <tab>, <esc>, q bindings reach the trie
	// via AllDefaultBindings. Constructed here — not in AttachControllers
	// — because it needs a Pop-capable handle on the focus-stack.
	if g.registry != nil && g.registry.Cheatsheet != nil && g.tree != nil {
		cheatCtrl := controllers.NewCheatsheetController(
			g.deps.Common, helperBag.CoreDeps, g.registry.Cheatsheet, g.tree,
		)
		cheatCtrl.AttachToContext(&g.registry.Cheatsheet.BaseContext)
		g.controllers.Cheatsheet = cheatCtrl
	}

	// build the RELATIONSHIP_PANEL controller and attach it
	// to the panel context so the RELATIONSHIP_PANEL-scoped <cr>/<esc>
	// bindings (plus the RESULT_GRID-scoped <leader>gr toggle) reach the
	// trie via AllDefaultBindings. Constructed here — not in
	// AttachControllers — because it needs a Push/Pop handle on the
	// focus-stack (*gui.ContextTree). The result-tabs helper supplies the
	// active-tab manager surface; the FK lookups route through the active
	// session's FKCache (metadata only, zero data queries); the live-follow
	// repaint marshals through OnUIThreadContentOnly. SetOnGridCursorChange
	// installs the per-tab cursor hook that drives the debounced follow.
	if g.registry != nil && g.registry.RelationshipPanel != nil && g.tree != nil {
		var mgr controllers.ResultTabsManager
		if g.resultTabsH != nil {
			mgr = g.resultTabsH
		}
		panelCtrl := controllers.NewRelationshipPanelController(
			g.deps.Common, helperBag.CoreDeps, g.registry.RelationshipPanel, g.tree,
			mgr, g.lookupForwardFK, g.lookupReverseFK, g.OnUIThreadContentOnly,
		)
		// Outbound preview resolver: acquire a FRESH POOLED session and resolve
		// the parent row's display-column value, mirroring the EstimateRows /
		// IntrospectEditability wiring (wire_result_tabs.go). A fresh session
		// never preempts the user's stream, so no ErrPreemptPending handling is
		// needed; supersede is via the panel's own row-identity epoch.
		panelCtrl.SetPreviewResolver(g.resolveRelationshipPreview)
		// Inbound estimate resolver: acquire a FRESH POOLED session and run the
		// predicated planner-only EXPLAIN, mirroring the preview resolver.
		panelCtrl.SetEstimateResolver(g.resolveRelationshipEstimate)
		// Inbound exact-count resolver: on-demand COUNT(*) for the FOCUSED inbound
		// line, a fresh pooled session under a 750ms timeout. A timeout keeps the
		// ~estimate; the result replaces the focused line's estimate.
		panelCtrl.SetExactResolver(g.resolveRelationshipExact)
		// Enter -> Jump (outbound) reuses the gd forward-FK helper; Enter ->
		// open child tab (inbound) reuses the gD reverse-picker surfaces
		// (runner / tabs / jumps) without touching the picker itself.
		panelCtrl.SetFKForward(g.fkForwardH)
		panelCtrl.SetReverseOpen(g.queryState.queryRunner, g.resultTabsH, g.jumpListH)
		// Read-only exploration breadcrumb: projects the existing jump list +
		// open-tab labels (no new trail). Reuses the same surfaces as the
		// reverse-open path (jump list + tabs helper).
		panelCtrl.SetBreadcrumb(g.jumpListH, g.resultTabsH)
		// Sequential fill runs on the tracked worker pool. Adapt the panel's
		// plain func() to the orchestrator's gocui.Task-shaped worker.
		panelCtrl.SetOnWorker(func(fn func()) {
			g.OnWorker(func(gocui.Task) error {
				fn()
				return nil
			})
		})
		panelCtrl.AttachToContext(&g.registry.RelationshipPanel.BaseContext)
		g.controllers.RelationshipPanel = panelCtrl
		if g.resultTabsH != nil {
			g.resultTabsH.SetOnGridCursorChange(panelCtrl.NotifyCursorChange)
			// Repaint the panel on every active-tab change — a jump-opened child
			// tab, a gt/gT cycle, a <leader>1..9 jump, or a <c-o>/<c-i> jump-list
			// switch — so it follows the active tab without waiting for a grid
			// cursor nudge. NotifyCursorChange only fires on row motion.
			g.resultTabsH.SetOnActiveTabSet(panelCtrl.NotifyActiveTabChanged)
		}
		// Coarse cache eviction on a query-editor DML commit: that path carries
		// no parsed target table, so a successful INSERT/UPDATE/DELETE drops ALL
		// cached inbound counts (the cell-edit commit path evicts per-table).
		// QueryEditor is populated by AttachControllers (run before this wiring).
		if g.controllers.QueryEditor != nil {
			g.controllers.QueryEditor.SetOnDMLCommit(panelCtrl.EvictAllExactCounts)
		}
	}

	// <leader>s save flow: the SaveQueryHelper (name prompt + overwrite-
	// confirm + persist to queries.yml) driven by a blocking
	// chainedPrompterAdapter on a worker goroutine. fs + QueriesPath address
	// queries.yml; the adapter wraps the prompt/choice popups so the
	// Prompt->Confirm chain never nests ConfirmHelper inside
	// PromptHelper.onSubmit (which double-pops the focus stack).
	if g.controllers.QueryEditor != nil {
		saveHelper := data.NewSaveQueryHelper(g.deps.Common, fsFromCommon(g.deps.Common), g.deps.QueriesPath)
		savePrompter := newChainedPrompterAdapter(g.promptHelp, g.choiceHelp, g.OnUIThread)
		g.controllers.QueryEditor.SetSaveQuery(saveHelper, savePrompter)

		// Invalidate-on-write: a query run marks the History tab stale; a save
		// marks the Saved Queries tab stale. The leaves reload lazily on their
		// next activation (load-once + invalidate-on-write). Nil-safe.
		if g.registry != nil && g.registry.History != nil {
			historyCtx := g.registry.History
			g.controllers.QueryEditor.SetOnAfterRun(historyCtx.MarkStale)
		}
		if g.registry != nil && g.registry.SavedQuery != nil {
			savedCtx := g.registry.SavedQuery
			g.controllers.QueryEditor.SetOnAfterSave(savedCtx.MarkStale)
		}
	}
	// Wire SettingsController's deps + SettingsContext's live-config accessor.
	if g.controllers != nil && g.controllers.Settings != nil && g.registry != nil && g.registry.Settings != nil {
		g.registry.Settings.SetCfg(func() *config.UserConfig {
			if cfg := g.deps.Common.Cfg(); cfg != nil {
				return cfg
			}
			return config.GetDefaultConfig()
		})
		g.registry.Settings.SetDefaults(func() []*types.ChordBinding {
			if g.controllers == nil {
				return nil
			}
			return controllers.AllDefaultBindings(g.controllers)
		})
		g.controllers.Settings.SetDeps(controllers.SettingsDeps{
			Ctx:     g.registry.Settings,
			Prompt:  g.promptHelp,
			Confirm: g.confirmHelp,
			ShowToast: func(msg string) {
				if g.toastHelp != nil {
					g.toastHelp.Show(msg, 3*time.Second)
				}
			},
			StackDepth: func() int {
				if g.tree == nil {
					return 1
				}
				return len(g.tree.Stack())
			},
			OnSaveConfig: func(cfg *config.UserConfig) error {
				_, errs := config.ValidateUserConfig(cfg, config.ValidationDeps{
					ActionExists: func(id string) bool { return g.keybindingSystem.cmdRegistry.Has(id) },
					ScopeExists:  g.scopeExistsPredicate(),
				})
				if len(errs) > 0 {
					return errs[0]
				}
				if err := config.SaveUserConfig(g.deps.Common.Fs, g.deps.UserConfigPath, cfg); err != nil {
					return fmt.Errorf("save config: %w", err)
				}
				knownContexts := make([]types.ContextKey, 0)
				for _, ctx := range g.registry.Flatten() {
					if ctx != nil {
						knownContexts = append(knownContexts, ctx.GetKey())
					}
				}
				svc := keys.NewKeybindingService(knownContexts...)
				defaults := controllers.AllDefaultBindings(g.controllers)
				newTrie, warnings, buildErr := svc.Build(defaults, cfg, g.keybindingSystem.cmdRegistry, g.kindOf)
				if buildErr != nil {
					return fmt.Errorf("build trie: %w", buildErr)
				}
				g.keybindingSystem.matcher.SwapTrieSet(newTrie)
				g.deps.Common.UserConfig.Store(cfg)
				g.resultTabsH.SetYankFormat(cfg.UI.ResultGrid.YankFormat)
				for _, w := range theme.ApplyUserConfig(&cfg.Theme) {
					g.toaster("theme warning: " + w)
				}
				if len(warnings) > 0 && g.deps.Common != nil {
					for _, w := range warnings {
						g.deps.Common.Logger().Warn(fmt.Sprintf("keybindings: [%s] %s (%s)", w.Code, w.Message, w.Origin))
					}
				}
				return nil
			},
			ValidationDeps: config.ValidationDeps{
				ActionExists: func(id string) bool { return g.keybindingSystem.cmdRegistry.Has(id) },
				ScopeExists:  g.scopeExistsPredicate(),
			},
			Close: func() {
				if g.tree == nil {
					return
				}
				prevMain := g.tree.TakeEvictedMain()
				if err := g.tree.Pop(); err != nil {
					if prevMain != nil {
						_ = g.tree.Push(prevMain)
					}
					return
				}
				if prevMain != nil {
					_ = g.tree.Push(prevMain)
				}
			},
			IsPromptActive: func() bool {
				if g.promptHelp == nil {
					return false
				}
				return g.promptHelp.Active()
			},
		})
	}

	// build the ChangelogController and attach it to its context so its
	// j/k/Enter/Esc/q bindings reach the trie via AllDefaultBindings.
	// Constructed here — not in AttachControllers — because it needs a
	// Pop-capable handle on the focus-stack (*gui.ContextTree).
	if g.registry != nil && g.registry.Changelog != nil && g.tree != nil {
		changelogCtrl := controllers.NewChangelogController(
			g.deps.Common, helperBag.CoreDeps, g.registry.Changelog, g.tree,
		)
		changelogCtrl.SetOnDismiss(func() {
			if g.deps.Store != nil {
				g.deps.Store.StampVersion(g.deps.BuildVersion)
			}
		})
		changelogCtrl.AttachToContext(&g.registry.Changelog.BaseContext)
		g.controllers.Changelog = changelogCtrl
	}

	// Wire the FilePicker context + controller. The FilePickerContext needs
	// afero.Fs for directory listing and ModeSetter for editable input.
	if g.registry != nil && g.registry.FilePicker != nil && g.tree != nil && g.deps.Common != nil {
		g.registry.FilePicker.SetFs(g.deps.Common.Fs)
		g.registry.FilePicker.SetModes(g.keybindingSystem.modeStore)

		filePickerCtrl := controllers.NewFilePickerController(
			g.deps.Common, helperBag.CoreDeps, helperBag.UIDeps,
			func() *guicontext.FilePickerContext {
				return g.registry.FilePicker
			},
		)
		filePickerCtrl.AttachToContext(&g.registry.FilePicker.BaseContext)
		g.controllers.FilePicker = filePickerCtrl
	}
}

// wireEditorCompletion wires the completion engine + the SUGGESTIONS
// overlay context to VimEditorController.
func (g *Gui) wireEditorCompletion() {
	// wire the completion engine + the
	// SUGGESTIONS overlay context to VimEditorController so the
	// `<c-x><c-o>` trigger stops being a silent no-op. SchemaSource and
	// FunctionSource close over the live ConnectHelper session + the
	// SCHEMAS rail's current selection; KeywordsSource is static; the
	// HistorySource pulls from the per-process query.History opened
	// earlier in wireWithDriver. Every source no-ops on nil deps so the
	// popup degrades cleanly before the first Connect.
	if g.controllers != nil && g.controllers.VimEditor != nil && g.registry != nil && g.registry.Suggestions != nil {
		// completion reads a background-warmed metadata
		// snapshot synchronously instead of the live session — no data race, no
		// UI block. The SchemaWarmer owns the store (eager table+function names,
		// lazy per-table columns+FKs), routing every driver call through the
		// ConnectHelper serialized worker. SchemaSource/FunctionSource read the
		// store; SchemaSource fires reactive WarmTable on a column miss; the
		// warm landing re-triggers completion at the cursor (stale-guarded).
		g.schemaWarmer = data.NewConnectSchemaWarmer(
			g.connectHelper,
			g.OnWorker,
			g.OnUIThreadContentOnly,
			g.deps.Common.Logger(),
		)
		// Re-trigger bridge: onWarmed fires on the UI loop; the controller drops
		// it unless the cursor is unchanged and the popup is still open.
		g.schemaWarmer.SetOnWarmed(g.controllers.VimEditor.OnWarmLanded)
		// Surface a THROTTLED toast on warm failure (review Finding: the user
		// should see a metadata-load failure, not only a debug log).
		g.schemaWarmer.SetOnWarmError(func(_, table string, _ error) {
			g.toastWarmError(table)
		})

		store := g.schemaWarmer.Store()
		schemaPicker := schemasPickerAdapter{registry: g.registry.Schemas}
		schemaProv := func() string { return schemaPicker.SelectedSchemaName() }
		// inject the function-signature-help provider seam into
		// the FunctionSource, mirroring the SchemaMetadata/SessionProvider wiring.
		// The editor declares the FunctionDetailProvider interface; the concrete
		// ConnectHelper satisfies it structurally (FunctionDetail sync read +
		// WarmFunctionDetail async warm), so no helpers/data import leaks into the
		// editor. The warm callback must land on the UI loop, so wire the
		// ConnectHelper's UI scheduler + logger here. Signature
		// population/render is handled elsewhere — this only supplies the seam, so the
		// emitted function-name candidates are unchanged.
		fnSource := editor.NewFunctionSource(store)
		if g.connectHelper != nil {
			g.connectHelper.SetUIScheduler(g.OnUIThread)
			g.connectHelper.SetLogger(g.deps.Common.Logger())
			fnSource.SetDetailProvider(g.connectHelper)
		}
		sources := []editor.Source{
			editor.NewSchemaSource(store, g.schemaWarmer, schemaProv),
			fnSource,
			// register the real SnippetSource backed by the
			// built-in starter set (BuiltinSnippetProvider) — replacing the
			// removed placeholder source. NewSnippetSource defaults Priority() to
			// SnippetSourcePriority (the SnippetSourceBias rank const, 50), so the
			// source ranks below schema/function and above keyword/history.
			editor.NewSnippetSource(editor.BuiltinSnippetProvider{}),
			editor.KeywordsSource{PriorityVal: 20},
		}
		if g.queryState.history != nil {
			sources = append(sources, editor.HistorySource{
				Store:       g.queryState.history,
				PriorityVal: 10,
			})
		}
		g.controllers.VimEditor.SetCompletionEngine(editor.NewEngine(sources))
		g.controllers.VimEditor.SetSuggestionsContext(g.registry.Suggestions)
		// feed the SAME signature-help provider + active-schema
		// source into the SUGGESTIONS popup context so the selected function's
		// signature renders as a dedicated help footer with re-render-on-warm.
		// The ConnectHelper's SetUIScheduler (wired above) guarantees the
		// WarmFunctionDetail onReady lands on the UI loop, so the popup's
		// HandleRender re-render is race-free. nil connectHelper => no help line.
		if g.connectHelper != nil {
			g.registry.Suggestions.SetFunctionDetailProvider(g.connectHelper, schemaProv)
		}
		// the SAME warmed store + active-schema provider feed
		// the accept-time ambiguous-column qualifier (it satisfies
		// editor.SchemaMetadata). The store's Columns ok-return is the
		// warmed-or-not signal the qualifier requires before guessing.
		g.controllers.VimEditor.SetSchemaMetadata(store, schemaProv)
	}
}

// warmErrorToastThrottle is the minimum gap between consecutive warm-failure
// toasts, so a burst of failing completion warms does not spam the status line.
const warmErrorToastThrottle = 5 * time.Second

// toastWarmError shows a throttled, user-visible toast when a completion
// metadata warm fails. The error itself is already logged by the warmer; this
// only surfaces a brief hint. No-op when the toast helper is unwired.
func (g *Gui) toastWarmError(table string) {
	if g.toastHelp == nil {
		return
	}
	now := time.Now()
	if !g.lastWarmErrorToast.IsZero() && now.Sub(g.lastWarmErrorToast) < warmErrorToastThrottle {
		return
	}
	g.lastWarmErrorToast = now
	g.toastHelp.Show("completion: could not load metadata for "+table, 3*time.Second)
}
