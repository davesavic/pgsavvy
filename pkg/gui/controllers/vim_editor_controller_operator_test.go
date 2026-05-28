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

// newOperatorQEC wires a QueryEditorContext with a real ModeStore and
// returns the (context, matcher) pair the operator tests need to assert
// both mode flips and register writes.
func newOperatorQEC(t *testing.T) (*context.QueryEditorContext, *keys.Matcher, *keys.ModeStore) {
	t.Helper()
	modes := keys.NewModeStore()
	matcher, err := keys.NewMatcher(nil, keys.MatcherConfig{Modes: modes})
	if err != nil {
		t.Fatalf("NewMatcher err = %v", err)
	}
	qec := context.NewQueryEditorContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		types.ContextTreeDeps{},
		modes,
		matcher,
	)
	return qec, matcher, modes
}

func opCtrl(t *testing.T) (*controllers.VimEditorController, *commands.Registry, *context.QueryEditorContext, *keys.Matcher, *keys.ModeStore) {
	t.Helper()
	qec, matcher, modes := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	return ctrl, reg, qec, matcher, modes
}

func runHandler(t *testing.T, reg *commands.Registry, id string, ec commands.ExecCtx) {
	t.Helper()
	cmd, ok := reg.Get(id)
	if !ok {
		t.Fatalf("registry missing %q", id)
	}
	if err := cmd.Handler(ec); err != nil {
		t.Fatalf("%s handler err = %v", id, err)
	}
}

// --- Normal mode: operator stashes + sets OperatorPending ---

func TestOperatorDeleteInNormalSetsOpPending(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})

	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeOperatorPending {
		t.Errorf("mode = %v, want ModeOperatorPending", got)
	}
	if got := qec.Repeat().PendingOpID; got != commands.OperatorDelete {
		t.Errorf("PendingOpID = %q, want %q", got, commands.OperatorDelete)
	}
}

func TestOperatorDeleteCompletesViaMotionWordNext(t *testing.T) {
	_, reg, qec, matcher, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// 1. d in Normal.
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})

	// 2. motion.word_next in OperatorPending — completes the dispatch.
	runHandler(t, reg, commands.MotionWordNext, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "world" {
		t.Errorf("buffer after dw = %q, want %q", got, "world")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode after dw = %v, want Normal", got)
	}
	cut := matcher.Registers().Get('"')
	if cut != "hello " {
		t.Errorf("register \" = %q, want %q", cut, "hello ")
	}
	if got := qec.Repeat().LastOpID; got != commands.OperatorDelete {
		t.Errorf("LastOpID = %q, want %q", got, commands.OperatorDelete)
	}
}

// --- dd / yy / cc / >> / << — doubled-operator linewise variants ---

func TestOperatorDoubledDeleteLinewise(t *testing.T) {
	_, reg, qec, matcher, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("first")},
		{Runes: []rune("second")},
		{Runes: []rune("third")},
	}
	buf.SetCursor(editor.Position{Line: 1, Col: 2})

	// 1. d in Normal → op-pending.
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	// 2. d again in OperatorPending → linewise current line.
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if len(buf.Lines) != 2 {
		t.Fatalf("Lines after dd = %d, want 2", len(buf.Lines))
	}
	if got := matcher.Registers().Get('"'); got != "second\n" {
		t.Errorf("register \" = %q, want \"second\\n\"", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode = %v, want Normal", got)
	}
}

func TestOperatorDoubledYankDoesNotMutate(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("only line")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "only line" {
		t.Errorf("yy mutated buffer to %q (must not mutate)", got)
	}
	if got := matcher.Registers().Get('"'); got != "only line\n" {
		t.Errorf("register \" = %q, want %q", got, "only line\n")
	}
}

func TestOperatorDoubledChangeEndsInInsertMode(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}, {Runes: []rune("bbb")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorChange, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.OperatorChange, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Errorf("mode after cc = %v, want ModeInsert", got)
	}
}

func TestOperatorDoubledIndentRight(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}, {Runes: []rune("bbb")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorIndentRight, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.OperatorIndentRight, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "  aaa" {
		t.Errorf("Line 0 after >> = %q, want %q", got, "  aaa")
	}
}

func TestOperatorDoubledIndentRightWithCount(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("aaa")},
		{Runes: []rune("bbb")},
		{Runes: []rune("ccc")},
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorIndentRight, commands.ExecCtx{Mode: types.ModeNormal, Count: 3})
	runHandler(t, reg, commands.OperatorIndentRight, commands.ExecCtx{Mode: types.ModeOperatorPending, Count: 3})

	wants := []string{"  aaa", "  bbb", "  ccc"}
	for i, want := range wants {
		if got := string(buf.Lines[i].Runes); got != want {
			t.Errorf("Line %d = %q, want %q", i, got, want)
		}
	}
}

