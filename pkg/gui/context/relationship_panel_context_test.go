package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// newRelationshipPanelForTest builds a RELATIONSHIP_PANEL context bound to
// a RecorderGuiDriver with its view pre-registered so SetContent records
// instead of returning ErrUnknownView.
func newRelationshipPanelForTest(t *testing.T) (*RelationshipPanelContext, *testfake.RecorderGuiDriver) {
	t.Helper()
	rec := testfake.NewRecorderGuiDriver()
	// Register the view so SetContent has a target.
	if _, err := rec.SetView(string(types.RELATIONSHIP_PANEL), 0, 0, 10, 10, 0); err != nil {
		// Fresh-view creation pairs with ErrUnknownView on some driver
		// modes; the view is registered regardless, which is all we need.
		_ = err
	}
	base := NewBaseContext(BaseContextOpts{
		Key:      types.RELATIONSHIP_PANEL,
		ViewName: string(types.RELATIONSHIP_PANEL),
		Kind:     types.DISPLAY_CONTEXT,
		Title:    "Relationships",
	})
	deps := types.ContextTreeDeps{GuiDriver: rec}
	return NewRelationshipPanelContext(base, deps), rec
}

func TestRelationshipPanelContextKindAndKey(t *testing.T) {
	p, _ := newRelationshipPanelForTest(t)
	if p.GetKey() != types.RELATIONSHIP_PANEL {
		t.Fatalf("GetKey() = %q, want relationship_panel", p.GetKey())
	}
	if p.GetKind() != types.DISPLAY_CONTEXT {
		t.Fatalf("GetKind() = %d, want DISPLAY_CONTEXT", p.GetKind())
	}
}

func TestRelationshipPanelContextRendersBody(t *testing.T) {
	p, rec := newRelationshipPanelForTest(t)
	p.SetBody("-> customers (customer_id=42)")
	if err := p.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	got := rec.GetViewBuffer(string(types.RELATIONSHIP_PANEL))
	if got != "-> customers (customer_id=42)" {
		t.Fatalf("rendered body = %q, want the FK line", got)
	}
}

func TestRelationshipPanelContextEmptyBodyRendersGracefully(t *testing.T) {
	p, rec := newRelationshipPanelForTest(t)
	// No SetBody — body is empty. HandleRender must not panic and writes "".
	if err := p.HandleRender(); err != nil {
		t.Fatalf("HandleRender on empty body: %v", err)
	}
	if got := rec.GetViewBuffer(string(types.RELATIONSHIP_PANEL)); got != "" {
		t.Fatalf("empty-body render = %q, want empty", got)
	}
}

func TestRelationshipPanelContextNilDriverNoPanic(t *testing.T) {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.RELATIONSHIP_PANEL,
		ViewName: string(types.RELATIONSHIP_PANEL),
		Kind:     types.DISPLAY_CONTEXT,
	})
	p := NewRelationshipPanelContext(base, types.ContextTreeDeps{})
	p.SetBody("anything")
	// Nil GuiDriver: writeView is a silent no-op, HandleRender must not
	// panic and returns nil.
	if err := p.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestRelationshipPanelContextAddKeybindingsIsNoOp(t *testing.T) {
	p, _ := newRelationshipPanelForTest(t)
	// DISPLAY_CONTEXT chrome drops contributors; GetKeybindings stays empty
	// even after an AddKeybindingsFn call.
	p.AddKeybindingsFn(func(types.KeybindingsOpts) []*types.ChordBinding {
		return []*types.ChordBinding{{ActionID: "should.not.appear"}}
	})
	if got := p.GetKeybindings(types.KeybindingsOpts{}); len(got) != 0 {
		t.Fatalf("GetKeybindings() = %d bindings, want 0 (DISPLAY_CONTEXT is read-only chrome)", len(got))
	}
}
