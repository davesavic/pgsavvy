package orchestrator

import (
	"context"
	"errors"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// activeSessionFKCacheAdapter resolves the per-Connection FKCache from
// *Gui.activeSQLSession on every Get call. The FKForwardHelper holds a
// single Cache reference for its lifetime, but the underlying session
// changes on every Connect; the adapter routes each lookup through the
// session currently bound to the active connection.
//
// Returns a descriptive error when no session is active so the helper
// surfaces an actionable toast rather than a nil-deref. dbsavvy-8oo
// stub #1.
type activeSessionFKCacheAdapter struct {
	g *Gui
}

// Get satisfies helpers.FKCache. When activeSQLSession is nil (no
// active connection) it returns an error rather than synthesising an
// empty FK list so the FKForward helper's "no FK on cursor column"
// branch isn't conflated with "we couldn't look up the FK".
func (a *activeSessionFKCacheAdapter) Get(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	if a == nil || a.g == nil {
		return nil, errors.New("fk forward: cache adapter not bound to gui")
	}
	if a.g.queryState.activeSQLSession == nil {
		return nil, errors.New("fk forward: no active session")
	}
	fkc := a.g.queryState.activeSQLSession.FKCache()
	if fkc == nil {
		return nil, errors.New("fk forward: active session has no fk cache")
	}
	// Preempt any parked stream before the loader touches the shared
	// driver session (dbsavvy-lxn.4): on a cache miss fkc.Get runs
	// ListForeignKeys on the SAME driver session a >initial-fill parked
	// stream still holds via its inFlight guard, so acquireInFlight would
	// panic "session: concurrent use". Mirrors the QueryRunner chokepoint
	// preempt (last-wins) so gd preempts the parked stream rather than
	// racing its conn.
	a.g.preemptForFKCacheLoad()
	return fkc.Get(ctx, schema, table)
}

// preemptForFKCacheLoad stops any in-flight/parked result-tab stream so a
// synchronous FK-cache loader read does not race the driver session's
// inFlight guard (dbsavvy-lxn.4). Nil-safe; a no-op when no stream is in
// flight. See ResultTabsHelper.PreemptInFlight for the stop semantics
// (it blocks until the worker has closed its stream and released the
// guard, so the loader's acquireInFlight cannot panic afterwards).
func (g *Gui) preemptForFKCacheLoad() {
	if g == nil || g.resultTabsH == nil {
		return
	}
	g.resultTabsH.PreemptInFlight()
}

// lookupReverseFK resolves inbound foreign keys for (schema, table) through
// the active session's FKCache, wired as the gD reverse-picker's
// ReverseFKLookup. Like the forward gd path it preempts any parked stream
// before the loader runs (dbsavvy-lxn.4): GetReverse's loader calls
// ListInboundForeignKeys on the SAME driver session a parked stream holds
// via its inFlight guard, which would otherwise panic "session: concurrent
// use". Same last-wins rationale as activeSessionFKCacheAdapter.Get.
func (g *Gui) lookupReverseFK(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	if g == nil || g.queryState.activeSQLSession == nil {
		return nil, errors.New("no active session")
	}
	fkc := g.queryState.activeSQLSession.FKCache()
	if fkc == nil {
		return nil, errors.New("active session has no fk cache")
	}
	g.preemptForFKCacheLoad()
	return fkc.GetReverse(ctx, schema, table)
}
