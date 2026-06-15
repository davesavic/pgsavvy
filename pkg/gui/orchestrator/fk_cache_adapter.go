package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// relationshipPreviewTimeout bounds each outbound display-value lookup. Short
// + server-cancelled (statement-timeout maps to context.WithTimeout in
// Session.Execute) so a slow parent table never wedges the panel fill.
const relationshipPreviewTimeout = 2 * time.Second

// activeSessionFKCacheAdapter resolves the per-Connection FKCache from
// *Gui.activeSQLSession on every Get call. The FKForwardHelper holds a
// single Cache reference for its lifetime, but the underlying session
// changes on every Connect; the adapter routes each lookup through the
// session currently bound to the active connection.
//
// Returns a descriptive error when no session is active so the helper
// surfaces an actionable toast rather than a nil-deref.
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
	// driver session: on a cache miss fkc.Get runs
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
// inFlight guard. Nil-safe; a no-op when no stream is in
// flight. See ResultTabsHelper.PreemptInFlight for the stop semantics
// (it blocks until the worker has closed its stream and released the
// guard, so the loader's acquireInFlight cannot panic afterwards).
func (g *Gui) preemptForFKCacheLoad() {
	if g == nil || g.resultTabsH == nil {
		return
	}
	g.resultTabsH.PreemptInFlight()
}

// lookupForwardFK resolves outbound foreign keys for (schema, table)
// through the active session's FKCache, wired as the relationship panel's
// forward-FK source. Mirrors lookupReverseFK's preempt + nil-session
// handling. The cache returns metadata only (constraint definitions); the
// panel reads the focused row's own cell values for the displayed FK
// values, so this issues no per-row data queries.
func (g *Gui) lookupForwardFK(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	if g == nil || g.queryState.activeSQLSession == nil {
		return nil, errors.New("no active session")
	}
	fkc := g.queryState.activeSQLSession.FKCache()
	if fkc == nil {
		return nil, errors.New("active session has no fk cache")
	}
	g.preemptForFKCacheLoad()
	return fkc.Get(ctx, schema, table)
}

// lookupReverseFK resolves inbound foreign keys for (schema, table) through
// the active session's FKCache, wired as the gD reverse-picker's
// ReverseFKLookup. Like the forward gd path it preempts any parked stream
// before the loader runs: GetReverse's loader calls
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

// resolveRelationshipPreview resolves the parent row's display-column value
// for one outbound FK, wired as the relationship panel's preview source.
// Mirrors the EstimateRows / IntrospectEditability closures
// (wire_result_tabs.go): resolve the live connection at call time, acquire a
// FRESH POOLED session, and run the pg display-value resolver under a short
// timeout. A fresh session never preempts the user's in-flight stream, so —
// unlike the FK-cache lookups above — this path does NOT preempt. Non-pg
// drivers / no connection yield a nil value with no error (raw fallback).
func (g *Gui) resolveRelationshipPreview(ctx context.Context, fk models.ForeignKey, refValues []any) (any, error) {
	if g == nil || g.connectHelper == nil {
		return nil, errors.New("relationship preview: no connect helper")
	}
	conn := g.connectHelper.Connection()
	if conn == nil {
		return nil, errors.New("relationship preview: no active connection")
	}
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()

	pgSess, ok := sess.(*pg.Session)
	if !ok {
		return nil, nil // non-pg driver: no preview yet (raw fallback)
	}
	return pg.ResolveDisplayValue(ctx, pgSess, fk, refValues, relationshipPreviewTimeout)
}

// relationshipEstimateTimeout bounds each inbound predicated-estimate lookup.
// Shorter than the preview timeout: the planner-only EXPLAIN is cheap, and a
// child table whose planner is slow should not stall the panel fill.
const relationshipEstimateTimeout = 500 * time.Millisecond

// resolveRelationshipEstimate resolves the planner's row estimate for the
// inbound children of the focused row through one inbound FK, wired as the
// relationship panel's estimate source. Mirrors resolveRelationshipPreview:
// resolve the live connection at call time, acquire a FRESH POOLED session, and
// run the predicated planner-only EXPLAIN under a short timeout. A fresh
// session never preempts the user's in-flight stream, so this path does NOT
// preempt. Non-pg drivers / no connection yield (0, nil).
func (g *Gui) resolveRelationshipEstimate(ctx context.Context, fk models.ForeignKey, refValues []any) (int64, error) {
	if g == nil || g.connectHelper == nil {
		return 0, errors.New("relationship estimate: no connect helper")
	}
	conn := g.connectHelper.Connection()
	if conn == nil {
		return 0, errors.New("relationship estimate: no active connection")
	}
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = sess.Close() }()

	pgSess, ok := sess.(*pg.Session)
	if !ok {
		return 0, nil // non-pg driver: no estimate yet
	}
	return pg.EstimatePredicatedRows(ctx, pgSess, fk, refValues, relationshipEstimateTimeout)
}

// relationshipExactCountTimeout bounds each on-demand inbound exact COUNT(*).
// Longer than the planner estimate (a real COUNT scans), but short enough that a
// slow child table falls back to the ~estimate instead of stalling: a timeout is
// classified by the controller (errors.Is DeadlineExceeded) and keeps the
// estimate. Frozen decision: 750ms.
const relationshipExactCountTimeout = 750 * time.Millisecond

// evictRelationshipCounts drops the relationship panel's cached inbound counts
// (exact + estimate) for child table (schema, table) after a DML commit. It
// resolves the panel controller LAZILY (wired after the commit dialog) and is
// nil-safe at every hop, so a commit succeeds whether or not the panel exists.
// Also drops the session FKCache's reverse entry for that table per AC, though
// that only matters across DDL — FK metadata is unchanged by DML.
func (g *Gui) evictRelationshipCounts(schema, table string) {
	if g == nil {
		return
	}
	if g.controllers != nil && g.controllers.RelationshipPanel != nil {
		g.controllers.RelationshipPanel.EvictExactCounts(schema, table)
	}
	if g.queryState.activeSQLSession != nil {
		if fkc := g.queryState.activeSQLSession.FKCache(); fkc != nil {
			fkc.InvalidateReverse(schema, table)
		}
	}
}

// resolveRelationshipExact resolves the EXACT inbound child count for one
// focused inbound FK through a FRESH POOLED session, wired as the relationship
// panel's exact-count source. Mirrors resolveRelationshipEstimate: resolve the
// live connection at call time, acquire a fresh session, and run the predicated
// COUNT(*) under the 750ms timeout. A fresh session never preempts the user's
// in-flight stream, so this path does NOT preempt (ErrPreemptPending is N/A).
// Non-pg drivers / no connection yield (0, nil).
func (g *Gui) resolveRelationshipExact(ctx context.Context, fk models.ForeignKey, refValues []any) (int64, error) {
	if g == nil || g.connectHelper == nil {
		return 0, errors.New("relationship exact count: no connect helper")
	}
	conn := g.connectHelper.Connection()
	if conn == nil {
		return 0, errors.New("relationship exact count: no active connection")
	}
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = sess.Close() }()

	pgSess, ok := sess.(*pg.Session)
	if !ok {
		return 0, nil // non-pg driver: no exact count yet
	}
	return pg.CountPredicatedRows(ctx, pgSess, fk, refValues, relationshipExactCountTimeout)
}
