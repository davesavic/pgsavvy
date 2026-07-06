package context

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestNewContextTreeReturnsAllContexts(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree == nil {
		t.Fatal("NewContextTree returned nil")
	}
	flat := tree.Flatten()
	// SEARCH_LINE (TEMPORARY_POPUP) takes the count 26→27.
	// RELATIONSHIP_PANEL (DISPLAY_CONTEXT) takes it 27→28.
	// SAVED_QUERY (PERSISTENT_POPUP) takes it 28→29.
	// SCHEMA_RAIL replaced the two flattened SCHEMAS/TABLES side contexts
	// with a single flattened container (the leaves are now inFlatten=false
	// named-only fields), taking it 29→28.
	// tkt5.2 QUERY_RAIL topology flip: QUERY_EDITOR / SAVED_QUERY / HISTORY
	// become inFlatten=false container leaves (-3) and the single QUERY_RAIL
	// container is added (+1), taking it 28→26.
	// CELL_VIEWER (PERSISTENT_POPUP) takes it 26→27.
	// SETTINGS (MAIN_CONTEXT) takes it 27→28.
	if len(flat) != 28 {
		t.Fatalf("Flatten() len = %d, want 28 (QUERY_RAIL flattened; editor/saved/history leaves excluded; CELL_VIEWER, SETTINGS added)", len(flat))
	}
	// Sanity: no nil entries.
	for i, c := range flat {
		if c == nil {
			t.Fatalf("Flatten()[%d] is nil", i)
		}
	}
}

// TestQueryRailTopology pins the tkt5.2 QUERY_RAIL container flip:
// Flatten() includes QUERY_RAIL exactly once and EXCLUDES the three leaves
// (QUERY_EDITOR/SAVED_QUERY/HISTORY); AllContextKeys() returns QUERY_RAIL;
// the container's default active tab is 0 (the editor).
func TestQueryRailTopology(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})

	leaves := map[types.ContextKey]bool{
		types.QUERY_EDITOR: false,
		types.SAVED_QUERY:  false,
		types.HISTORY:      false,
	}
	railCount := 0
	for _, c := range tree.Flatten() {
		if c.GetKey() == types.QUERY_RAIL {
			railCount++
		}
		if _, isLeaf := leaves[c.GetKey()]; isLeaf {
			t.Errorf("Flatten() includes container leaf %q; leaves must be inFlatten=false", c.GetKey())
		}
	}
	if railCount != 1 {
		t.Fatalf("Flatten() contains QUERY_RAIL %d times, want exactly 1", railCount)
	}

	found := false
	for _, k := range types.AllContextKeys() {
		if k == types.QUERY_RAIL {
			found = true
			break
		}
	}
	if !found {
		t.Error("AllContextKeys() does not include QUERY_RAIL")
	}

	if tree.QueryRail == nil {
		t.Fatal("tree.QueryRail = nil after NewContextTree")
	}
	if got := tree.QueryRail.ActiveTab(); got != 0 {
		t.Errorf("QueryRail.ActiveTab() = %d, want 0 (editor default)", got)
	}
	if got := tree.QueryRail.ActiveLeafKey(); got != types.QUERY_EDITOR {
		t.Errorf("QueryRail.ActiveLeafKey() = %q, want QUERY_EDITOR", got)
	}
}

func TestNewContextTreeEveryKeyRetrievable(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})

	allKeys := []types.ContextKey{
		// Live (14 — 1 side container + 9 temp popup + 1 global + 3 display + 1
		// persistent popup; SCHEMAS/TABLES are now named-only leaves and not
		// retrievable via ByKey, replaced by the SCHEMA_RAIL container).
		types.SCHEMA_RAIL,
		types.MENU, types.CONFIRMATION, types.PROMPT, types.SELECTION, types.SUGGESTIONS, types.COMMAND_LINE, types.HIDE_OVERLAY, types.EXPORT_MENU, types.TABLE_INSPECT,
		types.GLOBAL, types.LIMIT, types.WHICH_KEY, types.CHEATSHEET,
		types.FIRST_RUN_TIP,
		types.CELL_VIEWER,
		// Main + stub. QUERY_RAIL is the flattened container; QUERY_EDITOR /
		// HISTORY / SAVED_QUERY are now inFlatten=false leaves and NOT
		// retrievable via ByKey (tkt5.2 topology flip).
		types.QUERY_RAIL, types.CONNECTION_MANAGER, types.TABLE_DATA_EDITOR, types.RESULT_GRID,
		types.PLAN,
		types.SETTINGS,
	}
	if len(allKeys) != 22 {
		t.Fatalf("test bug: allKeys len = %d, want 22", len(allKeys))
	}
	for _, k := range allKeys {
		c := tree.ByKey(k)
		if c == nil {
			t.Fatalf("ByKey(%s) = nil, want a Context", k)
		}
		if c.GetKey() != k {
			t.Fatalf("ByKey(%s).GetKey() = %s, want %s", k, c.GetKey(), k)
		}
	}
}

