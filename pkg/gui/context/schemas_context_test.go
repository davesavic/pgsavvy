package context

import (
	"strings"
	"sync"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newTestSchemas(drv types.GuiDriver) *SchemasContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SCHEMAS,
		ViewName: string(types.SCHEMAS),
		Kind:     types.SIDE_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewSchemasContext(base, deps)
}

// TestSchemasContext_HandleRenderWritesRows guards against regression: without
// a HandleRender override SchemasContext inherited BaseContext's no-op
// and the SCHEMAS rail stayed blank after a successful connect even
// though populateSchemasRail loaded the items.
func TestSchemasContext_HandleRenderWritesRows(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSchemas(drv)
	c.SetItems([]any{
		models.Schema{Name: "app"},
		models.Schema{Name: "reporting"},
		models.Schema{Name: "public"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes == 0 {
		t.Fatal("HandleRender wrote nothing; expected schema rows")
	}
	body := drv.lastContent
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3; body=%q", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "> ") || !strings.Contains(lines[0], "app") {
		t.Errorf("cursor=0 line = %q, want '> ' prefix and 'app'", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") || strings.HasPrefix(lines[1], "> ") {
		t.Errorf("non-cursor line = %q, want '  ' prefix", lines[1])
	}
}

// TestSchemasContext_HandleRenderAcceptsPointerOrValue ensures the
// renderer copes with either models.Schema or *models.Schema item
// shapes. populateSchemasRail currently stores by value, but the
// SchemaPicker adapter already supports both — keep the render side
// symmetric.
func TestSchemasContext_HandleRenderAcceptsPointerOrValue(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSchemas(drv)
	c.SetItems([]any{
		models.Schema{Name: "by_value"},
		&models.Schema{Name: "by_pointer"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "by_value") || !strings.Contains(body, "by_pointer") {
		t.Errorf("body = %q, must contain both row names", body)
	}
}

// TestSchemasContext_HandleRenderEmptyClears guards the empty-rail
// path: with no items the rail writes empty content so a previous
// connection's rows don't linger after disconnect/reconnect.
func TestSchemasContext_HandleRenderEmptyClears(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSchemas(drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (empty): %v", err)
	}
	if drv.writes == 0 {
		t.Fatal("HandleRender on empty list did not write; rail would retain stale content")
	}
	if drv.lastContent != "" {
		t.Errorf("empty-list body = %q, want empty string", drv.lastContent)
	}
}

// TestSchemasContextShowHiddenModeConcurrentToggle fires ~100 concurrent
// goroutines flipping SetShowHiddenMode and reading GetShowHiddenMode to
// verify the atomic.Bool guard. Run under -race the
// Go runtime will fail the test if it observes a data race on the field.
func TestSchemasContextShowHiddenModeConcurrentToggle(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	s := tree.Schemas

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := range n {
		v := i%2 == 0
		go func(v bool) {
			defer wg.Done()
			s.SetShowHiddenMode(v)
		}(v)
		go func() {
			defer wg.Done()
			_ = s.GetShowHiddenMode()
		}()
	}

	wg.Wait()

	// Final write so the post-condition is deterministic regardless of
	// goroutine scheduling: any Load after this point must observe true.
	s.SetShowHiddenMode(true)
	if !s.GetShowHiddenMode() {
		t.Fatal("Schemas.GetShowHiddenMode() = false after final SetShowHiddenMode(true)")
	}
}
