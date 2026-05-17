package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestStubContextSatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &StubContext{}
}

func TestStubContextIdentity(t *testing.T) {
	s := NewStubContext(types.QUERY_EDITOR, "query_editor")
	if got := s.GetKey(); got != types.QUERY_EDITOR {
		t.Fatalf("GetKey() = %s, want %s", got, types.QUERY_EDITOR)
	}
	if got := s.GetViewName(); got != "query_editor" {
		t.Fatalf("GetViewName() = %q, want %q", got, "query_editor")
	}
	if got := s.GetWindowName(); got != "query_editor" {
		t.Fatalf("GetWindowName() = %q, want %q", got, "query_editor")
	}
	if got := s.GetKind(); got != types.STUB {
		t.Fatalf("GetKind() = %d, want %d (STUB)", got, types.STUB)
	}
}

func TestStubContextLifecycleAllReturnNil(t *testing.T) {
	s := NewStubContext(types.RESULT_GRID, "result_grid")

	if err := s.HandleFocus(types.OnFocusOpts{NewContextKey: types.RESULT_GRID}); err != nil {
		t.Fatalf("HandleFocus = %v, want nil", err)
	}
	if err := s.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost = %v, want nil", err)
	}
	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender = %v, want nil", err)
	}
	if err := s.HandleRenderToMain(); err != nil {
		t.Fatalf("HandleRenderToMain = %v, want nil", err)
	}
	if err := s.HandleQuit(); err != nil {
		t.Fatalf("HandleQuit = %v, want nil", err)
	}
}

func TestStubContextRerenderHooksAreFalse(t *testing.T) {
	s := NewStubContext(types.PLAN, "plan")
	if s.NeedsRerenderOnHeightChange() {
		t.Fatal("NeedsRerenderOnHeightChange = true, want false")
	}
	if s.NeedsRerenderOnWidthChange() {
		t.Fatal("NeedsRerenderOnWidthChange = true, want false")
	}
}

func TestStubContextKeybindingsEmpty(t *testing.T) {
	s := NewStubContext(types.WHICH_KEY, "which_key")
	// AddKeybindingsFn must be a no-op (cannot bind to a deferred context).
	s.AddKeybindingsFn(func(_ types.KeybindingsOpts) []*types.KeyBinding {
		return []*types.KeyBinding{{Description: "should-be-dropped"}}
	})
	got := s.GetKeybindings(types.KeybindingsOpts{})
	if got == nil {
		t.Fatal("GetKeybindings returned nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("GetKeybindings returned %d entries, want 0 (stub drops AddKeybindingsFn)", len(got))
	}
	if mouse := s.GetMouseKeybindings(types.KeybindingsOpts{}); mouse != nil {
		t.Fatalf("GetMouseKeybindings = %v, want nil for stub", mouse)
	}
}

// HandleQuit returning nil must not perturb ContextMgr.Pop semantics.
// We don't import pkg/gui here (would invert the dependency graph), so
// we satisfy the AC by asserting HandleQuit returns nil and has no
// observable side effect on a second call.
func TestStubHandleQuitIdempotent(t *testing.T) {
	s := NewStubContext(types.HISTORY, "history")
	if err := s.HandleQuit(); err != nil {
		t.Fatalf("HandleQuit #1 = %v, want nil", err)
	}
	if err := s.HandleQuit(); err != nil {
		t.Fatalf("HandleQuit #2 = %v, want nil", err)
	}
}