func TestNewContextTreeKindAssignments(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})

	type want struct {
		key  types.ContextKey
		kind types.ContextKind
	}
	cases := []want{
		// 1 SIDE_CONTEXT: the SCHEMA_RAIL container (SCHEMAS/TABLES are
		// named-only leaves, not retrievable via ByKey).
		{types.SCHEMA_RAIL, types.SIDE_CONTEXT},
		// 9 TEMPORARY_POPUP.
		{types.MENU, types.TEMPORARY_POPUP},
		{types.CONFIRMATION, types.TEMPORARY_POPUP},
		{types.PROMPT, types.TEMPORARY_POPUP},
		{types.SELECTION, types.TEMPORARY_POPUP},
		{types.SUGGESTIONS, types.TEMPORARY_POPUP},
		{types.COMMAND_LINE, types.TEMPORARY_POPUP},
		{types.HIDE_OVERLAY, types.TEMPORARY_POPUP},
		{types.EXPORT_MENU, types.TEMPORARY_POPUP},
		{types.TABLE_INSPECT, types.TEMPORARY_POPUP},
		// HISTORY is now a QUERY_RAIL leaf (inFlatten=false), not ByKey-
		// retrievable (tkt5.2 topology flip).
		// 1 GLOBAL, 3 DISPLAY (messages panel removed).
		{types.GLOBAL, types.GLOBAL_CONTEXT},
		{types.LIMIT, types.DISPLAY_CONTEXT},
		{types.WHICH_KEY, types.DISPLAY_CONTEXT},
		{types.CHEATSHEET, types.DISPLAY_CONTEXT},
		// 1 PERSISTENT_POPUP (FIRST_RUN_TIP). SAVED_QUERY dropped its
		// PERSISTENT_POPUP kind and is now a QUERY_RAIL leaf (tkt5.2).
		{types.FIRST_RUN_TIP, types.PERSISTENT_POPUP},
		{types.CELL_VIEWER, types.PERSISTENT_POPUP},
		// 3 MAIN_CONTEXT: QUERY_RAIL container (QUERY_EDITOR is now a
		// non-flattened leaf) + CONNECTION_MANAGER + SETTINGS.
		{types.QUERY_RAIL, types.MAIN_CONTEXT},
		{types.CONNECTION_MANAGER, types.MAIN_CONTEXT},
		{types.SETTINGS, types.MAIN_CONTEXT},
		// 3 STUB (TABLE_DATA_EDITOR + RESULT_GRID + PLAN).
		{types.TABLE_DATA_EDITOR, types.STUB},
		{types.RESULT_GRID, types.STUB},
		{types.PLAN, types.STUB},
	}
	if len(cases) != 22 {
		t.Fatalf("test bug: cases len = %d, want 22", len(cases))
	}
	for _, c := range cases {
		got := tree.ByKey(c.key)
		if got == nil {
			t.Fatalf("ByKey(%s) = nil", c.key)
		}
		if got.GetKind() != c.kind {
			t.Fatalf("ByKey(%s).GetKind() = %d, want %d", c.key, got.GetKind(), c.kind)
		}
	}
}

