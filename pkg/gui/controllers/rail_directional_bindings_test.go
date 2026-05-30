package controllers

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// dbsavvy-xs0: railDirectionalBindings publishes Ctrl+H/J/K/L on the
// scope appropriate for each pane. Connections/Schemas get Up/Down/Right-
// to-QueryEditor; Tables gets Up/Down/Right-to-Results (physically
// adjacent); QueryEditor gets Left(=LastRail)/Down(=Results); result
// grid gets Left(=Tables)/Up(=QueryEditor). Anything else gets nothing.
func TestRailDirectionalBindings_PerScope(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	ctrlH := types.ChordKey{Code: 'h', Mod: types.ChordModCtrl}
	ctrlJ := types.ChordKey{Code: 'j', Mod: types.ChordModCtrl}
	ctrlK := types.ChordKey{Code: 'k', Mod: types.ChordModCtrl}
	ctrlL := types.ChordKey{Code: 'l', Mod: types.ChordModCtrl}

	type want struct {
		key      types.ChordKey
		actionID string
	}
	cases := []struct {
		name  string
		scope types.ContextKey
		want  []want
	}{
		{"schemas", types.SCHEMAS, []want{
			{ctrlK, commands.RailSwitchUp},
			{ctrlJ, commands.RailSwitchDown},
			{ctrlL, commands.RailSwitchQueryEditor},
		}},
		{"tables", types.TABLES, []want{
			{ctrlK, commands.RailSwitchUp},
			{ctrlJ, commands.RailSwitchDown},
			{ctrlL, commands.RailSwitchResults},
		}},
		{"query_editor", types.QUERY_EDITOR, []want{
			{ctrlH, commands.RailSwitchLastRail},
			{ctrlJ, commands.RailSwitchResults},
		}},
		{"result_grid", types.RESULT_GRID, []want{
			{ctrlH, commands.RailSwitchTables},
			{ctrlK, commands.RailSwitchQueryEditor},
		}},
		{"global (no bindings)", types.GLOBAL, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := railDirectionalBindings(tc.scope, tr)
			if len(got) != len(tc.want) {
				t.Fatalf("scope=%s: got %d bindings, want %d", tc.scope, len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if len(got[i].Sequence) != 1 {
					t.Errorf("scope=%s [%d]: sequence length = %d, want 1", tc.scope, i, len(got[i].Sequence))
					continue
				}
				if got[i].Sequence[0] != w.key {
					t.Errorf("scope=%s [%d]: key = %+v, want %+v", tc.scope, i, got[i].Sequence[0], w.key)
				}
				if got[i].ActionID != w.actionID {
					t.Errorf("scope=%s [%d]: actionID = %q, want %q", tc.scope, i, got[i].ActionID, w.actionID)
				}
				if got[i].Scope != tc.scope {
					t.Errorf("scope=%s [%d]: scope = %q, want %q", tc.scope, i, got[i].Scope, tc.scope)
				}
				if got[i].Mode != types.ModeNormal {
					t.Errorf("scope=%s [%d]: mode = %v, want ModeNormal so Insert mode keeps Backspace/Enter behavior", tc.scope, i, got[i].Mode)
				}
			}
		})
	}
}
