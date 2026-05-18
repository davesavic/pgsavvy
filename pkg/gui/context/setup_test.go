package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestNewContextTreeReturnsAllEighteenContexts(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree == nil {
		t.Fatal("NewContextTree returned nil")
	}
	flat := tree.Flatten()
	if len(flat) != 18 {
		t.Fatalf("Flatten() len = %d, want 18 (13 live + 5 stub)", len(flat))
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
		// Live (13 — 5 side + 4 popup + 1 extras + 1 global + 2 display).
		types.CONNECTIONS, types.SCHEMAS, types.TABLES, types.COLUMNS, types.INDEXES,
		types.MENU, types.CONFIRMATION, types.PROMPT, types.SUGGESTIONS,
		types.LOG, types.GLOBAL, types.LIMIT, types.WHICH_KEY,
		// Stub (5).
		types.QUERY_EDITOR, types.TABLE_DATA_EDITOR, types.RESULT_GRID,
		types.PLAN, types.HISTORY,
	}
	if len(allKeys) != 18 {
		t.Fatalf("test bug: allKeys len = %d, want 18", len(allKeys))
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
		// 5 SIDE_CONTEXT.
		{types.CONNECTIONS, types.SIDE_CONTEXT},
		{types.SCHEMAS, types.SIDE_CONTEXT},
		{types.TABLES, types.SIDE_CONTEXT},
		{types.COLUMNS, types.SIDE_CONTEXT},
		{types.INDEXES, types.SIDE_CONTEXT},
		// 4 TEMPORARY_POPUP.
		{types.MENU, types.TEMPORARY_POPUP},
		{types.CONFIRMATION, types.TEMPORARY_POPUP},
		{types.PROMPT, types.TEMPORARY_POPUP},
		{types.SUGGESTIONS, types.TEMPORARY_POPUP},
		// 1 EXTRAS, 1 GLOBAL, 2 DISPLAY.
		{types.LOG, types.EXTRAS_CONTEXT},
		{types.GLOBAL, types.GLOBAL_CONTEXT},
		{types.LIMIT, types.DISPLAY_CONTEXT},
		{types.WHICH_KEY, types.DISPLAY_CONTEXT},
		// 5 STUB.
		{types.QUERY_EDITOR, types.STUB},
		{types.TABLE_DATA_EDITOR, types.STUB},
		{types.RESULT_GRID, types.STUB},
		{types.PLAN, types.STUB},
		{types.HISTORY, types.STUB},
	}
	if len(cases) != 18 {
		t.Fatalf("test bug: cases len = %d, want 18", len(cases))
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
		types.SIDE_CONTEXT:    5,
		types.TEMPORARY_POPUP: 4,
		types.EXTRAS_CONTEXT:  1,
		types.GLOBAL_CONTEXT:  1,
		types.DISPLAY_CONTEXT: 2,
		types.STUB:            5,
	}
	for k, w := range want {
		if counts[k] != w {
			t.Fatalf("kind %d count = %d, want %d (full = %+v)", k, counts[k], w, counts)
		}
	}
	// Make sure nothing leaked into kinds we don't ship in T2.
	if counts[types.MAIN_CONTEXT] != 0 {
		t.Fatalf("MAIN_CONTEXT count = %d, want 0", counts[types.MAIN_CONTEXT])
	}
	if counts[types.PERSISTENT_POPUP] != 0 {
		t.Fatalf("PERSISTENT_POPUP count = %d, want 0", counts[types.PERSISTENT_POPUP])
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
