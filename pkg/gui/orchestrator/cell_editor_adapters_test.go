package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

// TestStreamBlocksEdit pins the edit-gate state policy: inline edits are
// permitted during StateRunning (rows are buffered, appends are
// append-only, pending edits are PK-keyed) but blocked while no stable
// buffer exists — StateQueued (no rows opened) and StateSorting (re-run
// cleared the buffer). Terminal states never block. dbsavvy-1po.
func TestStreamBlocksEdit(t *testing.T) {
	cases := []struct {
		state ui.TabState
		block bool
	}{
		{ui.StateQueued, true},
		{ui.StateSorting, true},
		{ui.StateRunning, false},
		{ui.StateComplete, false},
		{ui.StateCancelled, false},
		{ui.StateDetached, false},
		{ui.StateErrored, false},
		{ui.StatePlan, false},
		{ui.StateConnectionLost, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := streamBlocksEdit(tc.state); got != tc.block {
				t.Errorf("streamBlocksEdit(%q) = %v; want %v", tc.state, got, tc.block)
			}
		})
	}
}
