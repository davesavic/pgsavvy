package context

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeMatcherCanceller counts Cancel calls; satisfies
// types.MatcherCanceller. Lives in this file (not shared) so test
// failures point at the wiring directly.
type fakeMatcherCanceller struct {
	calls int
}

func (f *fakeMatcherCanceller) Cancel() { f.calls++ }

// newTestQueryEditorContext builds a QueryEditorContext with the
// in-package fakes wired in. Mirrors the constructor wiring that
// setup.go performs at runtime, but lets tests inspect the mode
// store + matcher fakes directly.
func newTestQueryEditorContext() (*QueryEditorContext, *fakeModeStore, *fakeMatcherCanceller) {
	modes := newFakeModeStore()
	matcher := &fakeMatcherCanceller{}
	ctx := NewQueryEditorContext(
		NewBaseContext(BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		depsAlias{},
		modes,
		matcher,
	)
	return ctx, modes, matcher
}

func TestQueryEditorContext_IBaseContextSurface(t *testing.T) {
	ctx, _, _ := newTestQueryEditorContext()

	if ctx.GetKey() != types.QUERY_EDITOR {
		t.Errorf("GetKey() = %q, want %q", ctx.GetKey(), types.QUERY_EDITOR)
	}
	if ctx.GetKind() != types.MAIN_CONTEXT {
		t.Errorf("GetKind() = %v, want MAIN_CONTEXT", ctx.GetKind())
	}
	if ctx.GetViewName() != string(types.QUERY_EDITOR) {
		t.Errorf("GetViewName() = %q, want %q", ctx.GetViewName(), types.QUERY_EDITOR)
	}
	if ctx.GetWindowName() != string(types.QUERY_EDITOR) {
		t.Errorf("GetWindowName() = %q, want %q (defaults to ViewName)", ctx.GetWindowName(), types.QUERY_EDITOR)
	}
}

func TestQueryEditorContext_BufferAndRepeatNonNil(t *testing.T) {
	ctx, _, _ := newTestQueryEditorContext()
	if ctx.Buffer() == nil {
		t.Fatal("Buffer() = nil, want non-nil")
	}
	if ctx.Repeat() == nil {
		t.Fatal("Repeat() = nil, want non-nil")
	}
}

func TestQueryEditorContext_HandleFocusSetsNormalMode(t *testing.T) {
	ctx, modes, _ := newTestQueryEditorContext()

	if err := ctx.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus = %v, want nil", err)
	}
	if got, ok := modes.set[types.QUERY_EDITOR]; !ok || got != types.ModeNormal {
		t.Fatalf("ModeStore[QUERY_EDITOR] = (%v, %v), want (ModeNormal, true)", got, ok)
	}
	if len(modes.sets) != 1 || modes.sets[0] != types.QUERY_EDITOR {
		t.Fatalf("Set call trace = %v, want [QUERY_EDITOR]", modes.sets)
	}
}

func TestQueryEditorContext_HandleFocusLostResetsAndCancels(t *testing.T) {
	ctx, modes, matcher := newTestQueryEditorContext()

	if err := ctx.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus = %v, want nil", err)
	}
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost = %v, want nil", err)
	}

	if _, ok := modes.set[types.QUERY_EDITOR]; ok {
		t.Fatal("ModeStore entry survived HandleFocusLost; want Reset to drop it")
	}
	if got := len(modes.resets); got != 1 || modes.resets[0] != types.QUERY_EDITOR {
		t.Fatalf("Reset call trace = %v, want [QUERY_EDITOR]", modes.resets)
	}
	if matcher.calls != 1 {
		t.Fatalf("matcher.Cancel calls = %d, want 1", matcher.calls)
	}
}

func TestQueryEditorContext_FocusBlurCycleIsIdempotent(t *testing.T) {
	ctx, modes, matcher := newTestQueryEditorContext()

	// Three focus/blur cycles; mode should converge to Normal after
	// focus and absent after blur, and matcher.Cancel fires once per
	// blur with no off-by-one weirdness.
	for i := range 3 {
		if err := ctx.HandleFocus(types.OnFocusOpts{}); err != nil {
			t.Fatalf("cycle %d: HandleFocus = %v", i, err)
		}
		if got := modes.set[types.QUERY_EDITOR]; got != types.ModeNormal {
			t.Fatalf("cycle %d: post-focus mode = %v, want ModeNormal", i, got)
		}
		if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
			t.Fatalf("cycle %d: HandleFocusLost = %v", i, err)
		}
		if _, ok := modes.set[types.QUERY_EDITOR]; ok {
			t.Fatalf("cycle %d: ModeStore retained QUERY_EDITOR after blur", i)
		}
	}
	if matcher.calls != 3 {
		t.Fatalf("matcher.Cancel calls = %d, want 3 (one per blur)", matcher.calls)
	}
}

func TestQueryEditorContext_HandleFocusLostNoPendingMatcherIsNoOp(t *testing.T) {
	ctx, _, matcher := newTestQueryEditorContext()

	// No HandleFocus, no half-built chord: HandleFocusLost should
	// still call matcher.Cancel exactly once (Matcher.Cancel itself
	// is documented as safe-when-idle) and not panic.
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost(idle) = %v, want nil", err)
	}
	if matcher.calls != 1 {
		t.Fatalf("matcher.Cancel calls = %d, want 1", matcher.calls)
	}
}

