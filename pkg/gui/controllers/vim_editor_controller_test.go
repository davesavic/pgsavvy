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
	// Motion bindings produce two ChordBindings per action: one for
	// ModeNormal (zero sentinel, can't be ORed) and one for the remaining
	// non-Normal modes (OperatorPending + Visual variants).
	wantNonNormal := types.ModeOperatorPending |
		types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock
	seenNormal := map[string]bool{}
	seenNonNormal := map[string]bool{}
	for _, kb := range kbs {
		if _, ok := wantActions[kb.ActionID]; !ok {
			continue
		}
		wantActions[kb.ActionID] = true
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("kb %s scope = %s, want QUERY_EDITOR", kb.ActionID, kb.Scope)
		}
		switch kb.Mode {
		case types.ModeNormal:
			seenNormal[kb.ActionID] = true
		case wantNonNormal:
			seenNonNormal[kb.ActionID] = true
		default:
			t.Errorf("kb %s unexpected mode = %v", kb.ActionID, kb.Mode)
		}
	}
	for id, seen := range wantActions {
		if !seen {
			t.Errorf("action %q not published in VimEditor bindings", id)
			continue
		}
		if !seenNormal[id] {
			t.Errorf("action %q missing ModeNormal binding", id)
		}
		if !seenNonNormal[id] {
			t.Errorf("action %q missing non-Normal mode binding", id)
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

func TestVimEditorPublishesAndRegistersPasteBeforeAndToggleCase(t *testing.T) {
	ctrl := controllers.NewVimEditorController(newVimQEC(t), nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	wantBound := map[string]bool{
		commands.EditorPasteBefore: false,
		commands.EditorToggleCase:  false,
	}
	for _, kb := range kbs {
		if _, ok := wantBound[kb.ActionID]; ok {
			wantBound[kb.ActionID] = true
		}
	}
	for id, seen := range wantBound {
		if !seen {
			t.Errorf("action %q not published in VimEditor bindings", id)
		}
	}

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for id := range wantBound {
		if _, ok := reg.Get(id); !ok {
			t.Errorf("registry missing action %q", id)
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

func TestVimEditorVisualEnterRegistersAndSeedsSelection(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 2})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.VisualEnter)
	if !ok {
		t.Fatalf("registry missing VisualEnter")
	}
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeNormal}); err != nil {
		t.Fatalf("VisualEnter handler err = %v", err)
	}
	if buf.Selection == nil {
		t.Fatalf("Selection not seeded by VisualEnter")
	}
	if buf.Selection.Start != (editor.Position{Line: 0, Col: 2}) {
		t.Fatalf("Selection.Start = %+v, want {0,2}", buf.Selection.Start)
	}
	if buf.Selection.LineWise || buf.Selection.BlockWise {
		t.Fatalf("char-wise visual should not set LineWise/BlockWise; got %+v", *buf.Selection)
	}
}

func TestVimEditorVisualEnterLineFlagsLineWise(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("a")}}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.VisualEnterLine)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeNormal}); err != nil {
		t.Fatalf("VisualEnterLine handler err = %v", err)
	}
	if buf.Selection == nil || !buf.Selection.LineWise || buf.Selection.BlockWise {
		t.Fatalf("Selection = %+v, want LineWise true", buf.Selection)
	}
}

func TestVimEditorVisualExitClearsSelection(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("abc")}}
	buf.Selection = &editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 3}}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.VisualExit)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual}); err != nil {
		t.Fatalf("VisualExit handler err = %v", err)
	}
	if buf.Selection != nil {
		t.Fatalf("Selection should be nil after VisualExit, got %+v", *buf.Selection)
	}
}

func TestVimEditorMotionInVisualExtendsSelection(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Enter visual first via the handler so Selection is seeded.
	enter, _ := reg.Get(commands.VisualEnter)
	_ = enter.Handler(commands.ExecCtx{Mode: types.ModeNormal})

	cmd, _ := reg.Get(commands.MotionWordNext)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual}); err != nil {
		t.Fatalf("WordNext handler err = %v", err)
	}
	if buf.Selection == nil {
		t.Fatalf("Selection cleared during Visual-mode motion")
	}
	if buf.Selection.End != (editor.Position{Line: 0, Col: 6}) {
		t.Fatalf("Selection.End = %+v, want {0,6}", buf.Selection.End)
	}
	if buf.Selection.Start != (editor.Position{Line: 0, Col: 0}) {
		t.Fatalf("Selection.Start moved during extend: %+v", buf.Selection.Start)
	}
}

func TestVimEditorMotionInVisualDoesNotPushJump(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("a")},
		{Runes: []rune("b")},
	}
	buf.SetCursor(editor.Position{Line: 1, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	enter, _ := reg.Get(commands.VisualEnter)
	_ = enter.Handler(commands.ExecCtx{Mode: types.ModeNormal})

	cmd, _ := reg.Get(commands.MotionBufferStart)
	_ = cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual})
	if buf.Jumps != nil && buf.Jumps.Len() != 0 {
		t.Fatalf("Visual-mode jump-motion pushed entry; Len = %d", buf.Jumps.Len())
	}
}