func TestOperatorDoubledIndentLeftAtColZeroIsNoOp(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorIndentLeft, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.OperatorIndentLeft, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "aaa" {
		t.Errorf("Line after << at col 0 = %q, want unchanged", got)
	}
}

// --- Visual + operator: bypass op-pending ---

func TestOperatorVisualConsumesSelection(t *testing.T) {
	_, reg, qec, matcher, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 5})

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeVisual})

	if got := string(buf.Lines[0].Runes); got != " world" {
		t.Errorf("Visual+d buffer = %q, want %q", got, " world")
	}
	if got := matcher.Registers().Get('"'); got != "hello" {
		t.Errorf("register \" = %q, want %q", got, "hello")
	}
	if buf.Selection != nil {
		t.Errorf("Selection still set after Visual+d, want nil (ExitVisual)")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode = %v, want Normal", got)
	}
}

func TestOperatorVisualChangeEndsInInsert(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa bbb")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 3})

	runHandler(t, reg, commands.OperatorChange, commands.ExecCtx{Mode: types.ModeVisual})

	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Errorf("mode after Visual c = %v, want ModeInsert", got)
	}
}

func TestOperatorVisualDeleteEmptySelectionExits(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello")}}
	editor.EnterVisual(buf, types.ModeVisual)
	// Selection is anchor-only (zero width).

	before := string(buf.Lines[0].Runes)
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeVisual})

	if got := string(buf.Lines[0].Runes); got != before {
		t.Errorf("buffer after Visual+d on empty selection = %q, want unchanged %q", got, before)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode = %v, want Normal", got)
	}
}

// --- Registers: default '"' + named '"a' ---

func TestOperatorYankToNamedRegister(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// yy → "a
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal, Register: 'a'})
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeOperatorPending, Register: 'a'})

	if got := matcher.Registers().Get('a'); got != "aaa\n" {
		t.Errorf("register a = %q, want %q", got, "aaa\n")
	}
	if got := matcher.Registers().Get('"'); got != "" {
		t.Errorf("register \" = %q, want empty (named register suppresses default)", got)
	}
}

func TestPasteFromNamedRegister(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}, {Runes: []rune("bbb")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	matcher.Registers().Set('a', "XX")

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 'a'})

	if got := string(buf.Lines[0].Runes); got != "aXXaa" {
		t.Errorf("after \"ap = %q, want %q", got, "aXXaa")
	}
}

func TestPasteFromEmptyRegisterIsNoOp(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 1})

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 'a'})

	if got := string(buf.Lines[0].Runes); got != "aaa" {
		t.Errorf("paste from empty register mutated buffer to %q", got)
	}
}

func TestPasteLinewiseInsertsOnNewLine(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}, {Runes: []rune("bbb")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 1})
	matcher.Registers().Set('"', "XX\n") // linewise (trailing \n)

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal})

	if len(buf.Lines) != 3 {
		t.Fatalf("Lines after linewise paste = %d, want 3", len(buf.Lines))
	}
	if got := string(buf.Lines[1].Runes); got != "XX" {
		t.Errorf("Line 1 after linewise paste = %q, want XX", got)
	}
}

// --- Mode bounds + cancel path ---

func TestEscClearsOperatorPending(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	if got := qec.Repeat().PendingOpID; got != commands.OperatorDelete {
		t.Fatalf("PendingOpID = %q, want OperatorDelete", got)
	}

	// <esc> in OperatorPending → mode.normal handler.
	runHandler(t, reg, commands.ModeNormal, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := qec.Repeat().PendingOpID; got != "" {
		t.Errorf("PendingOpID after <esc> = %q, want empty", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode after <esc> = %v, want Normal", got)
	}
	_ = buf
}

// --- TextObject + operator: yip ---

func TestOperatorTextObjectYankInnerParagraph(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("first line")},
		{Runes: []rune("second")},
		{Runes: []rune("")},
		{Runes: []rune("other")},
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectInnerParagraph, commands.ExecCtx{Mode: types.ModeOperatorPending})

	got := matcher.Registers().Get('"')
	if got != "first line\nsecond" {
		t.Errorf("register after yip = %q, want %q", got, "first line\nsecond")
	}
}

// --- Bindings: assert default chords are published ---

