package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestBaseContextIdentityAccessors(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{
		Key:        types.CONNECTIONS,
		ViewName:   "connections",
		WindowName: "connections-window",
		Kind:       types.SIDE_CONTEXT,
	})

	if got := b.GetKey(); got != types.CONNECTIONS {
		t.Fatalf("GetKey() = %s, want %s", got, types.CONNECTIONS)
	}
	if got := b.GetViewName(); got != "connections" {
		t.Fatalf("GetViewName() = %q, want %q", got, "connections")
	}
	if got := b.GetWindowName(); got != "connections-window" {
		t.Fatalf("GetWindowName() = %q, want %q", got, "connections-window")
	}
	if got := b.GetKind(); got != types.SIDE_CONTEXT {
		t.Fatalf("GetKind() = %d, want %d", got, types.SIDE_CONTEXT)
	}
}

func TestBaseContextWindowNameDefaultsToViewName(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{
		Key:      types.SCHEMAS,
		ViewName: "schemas",
		Kind:     types.SIDE_CONTEXT,
	})
	if got := b.GetWindowName(); got != "schemas" {
		t.Fatalf("GetWindowName() = %q, want %q (default from ViewName)", got, "schemas")
	}
}

func TestBaseContextLifecycleHooksReturnNil(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{Key: types.GLOBAL, Kind: types.GLOBAL_CONTEXT})

	if err := b.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus = %v, want nil", err)
	}
	if err := b.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost = %v, want nil", err)
	}
	if err := b.HandleRender(); err != nil {
		t.Fatalf("HandleRender = %v, want nil", err)
	}
	if err := b.HandleRenderToMain(); err != nil {
		t.Fatalf("HandleRenderToMain = %v, want nil", err)
	}
	if err := b.HandleQuit(); err != nil {
		t.Fatalf("HandleQuit = %v, want nil", err)
	}
	if b.NeedsRerenderOnHeightChange() {
		t.Fatal("NeedsRerenderOnHeightChange = true, want false")
	}
	if b.NeedsRerenderOnWidthChange() {
		t.Fatal("NeedsRerenderOnWidthChange = true, want false")
	}
}

func TestAddKeybindingsFnAppendsAndGetReturnsConcatenation(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, Kind: types.SIDE_CONTEXT})

	bindA := &types.ChordBinding{ViewName: "schemas", Description: "A"}
	bindB := &types.ChordBinding{ViewName: "schemas", Description: "B"}
	bindC := &types.ChordBinding{ViewName: "schemas", Description: "C"}

	b.AddKeybindingsFn(func(_ types.KeybindingsOpts) []*types.ChordBinding {
		return []*types.ChordBinding{bindA}
	})
	b.AddKeybindingsFn(func(_ types.KeybindingsOpts) []*types.ChordBinding {
		return []*types.ChordBinding{bindB, bindC}
	})

	got := b.GetKeybindings(types.KeybindingsOpts{})
	if len(got) != 3 {
		t.Fatalf("len(GetKeybindings) = %d, want 3", len(got))
	}
	wantOrder := []string{"A", "B", "C"}
	for i, want := range wantOrder {
		if got[i].Description != want {
			t.Fatalf("GetKeybindings[%d].Description = %q, want %q", i, got[i].Description, want)
		}
	}
}

// Last-attached-wins is documented as a SEMANTIC property: when two
// controllers register the same Key+ViewName tuple the later registration
// is the one the runtime resolves. GetKeybindings preserves attachment
// order so the later entry always appears AFTER the earlier one in the
// returned slice — the driver registration loop then overwrites the
// earlier binding by writing the later one second.
func TestGetKeybindingsLastAttachedWinsOrdering(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, Kind: types.SIDE_CONTEXT})

	earlier := &types.ChordBinding{ViewName: "schemas", Description: "old-H-handler"}
	later := &types.ChordBinding{ViewName: "schemas", Description: "new-H-handler"}

	b.AddKeybindingsFn(func(_ types.KeybindingsOpts) []*types.ChordBinding {
		return []*types.ChordBinding{earlier}
	})
	b.AddKeybindingsFn(func(_ types.KeybindingsOpts) []*types.ChordBinding {
		return []*types.ChordBinding{later}
	})

	got := b.GetKeybindings(types.KeybindingsOpts{})
	if len(got) != 2 {
		t.Fatalf("len(GetKeybindings) = %d, want 2", len(got))
	}
	if got[0].Description != "old-H-handler" {
		t.Fatalf("got[0] = %q, want %q", got[0].Description, "old-H-handler")
	}
	if got[1].Description != "new-H-handler" {
		t.Fatalf("got[1] = %q, want %q (later attachment must come AFTER earlier)", got[1].Description, "new-H-handler")
	}
}

func TestAddKeybindingsFnNilIsNoop(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, Kind: types.SIDE_CONTEXT})
	b.AddKeybindingsFn(nil)
	got := b.GetKeybindings(types.KeybindingsOpts{})
	if got == nil {
		t.Fatal("GetKeybindings returned nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(GetKeybindings) = %d, want 0", len(got))
	}
}

func TestGetKeybindingsEmptyReturnsNonNilSlice(t *testing.T) {
	b := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, Kind: types.SIDE_CONTEXT})
	got := b.GetKeybindings(types.KeybindingsOpts{})
	if got == nil {
		t.Fatal("GetKeybindings on empty context returned nil, want empty non-nil slice (AC)")
	}
	if len(got) != 0 {
		t.Fatalf("len(GetKeybindings) = %d, want 0", len(got))
	}
}

func TestBaseContextSatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &BaseContext{}
}