func TestVimEditorTextObjectInVisualSnapsSelection(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune(`SELECT "foo" FROM t`)}}
	// Place cursor inside the quoted string.
	buf.SetCursor(editor.Position{Line: 0, Col: 9})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	enter, _ := reg.Get(commands.VisualEnter)
	_ = enter.Handler(commands.ExecCtx{Mode: types.ModeNormal})

	cmd, ok := reg.Get(commands.TextObjectInnerQuoteDouble)
	if !ok {
		t.Fatalf("registry missing TextObjectInnerQuoteDouble")
	}
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual}); err != nil {
		t.Fatalf("text-object handler err = %v", err)
	}
	if buf.Selection == nil {
		t.Fatalf("Selection cleared by text-object in Visual")
	}
	// InnerQuote("foo") spans cols 8..11 (after the opening quote, before the closing).
	if buf.Selection.Start.Col != 8 || buf.Selection.End.Col != 11 {
		t.Fatalf("Selection cols = (%d,%d), want (8,11): %+v", buf.Selection.Start.Col, buf.Selection.End.Col, *buf.Selection)
	}
}

func TestVimEditorTextObjectInVisualBlockSnapsSelection(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune(`SELECT "foo" FROM t`)}}
	buf.SetCursor(editor.Position{Line: 0, Col: 9})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	enter, _ := reg.Get(commands.VisualEnterBlock)
	_ = enter.Handler(commands.ExecCtx{Mode: types.ModeNormal})

	cmd, ok := reg.Get(commands.TextObjectInnerQuoteDouble)
	if !ok {
		t.Fatalf("registry missing TextObjectInnerQuoteDouble")
	}
	// text objects must fire (and snap) in VisualBlock.
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisualBlock}); err != nil {
		t.Fatalf("text-object handler err = %v", err)
	}
	if buf.Selection == nil {
		t.Fatalf("Selection cleared by text-object in VisualBlock")
	}
	if buf.Selection.Start.Col != 8 || buf.Selection.End.Col != 11 {
		t.Fatalf("Selection cols = (%d,%d), want (8,11)", buf.Selection.Start.Col, buf.Selection.End.Col)
	}
}

func TestVimEditorTextObjectPublishedUnderVisualBlock(t *testing.T) {
	ctrl := controllers.NewVimEditorController(newVimQEC(t), nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})
	found := false
	for _, kb := range kbs {
		if kb.ActionID == commands.TextObjectInnerWord && kb.Mode.Has(types.ModeVisualBlock) {
			found = true
			break
		}
	}
	if !found {
		t.Error("iw text object not published under ModeVisualBlock (textObjectModeMask)")
	}
}

func TestVimEditorPublishesVisualBindings(t *testing.T) {
	ctrl := controllers.NewVimEditorController(newVimQEC(t), nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})
	wantVisual := map[string]bool{
		commands.VisualEnter:      false,
		commands.VisualEnterLine:  false,
		commands.VisualEnterBlock: false,
		commands.VisualExit:       false,
	}
	for _, kb := range kbs {
		if _, ok := wantVisual[kb.ActionID]; ok {
			wantVisual[kb.ActionID] = true
		}
	}
	for id, seen := range wantVisual {
		if !seen {
			t.Errorf("visual action %q not published", id)
		}
	}
}

func TestVimEditorNoCollisionWithQueryEditorBindings(t *testing.T) {
	qec := newVimQEC(t)
	vim := controllers.NewVimEditorController(qec, nil)
	qe := controllers.NewQueryEditorController(nil, controllers.CoreDeps{}, controllers.NavDeps{}, controllers.UIDeps{}, controllers.QueryDeps{}, controllers.ThreadingDeps{})

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

func TestVimEditorRepeatHandlerNoOpWithoutCapture(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("alpha beta")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	matcher, _ := keys.NewMatcher(nil, keys.MatcherConfig{Modes: keys.NewModeStore()})
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.EditorRepeat)
	if !ok {
		t.Fatalf("registry missing %s", commands.EditorRepeat)
	}
	before := buf.String()
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeNormal, Scope: types.QUERY_EDITOR}); err != nil {
		t.Fatalf("repeat handler err = %v", err)
	}
	if buf.String() != before {
		t.Errorf("repeat with no capture mutated buffer: %q -> %q", before, buf.String())
	}
}

func TestVimEditorRepeatHandlerReRunsOperatorAtCurrentCursor(t *testing.T) {
	qec := newVimQEC(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("alpha beta gamma")},
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	matcher, _ := keys.NewMatcher(nil, keys.MatcherConfig{Modes: keys.NewModeStore()})
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Run `dw` once via direct handler calls to populate RepeatStore.
	dCmd, _ := reg.Get(commands.OperatorDelete)
	wCmd, _ := reg.Get(commands.MotionWordNext)
	if err := dCmd.Handler(commands.ExecCtx{Mode: types.ModeNormal, Scope: types.QUERY_EDITOR}); err != nil {
		t.Fatalf("d handler: %v", err)
	}
	// After `d`, mode is OperatorPending. The motion completes the pending op.
	if err := wCmd.Handler(commands.ExecCtx{Mode: types.ModeOperatorPending, Scope: types.QUERY_EDITOR}); err != nil {
		t.Fatalf("w handler in op-pending: %v", err)
	}
	if got := buf.String(); got != "beta gamma" {
		t.Fatalf("after dw: buf = %q; want %q", got, "beta gamma")
	}
	rep := qec.Repeat()
	if rep.LastOpID != commands.OperatorDelete || rep.LastMotionID != commands.MotionWordNext {
		t.Fatalf("RepeatStore = %+v; want op=delete motion=word_next", rep)
	}
	// `.` repeats at the CURRENT cursor — should delete the next word too.
	repeatCmd, _ := reg.Get(commands.EditorRepeat)
	if err := repeatCmd.Handler(commands.ExecCtx{Mode: types.ModeNormal, Scope: types.QUERY_EDITOR}); err != nil {
		t.Fatalf("repeat handler: %v", err)
	}
	if got := buf.String(); got != "gamma" {
		t.Errorf("after `.` buf = %q; want %q", got, "gamma")
	}
}