func TestNewContextTreeKindCounts(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	counts := map[types.ContextKind]int{}
	for _, c := range tree.Flatten() {
		counts[c.GetKind()]++
	}
	want := map[types.ContextKind]int{
		// SCHEMA_RAIL container is the only flattened SIDE_CONTEXT; SCHEMAS and
		// TABLES are inFlatten=false leaves.
		types.SIDE_CONTEXT: 1,
		// CellEditor, CommitDialog, ConflictDialog and
		// FKReversePicker take TEMPORARY_POPUP from 9→13.
		// SEARCH_LINE takes it 13→14.
		// HISTORY (promoted from STUB) took it 14→15, then tkt5.2 flipped
		// HISTORY into a QUERY_RAIL leaf (MAIN_CONTEXT, inFlatten=false),
		// taking it back 15→14.
		types.TEMPORARY_POPUP: 14,
		types.EXTRAS_CONTEXT:  0,
		types.GLOBAL_CONTEXT:  1,
		// RELATIONSHIP_PANEL takes DISPLAY_CONTEXT 3→4.
		types.DISPLAY_CONTEXT: 4,
		// MAIN_CONTEXT: the QUERY_RAIL container (tkt5.2 — QUERY_EDITOR is now
		// a non-flattened leaf) + CONNECTION_MANAGER + SETTINGS = 3.
		types.MAIN_CONTEXT: 3,
		// HISTORY was promoted from STUB to TEMPORARY_POPUP, so
		// STUB drops 4→3 (HISTORY's later flip to a MAIN_CONTEXT leaf does not
		// affect STUB).
		types.STUB: 3,
		// FIRST_RUN_TIP is the only PERSISTENT_POPUP now: SAVED_QUERY dropped
		// its PERSISTENT_POPUP kind to become a QUERY_RAIL leaf (tkt5.2), so
		// PERSISTENT_POPUP drops 2→1.
		// CELL_VIEWER (PERSISTENT_POPUP) takes it 1→2.
		types.PERSISTENT_POPUP: 2,
	}
	for k, w := range want {
		if counts[k] != w {
			t.Fatalf("kind %d count = %d, want %d (full = %+v)", k, counts[k], w, counts)
		}
	}
}

func TestGlobalContextHasNoViewName(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if got := tree.Global.GetViewName(); got != "" {
		t.Fatalf("Global.GetViewName() = %q, want \"\" (GLOBAL_CONTEXT is viewless)", got)
	}
}

func TestNewContextTreeNilDepsIsSafe(t *testing.T) {
	// Reconstruct with all-nil hooks: HandleRender on every Context must
	// not panic and must return nil.
	tree := NewContextTree(types.ContextTreeDeps{})
	for _, c := range tree.Flatten() {
		if err := c.HandleRender(); err != nil {
			t.Fatalf("%s.HandleRender = %v, want nil", c.GetKey(), err)
		}
	}
}

func TestNewContextTreeCommandLineWired(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree.CommandLine == nil {
		t.Fatal("CommandLine field is nil after NewContextTree")
	}
	if tree.CommandLine.GetKey() != types.COMMAND_LINE {
		t.Errorf("CommandLine.GetKey() = %q, want %q",
			tree.CommandLine.GetKey(), types.COMMAND_LINE)
	}
	if tree.CommandLine.GetKind() != types.TEMPORARY_POPUP {
		t.Errorf("CommandLine.GetKind() = %v, want TEMPORARY_POPUP",
			tree.CommandLine.GetKind())
	}
	if tree.CommandLine.GetViewName() != string(types.COMMAND_LINE) {
		t.Errorf("CommandLine.GetViewName() = %q, want %q",
			tree.CommandLine.GetViewName(), string(types.COMMAND_LINE))
	}
	// Must appear in Flatten().
	found := false
	for _, c := range tree.Flatten() {
		if c == tree.CommandLine {
			found = true
			break
		}
	}
	if !found {
		t.Error("CommandLine missing from Flatten() output")
	}
}

func TestSchemasContextShowHiddenAccessors(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree.Schemas.GetShowHiddenMode() {
		t.Fatal("Schemas.GetShowHiddenMode() = true at construction, want false")
	}
	tree.Schemas.SetShowHiddenMode(true)
	if !tree.Schemas.GetShowHiddenMode() {
		t.Fatal("Schemas.GetShowHiddenMode() = false after SetShowHiddenMode(true)")
	}
	tree.Schemas.SetShowHiddenMode(false)
	if tree.Schemas.GetShowHiddenMode() {
		t.Fatal("Schemas.GetShowHiddenMode() = true after SetShowHiddenMode(false)")
	}
}
