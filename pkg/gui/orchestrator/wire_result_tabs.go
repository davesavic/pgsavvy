package orchestrator

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/tasks"
)

// wireResultTabs builds the ResultTabsHelper and the wiring that depends on it:
// the QueryRunner preempter, the tab lifecycle callbacks, and the FK-forward
// helper. Extracted from wireWithDriver (dbsavvy-y5th.1.2); it must run after
// g.queryRunner and g.jumpListH exist.
func (g *Gui) wireResultTabs(tr *i18n.TranslationSet) {
	// ResultTabsHelper owns the multi-tab pane in the secondary slot.
	// Each tab gets its own ResultBufferManager built against the
	// orchestrator's threading helpers. dbsavvy-66p.12.
	resultTabsDeps := ui.ResultTabsHelperDeps{
		Driver:     g.driver,
		Toast:      g.toastHelp,
		Confirm:    g.confirmHelp,
		Search:     g.searchLineHelp,
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
	// dbsavvy-s8y (Gap 2b): production editability introspection. Resolve
	// the live connection at call time (it is invalidated on Disconnect),
	// acquire a fresh pooled session, and run the pg introspector. Non-pg
	// drivers or no connection leave editability off.
	resultTabsDeps.IntrospectEditability = func(ctx context.Context, cols []models.ColumnMeta) (bool, []int, string, string) {
		if g.connectHelper == nil {
			return false, nil, "", ""
		}
		conn := g.connectHelper.Connection()
		if conn == nil {
			return false, nil, "", ""
		}
		sess, err := conn.AcquireSession(ctx)
		if err != nil {
			return false, nil, "", ""
		}
		defer func() { _ = sess.Close() }()

		pgSess, ok := sess.(*pg.Session)
		if !ok {
			return false, nil, "", "" // non-pg driver: no introspection yet
		}
		baseRelation, rowID, reason, err := pg.EditabilityIntrospect(ctx, pgSess, cols)
		if err != nil {
			// reason already carries the canonical "introspection failed: …"
			// string (editability.go); don't re-prefix it.
			return false, nil, reason, ""
		}
		editable := reason == ""

		readOnly := false
		if p := g.connectionState.activeConnProfile; p != nil {
			readOnly = p.ReadOnly
		}
		editable, reason = pg.ApplyConnectionGate(editable, reason, readOnly, true /* pg SupportsInlineEdit */)
		// baseRelation.Schema is the catalog-resolved schema (pg_namespace.
		// nspname). Thread it out so the apply path can schema-qualify the
		// UPDATE; the SQL-parsed base table is unqualified for a bare
		// `SELECT ... FROM tbl` (dbsavvy-8q6).
		return editable, rowID, reason, baseRelation.Schema
	}
	// Lazy OID->relname resolution for the hide-cols overlay. Mirrors the
	// editability closure: resolve the live connection at call time, acquire
	// a fresh session, run the pg catalog lookup. Non-pg drivers / no
	// connection yield a nil map, leaving the overlay labels bare.
	resultTabsDeps.ResolveTableNames = func(ctx context.Context, oids []uint32) (map[uint32]string, error) {
		if g.connectHelper == nil {
			return nil, nil
		}
		conn := g.connectHelper.Connection()
		if conn == nil {
			return nil, nil
		}
		sess, err := conn.AcquireSession(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = sess.Close() }()

		pgSess, ok := sess.(*pg.Session)
		if !ok {
			return nil, nil // non-pg driver: no resolution yet
		}
		return pg.TableNamesFromOIDs(ctx, pgSess, oids)
	}
	if tr != nil {
		resultTabsDeps.SortPickLabel = tr.Actions.ResultSortPickLabel
	}
	if cfg := g.deps.Common.Cfg(); cfg != nil {
		resultTabsDeps.ResultPageSize = cfg.UI.ResultPageSize
		resultTabsDeps.ReadToEndWarnThreshold = cfg.UI.ReadToEndWarnThreshold
		resultTabsDeps.MouseDoubleClickMs = cfg.UI.Mouse.DoubleClickMs
		resultTabsDeps.ExportBufferedRowWarnThreshold = cfg.UI.Export.BufferedRowWarnThreshold
		resultTabsDeps.ExportClipboardMaxBytes = cfg.UI.Export.ClipboardMaxBytes
	}
	g.resultTabsH = ui.NewResultTabsHelper(resultTabsDeps)
	if g.deps.Common != nil {
		g.resultTabsH.SetLogger(g.deps.Common.Logger())
	}

	// Centralize last-wins preemption at the QueryRunner chokepoint: every
	// Run / RunQuery / Explain stops any parked >200-row stream before it
	// acquires the per-session queue lock, so no synchronous session op on
	// the UI goroutine can freeze the TUI (dbsavvy-lxn.1). Set on the runner
	// itself so it survives Bind / Unbind on reconnect.
	g.queryRunner.SetPreempter(g.resultTabsH.PreemptInFlight)

	// dbsavvy-bwq.15: prune jump entries belonging to a closed result
	// tab so <c-o>/<c-i> never resurface stale references. Wired after
	// both helpers exist; ResultTabsHelper invokes the callback during
	// tab removal on the UI thread.
	if g.resultTabsH != nil {
		g.resultTabsH.SetOnTabRemoved(func(tabID string) {
			g.jumpListH.PruneByTab(tabID)
		})
		// dbsavvy-aqw: when the user closes the focused result tab
		// (<leader>X), its MAIN_CONTEXT is still on top of the focus
		// stack pointing at a now-deleted view, so no panel renders as
		// focused. Reconcile by shifting focus to the new active tab, or
		// to the query editor when no tabs remain. Both are MAIN_CONTEXTs,
		// so Push replaces the stale top via removeMain. Runs on the main
		// loop (CloseActive is dispatched from the keybinding handler).
		g.resultTabsH.SetOnActiveClosed(func() {
			if g.tree == nil {
				return
			}
			if next := g.resultTabsH.ActiveContext(); next != nil {
				_ = g.tree.Push(next)
				return
			}
			if g.registry != nil && g.registry.QueryEditor != nil {
				_ = g.tree.Push(g.registry.QueryEditor)
			}
		})
		// dbsavvy-pc4k: a user-initiated tab switch (gt/gT cycle, <leader>1..9
		// jump) moves the active tab but not the focus stack, so gocui's
		// current-view (RunLayout reads tree.Current().GetViewName()) stays on
		// the prior tab's view and leader chords dispatch under the stale scope
		// (e.g. PLAN instead of RESULT_GRID). Re-point the stack onto the new
		// active tab's view. Replace (not Push) because grid->grid switches
		// share the RESULT_GRID key and Push no-ops on a key match. Guarded to
		// the case where the result pane already holds focus: <leader> jumps are
		// GLOBAL-scoped and must not steal focus from the query editor or a rail.
		g.resultTabsH.SetOnActiveChanged(func() {
			if g.tree == nil {
				return
			}
			top := g.tree.Current()
			if top == nil || (top.GetKey() != types.RESULT_GRID && top.GetKey() != types.PLAN) {
				return
			}
			if next := g.resultTabsH.ActiveContext(); next != nil {
				_ = g.tree.Replace(next)
			}
		})
	}
	// FKForwardHelper drives `gd` forward FK navigation. Cache routes
	// each Get through activeSessionFKCacheAdapter so per-Connect
	// FKCache rotation is invisible to the helper. BusyChecker remains
	// nil and is unused: with last-wins (dbsavvy-lxn.1) gd preempts any
	// parked prior stream at the QueryRunner chokepoint rather than
	// queueing, so the helper no longer branches on session busyness.
	g.fkForwardH = helpers.NewFKForwardHelper(helpers.FKForwardDeps{
		Cache:    &activeSessionFKCacheAdapter{g: g},
		JumpList: g.jumpListH,
		Runner:   g.queryRunner,
		Tabs:     g.resultTabsH,
		Toast:    g.toastHelp,
		Busy:     nil,
		Limit:    0,
	})
}
