package orchestrator

import (
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// SetSchemaLoadTimeoutForTest shrinks (and returns a restorer for) the
// schema-load timeout budget so the staged-progress timeout test can exercise
// the Objects-✗ path deterministically without waiting the production budget.
func SetSchemaLoadTimeoutForTest(d time.Duration) (restore func()) {
	prev := schemaLoadTimeout
	schemaLoadTimeout = d
	return func() { schemaLoadTimeout = prev }
}

// HistoryStoreForTest returns the *query.History the most recent
// wireWithDriver pass opened, or nil if the store failed to open. Test-only
// seam (compiled only under `go test`) so external orchestrator_test cases can
// record through the REAL Record/write path the HistoryOpen worker reads from
// via Recent(), exercising the populated worker path end-to-end.
func (g *Gui) HistoryStoreForTest() *query.History {
	if g == nil {
		return nil
	}
	return g.queryState.history
}

// SearchPathSetRunnerForTest exposes the SetRunner wired onto the SearchPath
// controller. Guards the setExHandler triple-use (dbsavvy-y5th.1.1): the :set
// ex-command, SearchPath.SetRunner, and StatementTimeout.SetRunner must all
// route to g.handleSetEx after wireWithDriver.
func (g *Gui) SearchPathSetRunnerForTest() func([]string, commands.ExecCtx) error {
	if g == nil || g.controllers == nil || g.controllers.SearchPath == nil {
		return nil
	}
	return g.controllers.SearchPath.SetRunner
}

// StatementTimeoutSetRunnerForTest exposes the SetRunner wired onto the
// StatementTimeout controller. See SearchPathSetRunnerForTest.
func (g *Gui) StatementTimeoutSetRunnerForTest() func([]string, commands.ExecCtx) error {
	if g == nil || g.controllers == nil || g.controllers.StatementTimeout == nil {
		return nil
	}
	return g.controllers.StatementTimeout.SetRunner
}
