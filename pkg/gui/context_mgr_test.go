package gui

import (
	"errors"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

type fakeContext struct {
	key      types.ContextKey
	kind     types.ContextKind
	focusLog *[]types.ContextKey
	lostLog  *[]types.ContextKey
}

func newFake(key types.ContextKey, kind types.ContextKind, focusLog, lostLog *[]types.ContextKey) *fakeContext {
	return &fakeContext{key: key, kind: kind, focusLog: focusLog, lostLog: lostLog}
}

func (f *fakeContext) GetKey() types.ContextKey   { return f.key }
func (f *fakeContext) GetViewName() string        { return string(f.key) }
func (f *fakeContext) GetWindowName() string      { return string(f.key) }
func (f *fakeContext) GetKind() types.ContextKind { return f.kind }
func (f *fakeContext) GetTitle() string           { return "" }
func (f *fakeContext) HandleFocus(_ types.OnFocusOpts) error {
	if f.focusLog != nil {
		*f.focusLog = append(*f.focusLog, f.key)
	}
	return nil
}

func (f *fakeContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	if f.lostLog != nil {
		*f.lostLog = append(*f.lostLog, f.key)
	}
	return nil
}
func (f *fakeContext) HandleRender() error                    { return nil }
func (f *fakeContext) HandleRenderToMain() error              { return nil }
func (f *fakeContext) HandleQuit() error                      { return nil }
func (f *fakeContext) NeedsRerenderOnHeightChange() bool      { return false }
func (f *fakeContext) NeedsRerenderOnWidthChange() bool       { return false }
func (f *fakeContext) AddKeybindingsFn(_ types.KeybindingsFn) {}
func (f *fakeContext) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return nil
}

func (f *fakeContext) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}

func keysOf(stack []types.IBaseContext) []types.ContextKey {
	out := make([]types.ContextKey, len(stack))
	for i, c := range stack {
		out[i] = c.GetKey()
	}
	return out
}

func mustPush(t *testing.T, tree *ContextTree, c types.IBaseContext) {
	t.Helper()
	if err := tree.Push(c); err != nil {
		t.Fatalf("Push(%s) returned error: %v", c.GetKey(), err)
	}
}

func TestPushSideWipesStack(t *testing.T) {
	var lost []types.ContextKey
	tree := NewContextTree()
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, &lost)
	main := newFake(types.QUERY_EDITOR, types.MAIN_CONTEXT, nil, &lost)
	popup := newFake(types.CONFIRMATION, types.TEMPORARY_POPUP, nil, &lost)

	mustPush(t, tree, schemas)
	mustPush(t, tree, main)
	mustPush(t, tree, popup)

	lost = lost[:0]

	tables := newFake(types.TABLES, types.SIDE_CONTEXT, nil, nil)
	mustPush(t, tree, tables)

	got := keysOf(tree.Stack())
	want := []types.ContextKey{types.TABLES}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("stack = %v, want %v", got, want)
	}

	wantLostOrder := []types.ContextKey{types.CONFIRMATION, types.QUERY_EDITOR, types.SCHEMAS}
	if len(lost) != len(wantLostOrder) {
		t.Fatalf("HandleFocusLost order = %v, want %v", lost, wantLostOrder)
	}
	for i, k := range wantLostOrder {
		if lost[i] != k {
			t.Fatalf("HandleFocusLost[%d] = %s, want %s (full = %v)", i, lost[i], k, lost)
		}
	}
}

func TestPushMainRemovesOtherMain(t *testing.T) {
	var lost []types.ContextKey
	tree := NewContextTree()
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, &lost)
	queryEditor := newFake(types.QUERY_EDITOR, types.MAIN_CONTEXT, nil, &lost)
	popup := newFake(types.MENU, types.TEMPORARY_POPUP, nil, &lost)

	mustPush(t, tree, schemas)
	mustPush(t, tree, queryEditor)
	mustPush(t, tree, popup)

	lost = lost[:0]

	plan := newFake(types.PLAN, types.MAIN_CONTEXT, nil, nil)
	mustPush(t, tree, plan)

	got := keysOf(tree.Stack())
	want := []types.ContextKey{types.SCHEMAS, types.MENU, types.PLAN}
	if len(got) != len(want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("stack[%d] = %s, want %s (full = %v)", i, got[i], k, got)
		}
	}

	if len(lost) != 1 || lost[0] != types.QUERY_EDITOR {
		t.Fatalf("HandleFocusLost = %v, want [QUERY_EDITOR]", lost)
	}
}

