package orchestrator

import "github.com/davesavic/dbsavvy/pkg/query"

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
