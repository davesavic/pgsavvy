package orchestrator

import (
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// wireEditDeps builds the EditDeps inline-edit helper bundle
// (dbsavvy-bwq.py4). All optional.
func (g *Gui) wireEditDeps() controllers.EditDeps {
	return controllers.EditDeps{
		PendingDiscard: g.pendingDiscardH,
		JumpList:       g.jumpListH,
		FKForward:      g.fkForwardH,
		PendingEditSet: g.pendingEditSet,
		// gD picker open — resolves through g.controllers at dispatch
		// time so the closure works despite the controllers aggregate
		// being filled in AttachControllers AFTER this HelperBag is
		// composed. dbsavvy-8oo stub #2.
		OpenFKReversePicker: func(entries []controllers.ReverseEntry, origin controllers.FKReverseOriginTab, row, col int) bool {
			if g.controllers.FKReversePicker == nil {
				return false
			}
			return g.controllers.FKReversePicker.Open(entries, origin, row, col)
		},
		// Reverse-FK resolver — routes each lookup through the active
		// SQLSession's FKCache so per-Connect rotation is invisible to
		// the picker handler (dbsavvy-8oo stub #2). Extracted to a named
		// method so the parked-stream preempt is regression-testable
		// (dbsavvy-lxn.4), symmetric with the forward adapter.
		ReverseFKLookup: g.lookupReverseFK,
		// ActivePendingEditSet resolves the per-(connID, baseTable) set
		// from the registry using the currently-active tab's identity.
		// Returns nil when no tab is active OR the tab has no row
		// identity (non-table-backed result). dbsavvy-8oo stub #10 / #3.
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
					// the table's schema (dbsavvy-8q6).
					if set.Table.Schema == "" {
						if sch := gv.IdentitySchema(); sch != "" {
							set.Table.Schema = sch
						}
					}
					// Point the grid at this exact set so the dirty-cell
					// renderer shows staged values. The pointer is stable per
					// (connID, baseTable) and the cell editor stages into the
					// same instance via this resolver, so a staged edit
					// reflects on the next render (dbsavvy-cyh).
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
	// dbsavvy-1rf: wire the CONNECTION_MANAGER modal's list + in-modal-connect
	// closures now that both the modal context and connectInvoker exist. The
	// handlers run on the MainLoop (keybinding dispatch), so the connectInvoker
	// gen seams stay serialised. The connect lifecycle renders INSIDE the modal
	// (no standalone CONNECTING push); a successful publish pops the modal.
	if g.controllers != nil && g.controllers.ConnectionManager != nil && g.registry != nil && g.registry.ConnectionManager != nil {
		// Populate rows + restore the last-used cursor each time the modal
		// gains focus (dbsavvy-1rf).
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
			// dbsavvy-dyf: add/edit form wiring. Prompt drives the per-field
			// PROMPT popup; ExistingNames + DriversFn back validation + the
			// driver selector. dbsavvy-zod: OnSaveConnection persists the
			// validated profile (append for add, update for edit) and refreshes
			// the modal list from disk. The full conn carries the
			// form-untouched fields (Password, SSHTunnel, …) so the rewrite
			// preserves them.
			Prompt:             g.promptHelp,
			ExistingNames:      g.connectionNames,
			DriversFn:          drivers.Names,
			OnSaveConnection:   g.saveConnectionForm,
			OnDeleteConnection: g.deleteConnectionFromModal,
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
		// drop (dbsavvy-7k9, dbsavvy-f5t).
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
	// dbsavvy-2ttm: the SEARCH_LINE search input gets the same caret
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

	// dbsavvy-3vf.9: build the TABLE_INSPECT popup controller and attach
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

	// dbsavvy-o9k0.5: build the HISTORY popup controller and attach it to
	// its context so its j/k/gg/G/<cr>/<esc> bindings reach the trie via
	// AllDefaultBindings. Constructed here — not in AttachControllers —
	// because it needs a Pop-capable handle on the focus-stack
	// (*gui.ContextTree). refocus is nil: ContextTree.Pop already fires
	// HandleFocus on the new stack top (the query editor), so no explicit
	// focus call is needed after the <cr> insert pops the popup (mirrors
	// the FKReversePicker / TableInspect close path which rely on Pop
	// alone). The editor buffer adapter receives the inserted SQL.
	if g.registry != nil && g.registry.History != nil && g.tree != nil {
		historyCtx := g.registry.History
		historyCtrl := controllers.NewHistoryController(
			g.deps.Common, helperBag.CoreDeps, historyCtx,
			newEditorBufferAdapter(g.registry.QueryEditor), g.tree, nil,
		)
		historyCtrl.AttachToContext(&historyCtx.BaseContext)
		g.controllers.History = historyCtrl
	}

	// dbsavvy-bwq.Z1: build the CHEATSHEET popup controller and attach it
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
}

// wireEditorCompletion wires the completion engine + the SUGGESTIONS
// overlay context to VimEditorController.
func (g *Gui) wireEditorCompletion() {
	// dbsavvy-qsb / dbsavvy-8oo #8: wire the completion engine + the
	// SUGGESTIONS overlay context to VimEditorController so the
	// `<c-x><c-o>` trigger stops being a silent no-op. SchemaSource and
	// FunctionSource close over the live ConnectHelper session + the
	// SCHEMAS rail's current selection; KeywordsSource is static; the
	// HistorySource pulls from the per-process query.History opened
	// earlier in wireWithDriver. Every source no-ops on nil deps so the
	// popup degrades cleanly before the first Connect.
	if g.controllers != nil && g.controllers.VimEditor != nil && g.registry != nil && g.registry.Suggestions != nil {
		// dbsavvy-ko4m.2.3: completion reads a background-warmed metadata
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
		// dbsavvy-ko4m.5.3: inject the function-signature-help provider seam into
		// the FunctionSource, mirroring the SchemaMetadata/SessionProvider wiring.
		// The editor declares the FunctionDetailProvider interface; the concrete
		// ConnectHelper satisfies it structurally (FunctionDetail sync read +
		// WarmFunctionDetail async warm), so no helpers/data import leaks into the
		// editor. The warm callback must land on the UI loop, so wire the
		// ConnectHelper's UI scheduler + logger here (ko4m.5.2 handoff). Signature
		// population/render is ko4m.5.4 — this only supplies the seam, so the
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
			// dbsavvy-ko4m.7.3: register the real SnippetSource backed by the
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
		// dbsavvy-ko4m.5.4: feed the SAME signature-help provider + active-schema
		// source into the SUGGESTIONS popup context so the selected function's
		// signature renders as a dedicated help footer with re-render-on-warm.
		// The ConnectHelper's SetUIScheduler (wired above) guarantees the
		// WarmFunctionDetail onReady lands on the UI loop, so the popup's
		// HandleRender re-render is race-free. nil connectHelper => no help line.
		if g.connectHelper != nil {
			g.registry.Suggestions.SetFunctionDetailProvider(g.connectHelper, schemaProv)
		}
		// dbsavvy-ko4m.6.3: the SAME warmed store + active-schema provider feed
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
