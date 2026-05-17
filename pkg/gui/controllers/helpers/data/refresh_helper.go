package data

import (
	"context"
)

// RefreshHelper is a thin facade over ConnectHelper.LoadX. Its sole job
// is to give the gui controllers a stable, narrow surface for "I just
// mutated something — please reload this rail" calls. Today the
// implementation is a direct passthrough because the per-Session worker
// queue already serializes the underlying Load calls (T4); future
// epics may add coalescing / cache invalidation here without forcing
// every controller call site to change.
//
// Concurrency: every method delegates to ConnectHelper, which holds the
// per-Session worker queue. Concurrent RefreshX calls from controllers
// are safe; they enqueue and run FIFO against the underlying Session.
//
// `db` for LoadSchemas (M07a) is fixed to the empty string in the
// passthrough: the postgres driver documents-ignores the argument, and
// the gui's "active connection" model is single-database in v1 (per
// DESIGN.md §7 gap-closure #12). When multi-database lands the helper
// will accept the db name via constructor injection or a setter — but
// not now, to keep T7b minimal.
type RefreshHelper struct {
	connect *ConnectHelper
}

// NewRefreshHelper builds a helper bound to the supplied connect helper.
// connect may be nil for unit tests; each Refresh method nil-checks at
// call time and returns nil.
func NewRefreshHelper(connect *ConnectHelper) *RefreshHelper {
	return &RefreshHelper{connect: connect}
}

// RefreshSchemas reloads the SCHEMAS rail data via
// ConnectHelper.LoadSchemas. Returns the load error verbatim.
func (h *RefreshHelper) RefreshSchemas(ctx context.Context) error {
	if h.connect == nil {
		return nil
	}
	_, err := h.connect.LoadSchemas(ctx, "")
	return err
}

// RefreshTables reloads the TABLES rail for the supplied schema.
func (h *RefreshHelper) RefreshTables(ctx context.Context, schema string) error {
	if h.connect == nil {
		return nil
	}
	_, err := h.connect.LoadTables(ctx, schema)
	return err
}

// RefreshColumns reloads the COLUMNS rail for (schema, table).
func (h *RefreshHelper) RefreshColumns(ctx context.Context, schema, table string) error {
	if h.connect == nil {
		return nil
	}
	_, err := h.connect.LoadColumns(ctx, schema, table)
	return err
}

// RefreshIndexes reloads the INDEXES rail for (schema, table).
func (h *RefreshHelper) RefreshIndexes(ctx context.Context, schema, table string) error {
	if h.connect == nil {
		return nil
	}
	_, err := h.connect.LoadIndexes(ctx, schema, table)
	return err
}
