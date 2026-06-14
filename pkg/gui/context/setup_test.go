package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestNewContextTreeReturnsAllContexts(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree == nil {
		t.Fatal("NewContextTree returned nil")
	}
	flat := tree.Flatten()
	// SEARCH_LINE (TEMPORARY_POPUP) takes the count 26→27.
	if len(flat) != 27 {
		t.Fatalf("Flatten() len = %d, want 27 (20 live + 4 stub + 2 main + 1 persistent)", len(flat))
	}
	// Sanity: no nil entries.
	for i, c := range flat {
		if c == nil {
			t.Fatalf("Flatten()[%d] is nil", i)
		}
	}
}

func TestNewContextTreeEveryKeyRetrievable(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})

	allKeys := []types.ContextKey{
		// Live (15 — 2 side + 9 temp popup + 1 global + 3 display + 1 persistent popup; messages panel removed).
		types.SCHEMAS, types.TABLES,
		types.MENU, types.CONFIRMATION, types.PROMPT, types.SELECTION, types.SUGGESTIONS, types.COMMAND_LINE, types.HIDE_OVERLAY, types.EXPORT_MENU, types.TABLE_INSPECT,
		types.GLOBAL, types.LIMIT, types.WHICH_KEY, types.CHEATSHEET,
		types.FIRST_RUN_TIP,
		// Main + stub (6 — CONNECTING removed).
		types.QUERY_EDITOR, types.CONNECTION_MANAGER, types.TABLE_DATA_EDITOR, types.RESULT_GRID,
		types.PLAN, types.HISTORY,
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
		// 2 SIDE_CONTEXT (CONNECTIONS removed).
		{types.SCHEMAS, types.SIDE_CONTEXT},
		{types.TABLES, types.SIDE_CONTEXT},
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
		// HISTORY promoted from STUB to TEMPORARY_POPUP.
		{types.HISTORY, types.TEMPORARY_POPUP},
		// 1 GLOBAL, 3 DISPLAY (messages panel removed).
		{types.GLOBAL, types.GLOBAL_CONTEXT},
		{types.LIMIT, types.DISPLAY_CONTEXT},
		{types.WHICH_KEY, types.DISPLAY_CONTEXT},
		{types.CHEATSHEET, types.DISPLAY_CONTEXT},
		// 1 PERSISTENT_POPUP (FIRST_RUN_TIP).
		{types.FIRST_RUN_TIP, types.PERSISTENT_POPUP},
		// 2 MAIN_CONTEXT (CONNECTING removed).
		{types.QUERY_EDITOR, types.MAIN_CONTEXT},
		{types.CONNECTION_MANAGER, types.MAIN_CONTEXT},
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
		types.SIDE_CONTEXT: 2,
		// CellEditor, CommitDialog, ConflictDialog and
		// FKReversePicker take TEMPORARY_POPUP from 9→13.
		// SEARCH_LINE takes it 13→14.
		// HISTORY (promoted from STUB) takes it 14→15.
		types.TEMPORARY_POPUP: 15,
		types.EXTRAS_CONTEXT:  0,
		types.GLOBAL_CONTEXT:  1,
		types.DISPLAY_CONTEXT: 3,
		// QUERY_EDITOR was promoted from STUB to a real
		// MAIN_CONTEXT, so STUB drops 5→4 and MAIN_CONTEXT rises 0→1.
		// CONNECTING was added (MAIN_CONTEXT), so MAIN_CONTEXT is 2.
		// CONNECTING was later removed, so MAIN_CONTEXT is 2.
		types.MAIN_CONTEXT: 2,
		// HISTORY was promoted from STUB to TEMPORARY_POPUP, so
		// STUB drops 4→3.
		types.STUB: 3,
		// FIRST_RUN_TIP is the first
		// PERSISTENT_POPUP shipped by the app.
		types.PERSISTENT_POPUP: 1,
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