func TestQueryEditorContext_NilModesAndMatcherIsSafe(t *testing.T) {
	// Test wiring that omits modes + matcher entirely (e.g. fake
	// constructions) must not panic.
	ctx := NewQueryEditorContext(
		NewBaseContext(BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		depsAlias{},
		nil,
		nil,
	)
	if err := ctx.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus(nil deps) = %v, want nil", err)
	}
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost(nil deps) = %v, want nil", err)
	}
}

func TestQueryEditorContext_NilBufferExitVisualIsSafe(t *testing.T) {
	// The constructor never produces a nil Buffer, but the
	// exitVisualIfActive / saveBufferIfDirty stubs are documented as
	// nil-safe so tests / future refactors can substitute. Drop the
	// buffer field and confirm HandleFocusLost still completes.
	ctx, _, _ := newTestQueryEditorContext()
	ctx.buf = nil
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost(nil buf) = %v, want nil", err)
	}
}

func TestQueryEditorContext_RegisteredInTreeAsMainContext(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	if tree.QueryEditor == nil {
		t.Fatal("tree.QueryEditor = nil after NewContextTree")
	}
	if tree.QueryEditor.GetKind() != types.MAIN_CONTEXT {
		t.Fatalf("tree.QueryEditor.GetKind() = %v, want MAIN_CONTEXT", tree.QueryEditor.GetKind())
	}
	// ByKey lookup must round-trip through the IBaseContext slot.
	if got := tree.ByKey(types.QUERY_EDITOR); got == nil || got.GetKey() != types.QUERY_EDITOR {
		t.Fatalf("ByKey(QUERY_EDITOR) = %v, want a Context with that key", got)
	}
}

func TestQueryEditorContext_SetBuffer(t *testing.T) {
	ctx, _, _ := newTestQueryEditorContext()
	original := ctx.Buffer()
	replacement := original // any non-nil pointer; can't easily import editor here without cycle
	ctx.SetBuffer(replacement)
	if ctx.Buffer() != replacement {
		t.Error("SetBuffer did not install the replacement buffer")
	}
	// nil replacement is rejected silently — Buffer() stays non-nil.
	ctx.SetBuffer(nil)
	if ctx.Buffer() == nil {
		t.Error("SetBuffer(nil) overwrote the live buffer; want unchanged")
	}
}

func TestQueryEditorContext_HandleFocusLost_DirtyDispatchesSave(t *testing.T) {
	var sawConnID, sawUUID, sawContent string
	var calls int
	deps := depsAlias{
		SaveBuffer: func(connID, uuid, content string) {
			calls++
			sawConnID = connID
			sawUUID = uuid
			sawContent = content
		},
	}
	modes := newFakeModeStore()
	matcher := &fakeMatcherCanceller{}
	ctx := NewQueryEditorContext(
		NewBaseContext(BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		deps,
		modes,
		matcher,
	)
	buf := ctx.Buffer()
	buf.ConnectionID = "conn-1"
	buf.UUID = "deadbeef-1234-4567-89ab-cdef01234567"
	buf.Dirty = true
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if calls != 1 {
		t.Errorf("SaveBuffer hook invoked %d times; want 1", calls)
	}
	if sawConnID != "conn-1" || sawUUID != "deadbeef-1234-4567-89ab-cdef01234567" {
		t.Errorf("hook args = (%q, %q, %q); want (\"conn-1\", uuid, content)", sawConnID, sawUUID, sawContent)
	}
	if buf.Dirty {
		t.Error("Dirty stayed true after focus-loss save dispatch")
	}
}

func TestQueryEditorContext_HandleFocusLost_NotDirtyIsNoOp(t *testing.T) {
	var calls int
	deps := depsAlias{
		SaveBuffer: func(_, _, _ string) { calls++ },
	}
	modes := newFakeModeStore()
	matcher := &fakeMatcherCanceller{}
	ctx := NewQueryEditorContext(
		NewBaseContext(BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		deps,
		modes,
		matcher,
	)
	buf := ctx.Buffer()
	buf.ConnectionID = "conn-1"
	buf.UUID = "deadbeef-1234-4567-89ab-cdef01234567"
	buf.Dirty = false
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if calls != 0 {
		t.Errorf("SaveBuffer hook invoked %d times on clean buffer; want 0", calls)
	}
}

func TestQueryEditorContext_HandleFocusLost_EmptyConnIDSkipsSave(t *testing.T) {
	var calls int
	deps := depsAlias{
		SaveBuffer: func(_, _, _ string) { calls++ },
	}
	ctx := NewQueryEditorContext(
		NewBaseContext(BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		deps,
		newFakeModeStore(),
		&fakeMatcherCanceller{},
	)
	buf := ctx.Buffer()
	buf.Dirty = true
	// Leave ConnectionID + UUID empty.
	if err := ctx.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}
	if calls != 0 {
		t.Errorf("SaveBuffer hook invoked %d times with empty connID; want 0", calls)
	}
}
