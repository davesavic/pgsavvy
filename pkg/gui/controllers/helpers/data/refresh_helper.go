package data

import (
	"context"
)

// RefreshHelper is the controller-facing surface for "reload rail X
// from the live driver and push the result back into the rail's
// context". It carries one closure per rail; each closure encapsulates
// the Load + filter + SetItems sequence already implemented by the
// orchestrator's populateXxxRail helpers. The split keeps the populate
// logic in one place (orchestrator/adapters.go) while letting any
// controller trigger a refresh without an import cycle through
// pkg/gui/orchestrator.
//
// All fields are optional: a nil closure or a nil receiver collapses
// the corresponding RefreshXxx call to a silent no-op so early-boot
// wiring order (helpers built before the orchestrator finishes wiring
// closures) never panics the controllers. dbsavvy-56u.1.
type RefreshHelper struct {
	refreshSchemas     func(ctx context.Context) error
	refreshTables      func(ctx context.Context, schema string) error
	refreshColumns     func(ctx context.Context, schema, table string) error
	refreshIndexes     func(ctx context.Context, schema, table string) error
	refreshConnections func() error
}

// NewRefreshHelper builds a helper with nil closures. The orchestrator
// fills the closures via the SetXxx setters once it has constructed
// the connectInvoker (which owns populateXxxRail) and the side-rail
// contexts. Tests may leave closures unset; every RefreshXxx method
// nil-checks at call time.
func NewRefreshHelper() *RefreshHelper {
	return &RefreshHelper{}
}

// SetSchemasRefresher wires the SCHEMAS-rail reload closure.
func (h *RefreshHelper) SetSchemasRefresher(fn func(ctx context.Context) error) {
	if h == nil {
		return
	}
	h.refreshSchemas = fn
}

// SetTablesRefresher wires the TABLES-rail reload closure.
func (h *RefreshHelper) SetTablesRefresher(fn func(ctx context.Context, schema string) error) {
	if h == nil {
		return
	}
	h.refreshTables = fn
}

// SetColumnsRefresher wires the COLUMNS-rail reload closure.
func (h *RefreshHelper) SetColumnsRefresher(fn func(ctx context.Context, schema, table string) error) {
	if h == nil {
		return
	}
	h.refreshColumns = fn
}

// SetIndexesRefresher wires the INDEXES-rail reload closure.
func (h *RefreshHelper) SetIndexesRefresher(fn func(ctx context.Context, schema, table string) error) {
	if h == nil {
		return
	}
	h.refreshIndexes = fn
}

// SetConnectionsRefresher wires the CONNECTIONS-rail reload closure.
// CONNECTIONS has no context argument — the orchestrator's
// refreshConnectionsRail re-reads from the on-disk connection
// provider directly.
func (h *RefreshHelper) SetConnectionsRefresher(fn func() error) {
	if h == nil {
		return
	}
	h.refreshConnections = fn
}

// RefreshSchemas reloads the SCHEMAS rail data and pushes it back into
// SchemasContext. Returns nil when the closure is unwired.
func (h *RefreshHelper) RefreshSchemas(ctx context.Context) error {
	if h == nil || h.refreshSchemas == nil {
		return nil
	}
	return h.refreshSchemas(ctx)
}

// RefreshTables reloads the TABLES rail for schema. The closure
// applies a stale-guard against the rail's currently-selected schema
// before pushing.
func (h *RefreshHelper) RefreshTables(ctx context.Context, schema string) error {
	if h == nil || h.refreshTables == nil {
		return nil
	}
	return h.refreshTables(ctx, schema)
}

// RefreshColumns reloads the COLUMNS rail for (schema, table). The
// closure applies a stale-guard against the rail's currently-selected
// table before pushing.
func (h *RefreshHelper) RefreshColumns(ctx context.Context, schema, table string) error {
	if h == nil || h.refreshColumns == nil {
		return nil
	}
	return h.refreshColumns(ctx, schema, table)
}

// RefreshIndexes reloads the INDEXES rail for (schema, table). The
// closure applies a stale-guard against the rail's currently-selected
// table before pushing.
func (h *RefreshHelper) RefreshIndexes(ctx context.Context, schema, table string) error {
	if h == nil || h.refreshIndexes == nil {
		return nil
	}
	return h.refreshIndexes(ctx, schema, table)
}

// RefreshConnections reloads the CONNECTIONS rail from the on-disk
// connection provider.
func (h *RefreshHelper) RefreshConnections() error {
	if h == nil || h.refreshConnections == nil {
		return nil
	}
	return h.refreshConnections()
}
