package orchestrator

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestOrderedViewNamesCoversLiveContexts asserts that every entry in
// orderedViewNames maps to a known ContextKey (no typos), and that
// every live context the registry exposes either appears in the list
// or is intentionally absent (only GLOBAL is omitted — it has no view).
func TestOrderedViewNamesCoversLiveContexts(t *testing.T) {
	want := map[string]struct{}{
		string(types.CONNECTIONS):  {},
		string(types.SCHEMAS):      {},
		string(types.TABLES):       {},
		string(types.COLUMNS):      {},
		string(types.INDEXES):      {},
		string(types.LOG):          {},
		string(types.MENU):         {},
		string(types.CONFIRMATION): {},
		string(types.PROMPT):       {},
		string(types.SUGGESTIONS):  {},
		string(types.COMMAND_LINE): {},
		string(types.LIMIT):        {},
		string(types.CHEATSHEET):   {},
	}
	got := map[string]struct{}{}
	for _, name := range orderedViewNames() {
		got[name] = struct{}{}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("orderedViewNames missing %q", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("orderedViewNames has unexpected entry %q", name)
		}
	}
}
