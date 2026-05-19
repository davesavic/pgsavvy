package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func newVimQEC(t *testing.T) *context.QueryEditorContext {
	t.Helper()
	return context.NewQueryEditorContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		types.ContextTreeDeps{},
		nil,
		nil,
	)
}

func TestVimEditorControllerPublishesMotionBindings(t *testing.T) {
	ctrl := controllers.NewVimEditorController(newVimQEC(t), nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	wantActions := map[string]bool{
		commands.MotionCharLeft:          false,
		commands.MotionCharRight:         false,
		commands.MotionLineDown:          false,
		commands.MotionLineUp:            false,
		commands.MotionWordNext:          false,
		commands.MotionWordPrev:          false,
		commands.MotionWordEnd:           false,
		commands.MotionWordNextBig:       false,
		commands.MotionWordPrevBig:       false,
		commands.MotionWordEndBig:        false,
		commands.MotionLineStart:         false,
		commands.MotionLineFirstNonblank: false,
		commands.MotionLineEnd:           false,
		commands.MotionBufferStart:       false,
		commands.MotionBufferEnd:         false,
		commands.MotionParagraphPrev:     false,
		commands.MotionParagraphNext:     false,
		commands.MotionSentencePrev:      false,
		commands.MotionSentenceNext:      false,
		commands.MotionScreenTop:         false,
		commands.MotionScreenMiddle:      false,
		commands.MotionScreenBottom:      false,
	}
	wantMode := types.ModeNormal | types.ModeOperatorPending
	for _, kb := range kbs {
		if _, ok := wantActions[kb.ActionID]; ok {
			wantActions[kb.ActionID] = true
		}
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("kb %s scope = %s, want QUERY_EDITOR", kb.ActionID, kb.Scope)
		}
		if kb.Mode != wantMode {
			t.Errorf("kb %s mode = %v, want Normal|OperatorPending", kb.ActionID, kb.Mode)
		}
	}
	for id, seen := range wantActions {
		if !seen {
			t.Errorf("action %q not published in VimEditor bindings", id)
		}
	}
}

func TestVimEditorRegisterActionsCoversAllMotions(t *testing.T) {
	ctrl := controllers.NewVimEditorController(newVimQEC(t), nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, id := range []string{
		commands.MotionCharLeft,
		commands.MotionCharRight,
		commands.MotionLineDown,
		commands.MotionLineUp,
		commands.MotionWordNext,
		commands.MotionWordPrev,
		commands.MotionWordEnd,
		commands.MotionWordNextBig,
		commands.MotionWordPrevBig,
		commands.MotionWordEndBig,
		commands.MotionLineStart,
		commands.MotionLineFirstNonblank,
		commands.MotionLineEnd,
		commands.MotionBufferStart,
		commands.MotionBufferEnd,
		commands.MotionParagraphPrev,
		commands.MotionParagraphNext,
		commands.MotionSentencePrev,
		commands.MotionSentenceNext,
		commands.MotionScreenTop,
		commands.MotionScreenMiddle,
		commands.MotionScreenBottom,
	} {
		if _, ok := reg.Get(id); !ok {
			t.Errorf("registry missing motion action %q", id)
		}
	}
}

func TestVimEditorMotionHandlerMovesCursor(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.MotionWordNext)
	if !ok {
		t.Fatalf("registry missing %s", commands.MotionWordNext)
	}
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeNormal}); err != nil {
		t.Fatalf("Handler err = %v", err)
	}
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 6}) {
		t.Fatalf("cursor after WordNext = %+v, want {0,6}", got)
	}
}

func TestVimEditorMotionHandlerHonorsCount(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("one two three four")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.MotionWordNext)
	if err := cmd.Handler(commands.ExecCtx{Count: 3, Mode: types.ModeNormal}); err != nil {
		t.Fatalf("Handler err = %v", err)
	}
	// "one two three four" — index 14 is 'f' (start of "four").
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 14}) {
		t.Fatalf("cursor after WordNext count=3 = %+v, want {0,14}", got)
	}
}

func TestVimEditorMotionHandlerNegativeCountNoOp(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	start := editor.Position{Line: 0, Col: 5}
	buf.SetCursor(start)

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.MotionWordNext)
	if err := cmd.Handler(commands.ExecCtx{Count: -2, Mode: types.ModeNormal}); err != nil {
		t.Fatalf("Handler err = %v", err)
	}
	if got := buf.CursorPos(); got != start {
		t.Fatalf("cursor moved on negative count: %+v, want %+v", got, start)
	}
}

func TestVimEditorJumpMotionPushesJumpList(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("first")},
		{Runes: []rune("second")},
		{Runes: []rune("third")},
	}
	start := editor.Position{Line: 2, Col: 3}
	buf.SetCursor(start)

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.MotionBufferStart)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeNormal}); err != nil {
		t.Fatalf("Handler err = %v", err)
	}
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 0}) {
		t.Fatalf("cursor after gg = %+v, want {0,0}", got)
	}
	if buf.Jumps == nil || buf.Jumps.Len() != 1 {
		t.Fatalf("JumpList Len = %d, want 1", buf.Jumps.Len())
	}
	if got := buf.Jumps.At(0); got != start {
		t.Fatalf("JumpList[0] = %+v, want %+v", got, start)
	}
}

func TestVimEditorNonJumpMotionDoesNotPush(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.MotionWordNext)
	_ = cmd.Handler(commands.ExecCtx{Mode: types.ModeNormal})
	if buf.Jumps != nil && buf.Jumps.Len() != 0 {
		t.Fatalf("WordNext incorrectly pushed jump entry; Len = %d", buf.Jumps.Len())
	}
}

func TestVimEditorOperatorPendingSkipsCursorMove(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	start := editor.Position{Line: 0, Col: 0}
	buf.SetCursor(start)

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.MotionWordNext)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeOperatorPending}); err != nil {
		t.Fatalf("Handler err = %v", err)
	}
	// applyPending stub is a no-op; cursor must NOT move.
	if got := buf.CursorPos(); got != start {
		t.Fatalf("cursor moved during operator-pending: %+v, want %+v", got, start)
	}
}

func TestVimEditorNoCollisionWithQueryEditorBindings(t *testing.T) {
	qec := newVimQEC(t)
	vim := controllers.NewVimEditorController(qec, nil)
	qe := controllers.NewQueryEditorController(nil, controllers.HelperBag{})

	type key struct {
		seq  string
		mode types.Mode
	}
	seen := map[key]string{}
	add := func(bindings []*types.ChordBinding, owner string) {
		for _, b := range bindings {
			if b.Scope != types.QUERY_EDITOR {
				continue
			}
			k := key{seq: keys.SequenceString(b.Sequence), mode: b.Mode}
			if other, exists := seen[k]; exists {
				t.Errorf("collision on (%q, %v): %s and %s", k.seq, k.mode, other, owner)
				continue
			}
			seen[k] = owner
		}
	}
	add(qe.GetKeybindings(types.KeybindingsOpts{}), "QueryEditor")
	add(vim.GetKeybindings(types.KeybindingsOpts{}), "VimEditor")
}
