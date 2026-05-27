package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// railEmptyHook returns a RailEmptyText closure that maps each side-rail
// ContextKey to its contextual dim placeholder (mirrors the production
// wiring the orchestrator supplies). Used by the empty-state tests below.
func railEmptyHook() func(types.ContextKey) string {
	return func(key types.ContextKey) string {
		switch key {
		case types.SCHEMAS:
			return "(no schemas)"
		case types.TABLES:
			return "(select a schema)"
		case types.COLUMNS:
			return "(select a table)"
		case types.INDEXES:
			return "(select a table)"
		default:
			return ""
		}
	}
}

func newEmptyStateDeps(drv types.GuiDriver, hook func(types.ContextKey) string) Deps {
	deps := types.ContextTreeDeps{GuiDriver: drv}
	if hook != nil {
		deps.RailEmptyText = hook
	}
	return deps
}

// TestSchemasContext_EmptyStatePlaceholder asserts the SCHEMAS rail renders
// its dim "(no schemas)" placeholder when the item list is empty and a
// RailEmptyText hook is wired.
func TestSchemasContext_EmptyStatePlaceholder(t *testing.T) {
	drv := &captureDriver{}
	base := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, ViewName: string(types.SCHEMAS), Kind: types.SIDE_CONTEXT})
	c := NewSchemasContext(base, newEmptyStateDeps(drv, railEmptyHook()))
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "(no schemas)" {
		t.Errorf("empty SCHEMAS rail = %q, want %q", drv.lastContent, "(no schemas)")
	}
}

// TestTablesContext_EmptyStatePlaceholder asserts the TABLES rail renders the
// dim "(select a schema)" placeholder when empty, distinguishable from a
// blank loading state.
func TestTablesContext_EmptyStatePlaceholder(t *testing.T) {
	drv := &captureDriver{}
	base := NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT})
	c := NewTablesContext(base, newEmptyStateDeps(drv, railEmptyHook()))
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "(select a schema)" {
		t.Errorf("empty TABLES rail = %q, want %q", drv.lastContent, "(select a schema)")
	}
}

// TestColumnsContext_EmptyStatePlaceholder asserts the COLUMNS rail renders
// the dim "(select a table)" placeholder when empty.
func TestColumnsContext_EmptyStatePlaceholder(t *testing.T) {
	drv := &captureDriver{}
	base := NewBaseContext(BaseContextOpts{Key: types.COLUMNS, ViewName: string(types.COLUMNS), Kind: types.SIDE_CONTEXT})
	c := NewColumnsContext(base, newEmptyStateDeps(drv, railEmptyHook()))
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "(select a table)" {
		t.Errorf("empty COLUMNS rail = %q, want %q", drv.lastContent, "(select a table)")
	}
}

// TestIndexesContext_EmptyStatePlaceholder asserts the INDEXES rail renders
// the dim "(select a table)" placeholder when empty.
func TestIndexesContext_EmptyStatePlaceholder(t *testing.T) {
	drv := &captureDriver{}
	base := NewBaseContext(BaseContextOpts{Key: types.INDEXES, ViewName: string(types.INDEXES), Kind: types.SIDE_CONTEXT})
	c := NewIndexesContext(base, newEmptyStateDeps(drv, railEmptyHook()))
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != "(select a table)" {
		t.Errorf("empty INDEXES rail = %q, want %q", drv.lastContent, "(select a table)")
	}
}

// TestSideRailEmptyState_NilHookFallsThroughToBlank asserts every rail with
// no RailEmptyText hook wired renders the prior blank output and does not
// panic (nil-safety AC).
func TestSideRailEmptyState_NilHookFallsThroughToBlank(t *testing.T) {
	cases := []struct {
		key    types.ContextKey
		render func(Deps) error
	}{
		{types.SCHEMAS, func(d Deps) error {
			return NewSchemasContext(NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, ViewName: string(types.SCHEMAS), Kind: types.SIDE_CONTEXT}), d).HandleRender()
		}},
		{types.TABLES, func(d Deps) error {
			return NewTablesContext(NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT}), d).HandleRender()
		}},
		{types.COLUMNS, func(d Deps) error {
			return NewColumnsContext(NewBaseContext(BaseContextOpts{Key: types.COLUMNS, ViewName: string(types.COLUMNS), Kind: types.SIDE_CONTEXT}), d).HandleRender()
		}},
		{types.INDEXES, func(d Deps) error {
			return NewIndexesContext(NewBaseContext(BaseContextOpts{Key: types.INDEXES, ViewName: string(types.INDEXES), Kind: types.SIDE_CONTEXT}), d).HandleRender()
		}},
	}
	for _, tc := range cases {
		drv := &captureDriver{}
		deps := newEmptyStateDeps(drv, nil) // no RailEmptyText hook
		if err := tc.render(deps); err != nil {
			t.Fatalf("%s HandleRender (nil hook): %v", tc.key, err)
		}
		if drv.lastContent != "" {
			t.Errorf("%s nil-hook rail = %q, want blank", tc.key, drv.lastContent)
		}
	}
}