func TestOperatorBindingsArePublishedInModeMask(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	wantNonNormal := types.ModeOperatorPending |
		types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock
	wantIDs := map[string]bool{
		commands.OperatorDelete:      false,
		commands.OperatorYank:        false,
		commands.OperatorChange:      false,
		commands.OperatorUpper:       false,
		commands.OperatorLower:       false,
		commands.OperatorIndentRight: false,
		commands.OperatorIndentLeft:  false,
	}
	seenNormal := map[string]bool{}
	seenNonNormal := map[string]bool{}
	for _, kb := range kbs {
		if _, ok := wantIDs[kb.ActionID]; !ok {
			continue
		}
		wantIDs[kb.ActionID] = true
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("operator %s scope = %s, want QUERY_EDITOR", kb.ActionID, kb.Scope)
		}
		switch kb.Mode {
		case types.ModeNormal:
			seenNormal[kb.ActionID] = true
		case wantNonNormal:
			seenNonNormal[kb.ActionID] = true
		default:
			t.Errorf("operator %s unexpected mode = %v", kb.ActionID, kb.Mode)
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("operator %s not published", id)
			continue
		}
		if !seenNormal[id] {
			t.Errorf("operator %s missing ModeNormal binding", id)
		}
		if !seenNonNormal[id] {
			t.Errorf("operator %s missing non-Normal mode binding", id)
		}
	}
}

// --- +/* clipboard one-shot toast ---

func TestClipboardOneShotToastOnYank(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	var toasts []string
	ctrl.SetToaster(func(msg string) { toasts = append(toasts, msg) })

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// First "+y on a doubled-y → emits one toast.
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal, Register: '+'})
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeOperatorPending, Register: '+'})

	if len(toasts) != 1 {
		t.Fatalf("toast count after first +y = %d, want 1", len(toasts))
	}

	// Second use: silent fallthrough.
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal, Register: '*'})
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeOperatorPending, Register: '*'})

	if len(toasts) != 1 {
		t.Errorf("toast count after second use = %d, want 1 (one-shot)", len(toasts))
	}

	// In-memory fallback still records the text.
	if got := matcher.Registers().Get('+'); got != "aaa\n" {
		t.Errorf("register + = %q, want %q", got, "aaa\n")
	}
}

// --- AC-trace: explicit AC bullets exercised end-to-end ---

func TestACChangeEndOfLineFlipsToInsert(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 6})

	// c$ — change to end of line.
	runHandler(t, reg, commands.OperatorChange, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.MotionLineEnd, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "hello " {
		t.Errorf("after c$ = %q, want %q", got, "hello ")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Errorf("mode after c$ = %v, want ModeInsert", got)
	}
}

func TestACUppercaseNextWord(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// gUw — uppercase next word.
	runHandler(t, reg, commands.OperatorUpper, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.MotionWordNext, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "HELLO world" {
		t.Errorf("after gUw = %q, want %q", got, "HELLO world")
	}
}

func TestACLowercaseNextWord(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("HELLO WORLD")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorLower, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.MotionWordNext, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "hello WORLD" {
		t.Errorf("after guw = %q, want %q", got, "hello WORLD")
	}
}

func TestACDeleteAroundStatement(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("SELECT 1; SELECT 2;")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 12}) // inside "SELECT 2"

	// das — delete around statement (consumes the trailing `;`).
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectAroundStatement, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "SELECT 1;" {
		t.Errorf("after das = %q, want %q", got, "SELECT 1;")
	}
}

func TestACYankAroundParagraph(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("aaa")},
		{Runes: []rune("bbb")},
		{Runes: []rune("")},
		{Runes: []rune("ccc")},
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectAroundParagraph, commands.ExecCtx{Mode: types.ModeOperatorPending})

	got := matcher.Registers().Get('"')
	// AroundParagraph includes trailing blank line.
	if got != "aaa\nbbb\n" {
		t.Errorf("yap register = %q, want %q", got, "aaa\nbbb\n")
	}
}

func TestACDdOnSingleLineLeavesEmptyLine(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("only line")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if len(buf.Lines) != 1 {
		t.Fatalf("Lines after dd on single line = %d, want 1 (empty)", len(buf.Lines))
	}
	if got := string(buf.Lines[0].Runes); got != "" {
		t.Errorf("Lines[0] = %q, want \"\"", got)
	}
}

func TestHandleFocusLostClearsPendingOpID(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	if qec.Repeat().PendingOpID == "" {
		t.Fatalf("PendingOpID empty after operator stash (test setup wrong)")
	}

	if err := qec.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost err = %v", err)
	}
	if got := qec.Repeat().PendingOpID; got != "" {
		t.Errorf("PendingOpID after HandleFocusLost = %q, want empty", got)
	}
}

// --- Motion-at-boundary in OperatorPending still completes ---

func TestOperatorMotionAtBoundaryAppliesZeroRange(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// d in Normal.
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	// gg from {0,0} returns ok=false; motionHandler must still clear
	// pending + reset mode rather than strand OperatorPending.
	runHandler(t, reg, commands.MotionBufferStart, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode after dgg-at-bof = %v, want Normal", got)
	}
	if got := qec.Repeat().PendingOpID; got != "" {
		t.Errorf("PendingOpID after dgg-at-bof = %q, want empty", got)
	}
}