func TestPushPopupAutoPopsTemporary(t *testing.T) {
	var lost []types.ContextKey
	tree := NewContextTree()
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, &lost)
	menu := newFake(types.MENU, types.TEMPORARY_POPUP, nil, &lost)
	confirmation := newFake(types.CONFIRMATION, types.TEMPORARY_POPUP, nil, nil)

	mustPush(t, tree, schemas)
	mustPush(t, tree, menu)

	lost = lost[:0]

	mustPush(t, tree, confirmation)

	got := keysOf(tree.Stack())
	want := []types.ContextKey{types.SCHEMAS, types.CONFIRMATION}
	if len(got) != len(want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("stack[%d] = %s, want %s", i, got[i], k)
		}
	}

	if len(lost) != 1 || lost[0] != types.MENU {
		t.Fatalf("HandleFocusLost = %v, want [MENU]", lost)
	}
}

func TestPopRefusesAtBottom(t *testing.T) {
	tree := NewContextTree()
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, nil, nil)
	mustPush(t, tree, schemas)

	err := tree.Pop()
	if err == nil {
		t.Fatal("Pop() returned nil error at root, want non-nil")
	}
	if !errors.Is(err, ErrPopAtBottom) {
		t.Fatalf("Pop() error = %v, want ErrPopAtBottom", err)
	}

	got := keysOf(tree.Stack())
	if len(got) != 1 || got[0] != types.SCHEMAS {
		t.Fatalf("stack after refused Pop = %v, want [SCHEMAS]", got)
	}
}

func TestReplaceKeepsPopups(t *testing.T) {
	var lost []types.ContextKey
	var focus []types.ContextKey
	tree := NewContextTree()
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, &focus, &lost)
	menu := newFake(types.MENU, types.TEMPORARY_POPUP, &focus, &lost)

	mustPush(t, tree, schemas)
	mustPush(t, tree, menu)

	focus = focus[:0]
	lost = lost[:0]

	confirmation := newFake(types.CONFIRMATION, types.TEMPORARY_POPUP, &focus, &lost)
	if err := tree.Replace(confirmation); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}

	got := keysOf(tree.Stack())
	want := []types.ContextKey{types.SCHEMAS, types.CONFIRMATION}
	if len(got) != len(want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("stack[%d] = %s, want %s", i, got[i], k)
		}
	}

	if len(focus) != 0 {
		t.Fatalf("Replace fired HandleFocus = %v, want none", focus)
	}
	if len(lost) != 0 {
		t.Fatalf("Replace fired HandleFocusLost = %v, want none", lost)
	}
}

func TestPushSameKeyTwiceIsNoop(t *testing.T) {
	var focus []types.ContextKey
	var lost []types.ContextKey
	tree := NewContextTree()
	schemas := newFake(types.SCHEMAS, types.SIDE_CONTEXT, &focus, &lost)

	mustPush(t, tree, schemas)

	focus = focus[:0]
	lost = lost[:0]

	// Push a different *instance* with the same key — should still no-op.
	dup := newFake(types.SCHEMAS, types.SIDE_CONTEXT, &focus, &lost)
	if err := tree.Push(dup); err != nil {
		t.Fatalf("Push duplicate returned error: %v", err)
	}

	got := keysOf(tree.Stack())
	if len(got) != 1 || got[0] != types.SCHEMAS {
		t.Fatalf("stack after duplicate push = %v, want [SCHEMAS]", got)
	}
	if len(focus) != 0 {
		t.Fatalf("duplicate Push fired HandleFocus = %v, want none", focus)
	}
	if len(lost) != 0 {
		t.Fatalf("duplicate Push fired HandleFocusLost = %v, want none", lost)
	}
}
