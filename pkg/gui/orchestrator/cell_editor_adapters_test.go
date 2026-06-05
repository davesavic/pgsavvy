package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

// TestFormatForEdit_JSONObject asserts the cell-editor seed for a
// json/jsonb value pgx decoded into a Go map is JSON text, not Go's
// default map formatting (map[plan:pro]) that the user would otherwise
// have to hand-correct. Mirrors the grid renderer so seed and display
// agree. dbsavvy json-cell-format.
func TestFormatForEdit_JSONObject(t *testing.T) {
	got := cellEditorPicker{}.FormatForEdit(map[string]any{"plan": "pro", "active": true})
	require.Equal(t, `{"active":true,"plan":"pro"}`, got)
}

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
