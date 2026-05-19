package ui_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// Build a minimal Confirmation context for tests via the real
// constructor + a no-op deps bag.
func newConfirmationCtx() *guicontext.ConfirmationContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.CONFIRMATION,
		ViewName: string(types.CONFIRMATION),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewConfirmationContext(base, guicontext.Deps{})
}

// pushRoot installs a SIDE_CONTEXT root so a later Pop() does not hit
// ErrPopAtBottom — mirrors the runtime invariant that there is always
// a side-context root underneath any popup.
func pushRoot(t *testing.T, tree *gui.ContextTree) {
	t.Helper()
	root := &minimalCtx{key: types.CONNECTIONS, kind: types.SIDE_CONTEXT}
	if err := tree.Push(root); err != nil {
		t.Fatalf("push root: %v", err)
	}
}

func TestConfirmPushesTemporaryPopup(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	conf := newConfirmationCtx()
	h := ui.NewConfirmHelper(tree, conf)

	if err := h.Confirm("title", "body", func() error { return nil }, nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !h.Active() {
		t.Fatalf("Active = false after Confirm; want true")
	}
	top := tree.Current()
	if top == nil || top.GetKey() != types.CONFIRMATION {
		t.Fatalf("top = %v; want CONFIRMATION", top)
	}
	if top.GetKind() != types.TEMPORARY_POPUP {
		t.Fatalf("kind = %v; want TEMPORARY_POPUP", top.GetKind())
	}
}

func TestConfirmYesInvokesCallback(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewConfirmHelper(tree, newConfirmationCtx())
	yesCalled := 0
	if err := h.Confirm("t", "b", func() error { yesCalled++; return nil }, nil); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if err := h.Yes(); err != nil {
		t.Fatalf("Yes: %v", err)
	}
	if yesCalled != 1 {
		t.Fatalf("yesCalled = %d; want 1", yesCalled)
	}
	if h.Active() {
		t.Fatalf("Active = true after Yes; want false")
	}
}

func TestConfirmNoInvokesCallback(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewConfirmHelper(tree, newConfirmationCtx())
	noCalled := 0
	if err := h.Confirm("t", "b", nil, func() error { noCalled++; return nil }); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if err := h.No(); err != nil {
		t.Fatalf("No: %v", err)
	}
	if noCalled != 1 {
		t.Fatalf("noCalled = %d; want 1", noCalled)
	}
}

func TestConfirmTitleAndBodyAccessors(t *testing.T) {
	h := ui.NewConfirmHelper(nil, nil)
	_ = h.Confirm("hello", "world", nil, nil)
	if h.Title() != "hello" {
		t.Errorf("Title = %q; want hello", h.Title())
	}
	if h.Body() != "world" {
		t.Errorf("Body = %q; want world", h.Body())
	}
}

// minimalCtx is a no-op IBaseContext used to root the focus stack.
type minimalCtx struct {
	key  types.ContextKey
	kind types.ContextKind
}

func (m *minimalCtx) GetKey() types.ContextKey                      { return m.key }
func (m *minimalCtx) GetViewName() string                           { return string(m.key) }
func (m *minimalCtx) GetWindowName() string                         { return string(m.key) }
func (m *minimalCtx) GetKind() types.ContextKind                    { return m.kind }
func (m *minimalCtx) GetTitle() string                              { return "" }
func (m *minimalCtx) HandleFocus(_ types.OnFocusOpts) error         { return nil }
func (m *minimalCtx) HandleFocusLost(_ types.OnFocusLostOpts) error { return nil }
func (m *minimalCtx) HandleRender() error                           { return nil }
func (m *minimalCtx) HandleRenderToMain() error                     { return nil }
func (m *minimalCtx) HandleQuit() error                             { return nil }
func (m *minimalCtx) NeedsRerenderOnHeightChange() bool             { return false }
func (m *minimalCtx) NeedsRerenderOnWidthChange() bool              { return false }
func (m *minimalCtx) AddKeybindingsFn(_ types.KeybindingsFn)        {}
func (m *minimalCtx) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	return nil
}

func (m *minimalCtx) GetMouseKeybindings(_ types.KeybindingsOpts) []types.MouseBinding {
	return nil
}
