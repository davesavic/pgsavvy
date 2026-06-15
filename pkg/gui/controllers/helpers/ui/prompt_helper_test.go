package ui_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newPromptCtx() *guicontext.PromptContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.PROMPT,
		ViewName: string(types.PROMPT),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewPromptContext(base, guicontext.Deps{})
}

func TestPromptPushesTemporaryPopup(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewPromptHelper(tree, newPromptCtx())

	if err := h.Prompt("name?", "init", func(string) error { return nil }, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !h.Active() {
		t.Fatal("Active = false after Prompt; want true")
	}
	top := tree.Current()
	if top == nil || top.GetKey() != types.PROMPT {
		t.Fatalf("top = %v; want PROMPT", top)
	}
	if top.GetKind() != types.TEMPORARY_POPUP {
		t.Fatalf("kind = %v; want TEMPORARY_POPUP", top.GetKind())
	}
}

func TestPromptSubmitDeliversValue(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewPromptHelper(tree, newPromptCtx())
	var got string
	_ = h.Prompt("name?", "init", func(v string) error { got = v; return nil }, nil)
	if err := h.Submit("alice"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got != "alice" {
		t.Fatalf("submit value = %q; want alice", got)
	}
	if h.Active() {
		t.Fatal("Active = true after Submit")
	}
}

func TestPromptCancelInvokesCancelCb(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewPromptHelper(tree, newPromptCtx())
	cancelled := 0
	_ = h.Prompt("?", "", nil, func() error { cancelled++; return nil })
	if err := h.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled = %d; want 1", cancelled)
	}
}

func TestPromptLabelAndInitialAccessors(t *testing.T) {
	h := ui.NewPromptHelper(nil, nil)
	_ = h.Prompt("the label", "the initial", nil, nil)
	if h.Label() != "the label" {
		t.Errorf("Label = %q", h.Label())
	}
	if h.Initial() != "the initial" {
		t.Errorf("Initial = %q", h.Initial())
	}
}
