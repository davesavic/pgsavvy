package ui_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newSearchCtx() *guicontext.SearchLineContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      types.SEARCH_LINE,
		ViewName: string(types.SEARCH_LINE),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewSearchLineContext(base, guicontext.Deps{}, nil)
}

func TestSearchLineOpenPushesTemporaryPopup(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewSearchLineHelper(tree, newSearchCtx())

	if err := h.Open(ui.SearchLineOpts{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !h.Active() {
		t.Fatal("Active = false after Open; want true")
	}
	top := tree.Current()
	if top == nil || top.GetKey() != types.SEARCH_LINE {
		t.Fatalf("top = %v; want SEARCH_LINE", top)
	}
	if top.GetKind() != types.TEMPORARY_POPUP {
		t.Fatalf("kind = %v; want TEMPORARY_POPUP", top.GetKind())
	}
}

func TestSearchLineOnChangeForwardsQuery(t *testing.T) {
	h := ui.NewSearchLineHelper(nil, nil)
	var got []string
	_ = h.Open(ui.SearchLineOpts{OnChange: func(q string) { got = append(got, q) }})
	h.OnChange("f")
	h.OnChange("fo")
	if len(got) != 2 || got[0] != "f" || got[1] != "fo" {
		t.Fatalf("onChange got = %v, want [f fo]", got)
	}
}

// <CR> → OnAccept(query) + pop. Cursor is NOT restored on accept.
func TestSearchLineAcceptDeliversQueryAndPops(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewSearchLineHelper(tree, newSearchCtx())

	var accepted string
	restored := false
	_ = h.Open(ui.SearchLineOpts{
		OnAccept:      func(q string) { accepted = q },
		CursorRestore: func(any) { restored = true },
	})
	if err := h.OnAccept("needle"); err != nil {
		t.Fatalf("OnAccept: %v", err)
	}
	if accepted != "needle" {
		t.Fatalf("accepted = %q; want needle", accepted)
	}
	if h.Active() {
		t.Fatal("Active = true after OnAccept")
	}
	if top := tree.Current(); top == nil || top.GetKey() != types.SCHEMAS {
		t.Fatalf("top after accept = %v; want SCHEMAS (popped)", top)
	}
	if restored {
		t.Error("cursor restored on accept; want restore only on cancel")
	}
}

// <Esc> → OnCancel() + pop; cursor restore runs AFTER Pop().
func TestSearchLineCancelPopsRestoresCursorAfterPop(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewSearchLineHelper(tree, newSearchCtx())

	var order []string
	cancelled := false
	_ = h.Open(ui.SearchLineOpts{
		OnCancel: func() { cancelled = true },
		CursorSnapshot: func() any {
			order = append(order, "snapshot")
			return 42
		},
		CursorRestore: func(snap any) {
			// Pop must have already run: the grid context (SCHEMAS) is back
			// on top before restore runs.
			if top := tree.Current(); top == nil || top.GetKey() != types.SCHEMAS {
				t.Errorf("restore ran before Pop: top = %v, want SCHEMAS", top)
			}
			if snap != 42 {
				t.Errorf("restore snapshot = %v, want 42", snap)
			}
			order = append(order, "restore")
		},
	})
	if err := h.OnCancel(); err != nil {
		t.Fatalf("OnCancel: %v", err)
	}
	if !cancelled {
		t.Fatal("OnCancel callback not fired")
	}
	if h.Active() {
		t.Fatal("Active = true after OnCancel")
	}
	if len(order) != 2 || order[0] != "snapshot" || order[1] != "restore" {
		t.Fatalf("order = %v, want [snapshot restore]", order)
	}
}

// Empty buffer + Esc still fires OnCancel + restore (no typed query).
func TestSearchLineCancelEmptyStillFiresCancelAndRestore(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewSearchLineHelper(tree, newSearchCtx())

	cancelled := false
	restored := false
	_ = h.Open(ui.SearchLineOpts{
		OnCancel:       func() { cancelled = true },
		CursorSnapshot: func() any { return nil },
		CursorRestore:  func(any) { restored = true },
	})
	if err := h.OnCancel(); err != nil {
		t.Fatalf("OnCancel: %v", err)
	}
	if !cancelled {
		t.Error("OnCancel not fired on empty-buffer cancel")
	}
	if !restored {
		t.Error("cursor not restored on empty-buffer cancel")
	}
}

func TestSearchLineSetMatchCountForwardsToContext(t *testing.T) {
	// With a wired context the count slot reaches the context's render
	// (content assertion is covered in the context package test). Here we
	// only pin that the call is wired and nil-safe.
	ctx := newSearchCtx()
	h := ui.NewSearchLineHelper(nil, ctx)
	h.SetMatchCount("2/9") // must not panic

	h2 := ui.NewSearchLineHelper(nil, nil)
	h2.SetMatchCount("x") // nil context must not panic
}

func TestSearchLineCaretTogglerOnAndOff(t *testing.T) {
	tree := gui.NewContextTree()
	pushRoot(t, tree)
	h := ui.NewSearchLineHelper(tree, newSearchCtx())
	var states []bool
	h.SetCaretToggler(func(on bool) { states = append(states, on) })

	_ = h.Open(ui.SearchLineOpts{})
	_ = h.OnCancel()
	if len(states) != 2 || states[0] != true || states[1] != false {
		t.Fatalf("caret states = %v, want [true false]", states)
	}
}
