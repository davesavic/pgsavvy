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
	if a.g.activeSQLSession == nil {
		return nil, errors.New("fk forward: no active session")
	}
	fkc := a.g.activeSQLSession.FKCache()
	if fkc == nil {
		return nil, errors.New("fk forward: active session has no fk cache")
	}
	return fkc.Get(ctx, schema, table)
}
