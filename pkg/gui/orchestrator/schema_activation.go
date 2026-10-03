package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/davesavic/pgsavvy/pkg/common"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

type schemaActivationRequest struct {
	cancel context.CancelFunc
}

func (g *Gui) cancelSchemaActivation() {
	g.schemaActivationMu.Lock()
	r := g.schemaActivationRequest
	g.schemaActivationRequest = nil
	g.schemaActivationMu.Unlock()
	if r != nil {
		r.cancel()
	}
}

// Capture MainLoop-owned state before dispatch; only guarded publication may
// change the rails or run focus hooks.
func (g *Gui) activateSchema(inv *connectInvoker, schema string) {
	if g.closed.Load() || inv == nil || inv.helper == nil {
		return
	}
	g.cancelSchemaActivation()
	ctx, cancel := context.WithCancel(context.Background())
	ctx, sessionCurrent := inv.helper.BindSessionContext(ctx)
	request := &schemaActivationRequest{cancel: cancel}
	g.schemaActivationMu.Lock()
	g.schemaActivationRequest = request
	g.schemaActivationMu.Unlock()
	gen := g.connectionState.connectGen.Load()
	sess, connID, warmer := g.queryState.activeSQLSession, g.connectionState.activeConnID, g.schemaWarmer
	registry, schemasHelper := g.registry, g.schemasHelper
	current := func() bool {
		g.schemaActivationMu.Lock()
		owned := g.schemaActivationRequest == request
		g.schemaActivationMu.Unlock()
		return owned && !g.closed.Load() && ctx.Err() == nil && g.connectionState.connectGen.Load() == gen && sessionCurrent()
	}
	if g.deps.Store != nil && connID != "" {
		g.deps.Store.SetLastSchemaName(connID, schema)
	}
	g.OnWorker(func(gocui.Task) error {
		// A superseded SET must settle before the new activation's SET, even
		// when a driver returns success despite cancellation.
		g.schemaActivationRunMu.Lock()
		defer g.schemaActivationRunMu.Unlock()
		if !current() {
			cancel()
			return nil
		}
		var setErr, schemaErr, tableErr error
		var schemaItems, tableItems []any
		if sess != nil {
			sql, value := schemaSearchPathSQL(schema)
			_, setErr = sess.Execute(ctx, models.Query{SQL: sql})
			if !current() {
				cancel()
				return nil
			}
			if setErr == nil {
				sess.SettingsSnapshot().Set("search_path", value)
				if g.deps.Store != nil && connID != "" {
					g.deps.Store.MutateAndSave(func(a *common.AppState) {
						if a.LastSessionSettings == nil {
							a.LastSessionSettings = make(map[string]map[string]string)
						}
						if a.LastSessionSettings[connID] == nil {
							a.LastSessionSettings[connID] = make(map[string]string)
						}
						a.LastSessionSettings[connID]["search_path"] = value
					})
				}
				if cache := sess.FKCache(); cache != nil {
					cache.InvalidateAll()
				}
				var schemas []models.Schema
				schemas, schemaErr = inv.helper.LoadSchemas(ctx, "")
				if schemaErr == nil {
					if schemasHelper != nil {
						builtin, profile := defaultHiddenPatterns()
						schemas, _ = schemasHelper.FilterHidden(schemas, builtin, profile, nil)
					}
					schemaItems = make([]any, len(schemas))
					for i := range schemas {
						schemaItems[i] = schemas[i]
					}
				}
			}
		}
		if !current() {
			cancel()
			return nil
		}
		var tables []*models.Table
		tables, tableErr = inv.helper.LoadTables(ctx, schema)
		if tableErr == nil {
			tableItems = make([]any, len(tables))
			for i := range tables {
				tableItems[i] = tables[i]
			}
		}
		if warmer != nil && current() {
			warmer.LoadEagerContext(ctx, schema)
		}
		g.OnUIThread(func() error {
			defer cancel()
			if !current() || g.queryState.activeSQLSession != sess || g.connectionState.activeConnID != connID {
				return nil
			}
			if setErr != nil {
				logs.Event(g.deps.Common.Logger(), "gui", "schema_search_path_failed",
					slog.String("schema", schema), slog.String("err", setErr.Error()))
			}
			if schemaErr != nil {
				g.deps.Common.Logger().Warn("gui: load schemas after connect", "err", schemaErr)
			}
			if schemaItems != nil {
				inv.publishSchemaItems(schemaItems, true)
			}
			if tableErr != nil {
				g.deps.Common.Logger().Warn(fmt.Sprintf("gui: load tables for schema %q: %v", schema, tableErr))
			} else if registry.Tables != nil {
				registry.Tables.SetItems(tableItems)
			}
			if registry.SchemaRail != nil {
				registry.SchemaRail.SetActiveTab(guicontext.SchemaRailTabTables)
				return g.tree.Push(registry.SchemaRail)
			}
			return nil
		})
		return nil
	})
}
