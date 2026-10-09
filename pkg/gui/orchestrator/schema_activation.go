package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

type schemaActivationRequest struct {
	cancel context.CancelFunc
}

func (g *Gui) cancelSchemaActivation() {
	// Activation and refresh both publish the same rails. Lifecycle changes
	// must revoke queued refresh publications as well as in-flight loads.
	g.cancelSchemaRefreshes()
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
			owned := func() bool {
				g.settingsRunMu.Lock()
				defer g.settingsRunMu.Unlock()
				if !current() {
					return false
				}
				sql, _ := schemaSearchPathSQL(schema)
				_, setErr = sess.Execute(ctx, models.Query{SQL: sql})
				if !current() {
					return false
				}
				if setErr == nil {
					names := []string{schema}
					if schema != "public" {
						names = append(names, "public")
					}
					if err := g.persistTrackedSetting(sess, connID, "search_path", decodedSetting(names...)); err != nil {
						g.toaster("schema search_path applied, not persisted: state rejected the setting")
					}
				}
				return true
			}()
			if !owned {
				cancel()
				return nil
			}
			if setErr == nil {
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
			if schemaItems != nil && g.schemaRefreshCurrent(guicontext.SchemaRailTabSchemas, nil) {
				inv.publishSchemaItems(schemaItems, true)
			}
			if tableErr != nil {
				g.deps.Common.Logger().Warn(fmt.Sprintf("gui: load tables for schema %q: %v", schema, tableErr))
			} else if registry.Tables != nil && g.schemaRefreshCurrent(guicontext.SchemaRailTabTables, nil) {
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
