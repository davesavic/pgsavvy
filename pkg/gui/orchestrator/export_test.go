package orchestrator

import (
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// HistoryStoreForTest returns the *query.History the most recent
// wireWithDriver pass opened, or nil if the store failed to open. Test-only
// seam (compiled only under `go test`) so external orchestrator_test cases can
// record through the REAL Record/write path the HistoryOpen worker reads from
// via Recent(), exercising the populated worker path end-to-end.
func (g *Gui) HistoryStoreForTest() *query.History {
	if g == nil {
		return nil
	}
	return g.history
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
