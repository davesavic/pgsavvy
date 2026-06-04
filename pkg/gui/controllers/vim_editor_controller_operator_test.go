package controllers_test

import (
	"errors"
	"testing"
	"time"

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

// --- Visual-mode paste (p replaces selection) ---

func TestVisualLinePasteReplacesSelection(t *testing.T) {
	_, reg, qec, matcher, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("line0")},
		{Runes: []rune("line1")},
		{Runes: []rune("line2")},
		{Runes: []rune("line3")},
	}
	buf.SetCursor(editor.Position{Line: 1, Col: 0})
	// V on line1, extend to line2 → line-wise selection of line1+line2.
	editor.EnterVisual(buf, types.ModeVisualLine)
	editor.ExtendSelection(buf, editor.Position{Line: 2, Col: 0})
	matcher.Registers().Set('"', "REPLACED\n") // line-wise register

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeVisualLine})

	want := []string{"line0", "REPLACED", "line3"}
	if len(buf.Lines) != len(want) {
		t.Fatalf("Lines after visual-line paste = %d, want %d", len(buf.Lines), len(want))
	}
	for i, w := range want {
		if got := string(buf.Lines[i].Runes); got != w {
			t.Errorf("Line %d = %q, want %q", i, got, w)
		}
	}
	if buf.Selection != nil {
		t.Errorf("Selection still set after visual paste, want nil (ExitVisual)")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode = %v, want Normal", got)
	}
}

func TestVisualLinePastePreservesRegister(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("line0")},
		{Runes: []rune("line1")},
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisualLine)
	matcher.Registers().Set('"', "REPLACED\n")

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeVisualLine})

	if got := matcher.Registers().Get('"'); got != "REPLACED\n" {
		t.Errorf("register \" after visual paste = %q, want unchanged %q", got, "REPLACED\n")
	}
}

func TestVisualLinePasteAtEndOfBuffer(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("line0")},
		{Runes: []rune("line1")},
		{Runes: []rune("line2")},
		{Runes: []rune("line3")},
	}
	buf.SetCursor(editor.Position{Line: 2, Col: 0})
	editor.EnterVisual(buf, types.ModeVisualLine)
	editor.ExtendSelection(buf, editor.Position{Line: 3, Col: 0})
	matcher.Registers().Set('"', "REPLACED\n")

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeVisualLine})

	want := []string{"line0", "line1", "REPLACED"}
	if len(buf.Lines) != len(want) {
		t.Fatalf("Lines after end-of-buffer visual paste = %d, want %d", len(buf.Lines), len(want))
	}
	for i, w := range want {
		if got := string(buf.Lines[i].Runes); got != w {
			t.Errorf("Line %d = %q, want %q", i, got, w)
		}
	}
}

func TestVisualCharPasteReplacesSelection(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 6})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 11})
	matcher.Registers().Set('"', "vim") // char-wise register

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeVisual})

	if got := string(buf.Lines[0].Runes); got != "hello vim" {
		t.Errorf("after visual-char paste = %q, want %q", got, "hello vim")
	}
}

func TestVisualPasteEmptyRegisterIsNoOp(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{
		{Runes: []rune("line0")},
		{Runes: []rune("line1")},
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisualLine)
	editor.ExtendSelection(buf, editor.Position{Line: 1, Col: 0})

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeVisualLine})

	if len(buf.Lines) != 2 {
		t.Fatalf("buffer mutated by empty-register visual paste: %d lines, want 2", len(buf.Lines))
	}
	if buf.Selection != nil {
		t.Errorf("Selection still set after visual paste, want nil")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode = %v, want Normal", got)
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

// --- clipboard mirror (unnamedplus, yank-only) ---

// fakeClipboard records Write calls and lets tests stub Read's
// return/error. It satisfies clipboard.Clipboard.
type fakeClipboard struct {
	writes    []string // every value passed to Write
	writeErr  error    // returned from Write when non-nil
	readVal   string   // returned from Read
	readErr   error    // returned from Read when non-nil
	readCalls int
}

func (f *fakeClipboard) Write(text string) error {
	f.writes = append(f.writes, text)
	return f.writeErr
}

func (f *fakeClipboard) Read() (string, error) {
	f.readCalls++
	return f.readVal, f.readErr
}

// doubledYank performs `yy` on the current line via the normal -> op-pending
// doubled-shortcut path.
func doubledYank(t *testing.T, reg *commands.Registry, register rune) {
	t.Helper()
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal, Register: register})
	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeOperatorPending, Register: register})
}

// doubledDelete performs `dd` on the current line.
func doubledDelete(t *testing.T, reg *commands.Registry, register rune) {
	t.Helper()
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal, Register: register})
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeOperatorPending, Register: register})
}

// TestClipboardYankMirrors: a yank to a clipboard register mirrors the
// yanked text to the clipboard (Write called with the text).
func TestClipboardYankMirrors(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledYank(t, reg, '+')

	if len(fake.writes) != 1 || fake.writes[0] != "aaa\n" {
		t.Fatalf("clipboard writes = %q, want one write of %q", fake.writes, "aaa\n")
	}
	// Internal register also receives the text.
	if got := matcher.Registers().Get('+'); got != "aaa\n" {
		t.Errorf("register + = %q, want %q", got, "aaa\n")
	}
}

// TestClipboardVisualYankMirrors: a yank from Visual mode (operatorVisualApply
// path) mirrors the selected text to the clipboard.
func TestClipboardVisualYankMirrors(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 5})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeVisual, Register: '+'})

	if len(fake.writes) != 1 || fake.writes[0] != "hello" {
		t.Fatalf("clipboard writes = %q, want one write of %q", fake.writes, "hello")
	}
	if got := matcher.Registers().Get('+'); got != "hello" {
		t.Errorf("register + = %q, want %q", got, "hello")
	}
}

// TestClipboardVisualDeleteDoesNotMirror: a delete from Visual mode never
// mirrors to the clipboard, but the internal register still receives the text.
func TestClipboardVisualDeleteDoesNotMirror(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 5})

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeVisual, Register: 0})

	if len(fake.writes) != 0 {
		t.Fatalf("visual delete mirrored to clipboard: writes = %q, want none", fake.writes)
	}
	if got := matcher.Registers().Get('"'); got != "hello" {
		t.Errorf("register \" after Visual+d = %q, want %q", got, "hello")
	}
}

// TestClipboardUnnamedYankMirrors: a yank to the unnamed register (reg==0,
// i.e. plain `yy`) mirrors to the clipboard under unnamedplus.
func TestClipboardUnnamedYankMirrors(t *testing.T) {
	qec, _, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, nil) // nil matcher: register store is a no-op
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledYank(t, reg, 0) // unnamed register

	if len(fake.writes) != 1 || fake.writes[0] != "aaa\n" {
		t.Fatalf("clipboard writes = %q, want one write of %q", fake.writes, "aaa\n")
	}
}

// TestClipboardDeleteDoesNotMirror: deletes (dd and a single-line x-style
// delete) never call Write, but the internal register still receives the
// text so ddp works internally.
func TestClipboardDeleteDoesNotMirror(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}, {Runes: []rune("bbb")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// dd to the unnamed register.
	doubledDelete(t, reg, 0)

	if len(fake.writes) != 0 {
		t.Fatalf("delete mirrored to clipboard: writes = %q, want none", fake.writes)
	}
	// Internal register still holds the deleted line (ddp works internally).
	if got := matcher.Registers().Get('"'); got != "aaa\n" {
		t.Errorf("register \" after dd = %q, want %q", got, "aaa\n")
	}
}

// TestClipboardDeleteCharStyleDoesNotMirror: a char-wise delete over a
// motion (a delete operator, not yank) does not mirror.
func TestClipboardDeleteCharStyleDoesNotMirror(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// dw to the unnamed register.
	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal, Register: 0})
	runHandler(t, reg, commands.MotionWordNext, commands.ExecCtx{Mode: types.ModeOperatorPending, Register: 0})

	if len(fake.writes) != 0 {
		t.Fatalf("delete mirrored to clipboard: writes = %q, want none", fake.writes)
	}
	if got := matcher.Registers().Get('"'); got == "" {
		t.Errorf("register \" after dw = %q, want non-empty", got)
	}
}

// TestClipboardNamedRegisterNeverTouchesClipboard: "ayy then "ap never
// invokes the clipboard; paste uses the internal register.
func TestClipboardNamedRegisterNeverTouchesClipboard(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{readVal: "EXTERNAL"}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// "ayy
	doubledYank(t, reg, 'a')
	if len(fake.writes) != 0 {
		t.Fatalf("named-register yank mirrored: writes = %q, want none", fake.writes)
	}

	// "ap — paste from internal 'a', clipboard Read must not be consulted.
	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 'a'})
	if fake.readCalls != 0 {
		t.Fatalf("named-register paste read clipboard: readCalls = %d, want 0", fake.readCalls)
	}
	// The internal 'a' line-wise yank pasted below: line 1 == "aaa".
	if len(buf.Lines) < 2 || string(buf.Lines[1].Runes) != "aaa" {
		t.Errorf("after \"ap lines = %v, want second line %q", buf.Lines, "aaa")
	}
}

// TestClipboardReadExternalWins: an external clipboard change (Read err==nil,
// non-empty, != lastWrite) is what paste inserts.
func TestClipboardReadExternalWins(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{readVal: "EXTERNAL"}
	ctrl.SetClipboard(fake)

	matcher.Registers().Set('"', "internal")

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("x")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// p from unnamed register → clipboard "EXTERNAL" wins (char-wise after cursor).
	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 0})
	if got := string(buf.Lines[0].Runes); got != "xEXTERNAL" {
		t.Errorf("after p = %q, want %q", got, "xEXTERNAL")
	}
}

// TestClipboardReadEmptyFallsBack: empty clipboard read falls back to the
// internal register.
func TestClipboardReadEmptyFallsBack(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{readVal: ""}
	ctrl.SetClipboard(fake)

	matcher.Registers().Set('"', "internal")

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("x")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 0})
	if got := string(buf.Lines[0].Runes); got != "xinternal" {
		t.Errorf("after p (empty clipboard) = %q, want %q", got, "xinternal")
	}
}

// TestClipboardReadEqualsLastWriteUsesInternal: when the clipboard equals
// what we last mirrored, the internal (line-wise) register is used so yyp
// stays line-wise.
func TestClipboardReadEqualsLastWriteUsesInternal(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// yy mirrors "aaa\n" to the fake (and sets lastWrite).
	doubledYank(t, reg, 0)
	// Echo the mirrored value back as the clipboard contents.
	fake.readVal = "aaa\n"

	// p: clipboard == lastWrite → use internal line-wise register → pastes below.
	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 0})
	if len(buf.Lines) != 2 || string(buf.Lines[1].Runes) != "aaa" {
		t.Errorf("after yyp lines = %v, want line-wise paste below (second line %q)", buf.Lines, "aaa")
	}
}

// TestClipboardReadErrorFallsBack: a clipboard Read error falls back to the
// internal register (in-editor yank->p still works).
func TestClipboardReadErrorFallsBack(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{readVal: "EXTERNAL", readErr: errors.New("backend down")}
	ctrl.SetClipboard(fake)

	matcher.Registers().Set('"', "internal")

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("x")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: 0})
	if got := string(buf.Lines[0].Runes); got != "xinternal" {
		t.Errorf("after p (read error) = %q, want %q", got, "xinternal")
	}
}

// TestClipboardWriteErrorSwallowed: a clipboard Write error is swallowed —
// no panic, internal store still set, lastWrite NOT updated.
func TestClipboardWriteErrorSwallowed(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	fake := &fakeClipboard{writeErr: errors.New("write failed")}
	ctrl.SetClipboard(fake)

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledYank(t, reg, '+')

	// Internal store still set despite the Write error.
	if got := matcher.Registers().Get('+'); got != "aaa\n" {
		t.Errorf("register + after failed Write = %q, want %q", got, "aaa\n")
	}
	// lastWrite was not updated, so a clean external read (== the value)
	// would still be treated as external. Verify by reading: clipboard
	// reports "aaa\n" but lastWrite is "" so external wins (char-wise).
	fake.readVal = "aaa\n"
	fake.writeErr = nil
	buf.Lines = []editor.Line{{Runes: []rune("z")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: '+'})
	// External "aaa\n" is line-wise (trailing \n) so it pastes below as a line.
	if len(buf.Lines) != 2 || string(buf.Lines[1].Runes) != "aaa" {
		t.Errorf("after p (external wins post failed write) lines = %v, want line-wise below", buf.Lines)
	}
}

// TestClipboardNilDefaultUnchanged: with no clipboard wired (default),
// behavior is unchanged — yank writes only the internal register and paste
// reads only the internal register.
func TestClipboardNilDefaultUnchanged(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	// No SetClipboard call.

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("aaa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledYank(t, reg, '+')
	if got := matcher.Registers().Get('+'); got != "aaa\n" {
		t.Errorf("register + = %q, want %q", got, "aaa\n")
	}

	// Paste from + uses internal register (line-wise below).
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	runHandler(t, reg, commands.EditorPaste, commands.ExecCtx{Mode: types.ModeNormal, Register: '+'})
	if len(buf.Lines) < 2 || string(buf.Lines[1].Runes) != "aaa" {
		t.Errorf("after +p lines = %v, want line-wise paste below", buf.Lines)
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

// --- dbsavvy-5fxk: D (delete to end of line, vim `d$`) ---

// TestDeleteToEndOfLine drives the single-keystroke `D` action in Normal
// mode: it deletes from the cursor to the end of the current line,
// char-wise, leaving the mode in Normal (delete is not change).
func TestDeleteToEndOfLine(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 6})

	runHandler(t, reg, commands.OperatorDeleteEndOfLine, commands.ExecCtx{Mode: types.ModeNormal})

	if got := string(buf.Lines[0].Runes); got != "hello " {
		t.Errorf("after D = %q, want %q", got, "hello ")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode after D = %v, want ModeNormal", got)
	}
}

// TestDeleteToEndOfLineWritesRegister confirms the deleted span lands in
// the unnamed register so it can be pasted back (vim parity with d$).
func TestDeleteToEndOfLineWritesRegister(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 6})

	runHandler(t, reg, commands.OperatorDeleteEndOfLine, commands.ExecCtx{Mode: types.ModeNormal})

	if got := matcher.Registers().Get('"'); got != "world" {
		t.Errorf("register \" after D = %q, want %q", got, "world")
	}
}

// TestDeleteToEndOfLineAtLineEndIsNoOp guards the boundary: with the
// cursor already at end-of-line there is nothing to delete.
func TestDeleteToEndOfLineAtLineEndIsNoOp(t *testing.T) {
	_, reg, qec, _, modes := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 5}) // past-end of "hello"

	runHandler(t, reg, commands.OperatorDeleteEndOfLine, commands.ExecCtx{Mode: types.ModeNormal})

	if got := string(buf.Lines[0].Runes); got != "hello" {
		t.Errorf("after D at line end = %q, want %q (no-op)", got, "hello")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("mode after D no-op = %v, want ModeNormal", got)
	}
}

// TestDeleteToEndOfLinePublishedNormalOnly asserts the `D` binding is
// published under QUERY_EDITOR scope for Normal mode.
func TestDeleteToEndOfLinePublishedNormalOnly(t *testing.T) {
	ctrl, _, _, _, _ := opCtrl(t)
	var found bool
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb == nil || kb.ActionID != commands.OperatorDeleteEndOfLine {
			continue
		}
		found = true
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("D scope = %s, want QUERY_EDITOR", kb.Scope)
		}
		if len(kb.Sequence) != 1 || kb.Sequence[0].Code != 'D' {
			t.Errorf("D sequence = %+v, want ['D']", kb.Sequence)
		}
		if kb.Mode != types.ModeNormal {
			t.Errorf("D mode = %v, want ModeNormal", kb.Mode)
		}
	}
	if !found {
		t.Fatal("VimEditorController did not publish a binding for operator.delete_eol")
	}
}

// --- TextObject word objects: operator-agnostic + repeat state ---

func TestOperatorDeleteInnerWord(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("foo bar baz")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 5}) // inside "bar"

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectInnerWord, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "foo  baz" {
		t.Errorf("after diw = %q, want %q", got, "foo  baz")
	}
}

func TestOperatorDeleteAroundWord(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("foo bar baz")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 5}) // inside "bar"

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectAroundWord, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := string(buf.Lines[0].Runes); got != "foo baz" {
		t.Errorf("after daw = %q, want %q", got, "foo baz")
	}
}

func TestOperatorYankInnerWORDSpansPunctuation(t *testing.T) {
	_, reg, qec, matcher, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("foo.bar baz")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 1})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectInnerWORD, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if got := matcher.Registers().Get('"'); got != "foo.bar" {
		t.Errorf("register after yiW = %q, want %q", got, "foo.bar")
	}
}

func TestChangeInnerWordSetsRepeatState(t *testing.T) {
	_, reg, qec, _, _ := opCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("foo bar baz")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 5})

	runHandler(t, reg, commands.OperatorChange, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.TextObjectInnerWord, commands.ExecCtx{Mode: types.ModeOperatorPending})

	rep := qec.Repeat()
	if rep.LastOpID != commands.OperatorChange || rep.LastTextObjectID != commands.TextObjectInnerWord {
		t.Fatalf("repeat state = {op:%q to:%q}, want {op:%q to:%q}",
			rep.LastOpID, rep.LastTextObjectID, commands.OperatorChange, commands.TextObjectInnerWord)
	}
}

// --- dbsavvy-o6da: post-yank flash fired only on the yank operator ---

// fakeYankFlasher records the (buf, range) of the last Flash call so the
// operator tests can assert the flash fires (and with the expected span) on
// yank paths and never on delete/change paths.
type fakeYankFlasher struct {
	calls   int
	lastBuf *editor.Buffer
	lastR   editor.Range
}

func (f *fakeYankFlasher) Flash(buf *editor.Buffer, r editor.Range, _ time.Duration) {
	f.calls++
	f.lastBuf = buf
	f.lastR = r
}

// flashCtrl builds a controller wired with a fake YankFlasher.
func flashCtrl(t *testing.T) (*controllers.VimEditorController, *commands.Registry, *context.QueryEditorContext, *fakeYankFlasher) {
	t.Helper()
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	flasher := &fakeYankFlasher{}
	ctrl.SetYankFlasher(flasher)
	return ctrl, reg, qec, flasher
}

// POSITIVE: `yy` (doubled linewise) flashes the whole-line LineWise range.
func TestYankFlashDoubledYank(t *testing.T) {
	_, reg, qec, flasher := flashCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledYank(t, reg, 0)

	if flasher.calls != 1 {
		t.Fatalf("Flash calls = %d, want 1", flasher.calls)
	}
	if !flasher.lastR.LineWise {
		t.Errorf("yy flash range LineWise = false, want true")
	}
	if flasher.lastR.Start.Line != 0 || flasher.lastR.End.Line != 0 {
		t.Errorf("yy flash range = %+v, want single line 0", flasher.lastR)
	}
}

// POSITIVE: `yw` (op-pending motion) flashes the half-open motion span.
func TestYankFlashOpPendingMotion(t *testing.T) {
	_, reg, qec, flasher := flashCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.MotionWordNext, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if flasher.calls != 1 {
		t.Fatalf("Flash calls = %d, want 1", flasher.calls)
	}
	// `yw` over "hello world" yanks "hello " → half-open [0,0)-[0,6).
	want := editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 6}}
	if flasher.lastR.Start != want.Start || flasher.lastR.End != want.End {
		t.Errorf("yw flash range = %+v, want %+v", flasher.lastR, want)
	}
}

// POSITIVE: visual `y` flashes the yanked charwise span as half-open. The
// selection End (cursor col 5) is already the half-open end of the yanked
// text ("hello", cols 0..4), so NO endCol++ — the flash end stays at 5.
func TestYankFlashVisualCharwise(t *testing.T) {
	_, reg, qec, flasher := flashCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 5})

	runHandler(t, reg, commands.OperatorYank, commands.ExecCtx{Mode: types.ModeVisual})

	if flasher.calls != 1 {
		t.Fatalf("Flash calls = %d, want 1", flasher.calls)
	}
	// Selection captured BEFORE ExitVisual; matches the yanked "hello" span.
	if buf.Selection != nil {
		t.Errorf("Selection still set after visual yank, want nil")
	}
	want := editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 5}}
	if flasher.lastR.Start != want.Start || flasher.lastR.End != want.End {
		t.Errorf("visual-y flash range = %+v, want %+v (half-open, no endCol++)", flasher.lastR, want)
	}
	if flasher.lastR.LineWise {
		t.Errorf("visual charwise yank flash LineWise = true, want false")
	}
}

// NEGATIVE: `dd` produces a non-empty capture but must NOT flash.
func TestYankFlashDoubledDeleteNoFlash(t *testing.T) {
	_, reg, qec, flasher := flashCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledDelete(t, reg, 0)

	if flasher.calls != 0 {
		t.Fatalf("dd flashed (%d calls), want 0", flasher.calls)
	}
}

// NEGATIVE: `cw` (op-pending change) must NOT flash.
func TestYankFlashOpPendingChangeNoFlash(t *testing.T) {
	_, reg, qec, flasher := flashCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	runHandler(t, reg, commands.OperatorChange, commands.ExecCtx{Mode: types.ModeNormal})
	runHandler(t, reg, commands.MotionWordNext, commands.ExecCtx{Mode: types.ModeOperatorPending})

	if flasher.calls != 0 {
		t.Fatalf("cw flashed (%d calls), want 0", flasher.calls)
	}
}

// NEGATIVE: visual `d` must NOT flash.
func TestYankFlashVisualDeleteNoFlash(t *testing.T) {
	_, reg, qec, flasher := flashCtrl(t)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello world")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	editor.EnterVisual(buf, types.ModeVisual)
	editor.ExtendSelection(buf, editor.Position{Line: 0, Col: 5})

	runHandler(t, reg, commands.OperatorDelete, commands.ExecCtx{Mode: types.ModeVisual})

	if flasher.calls != 0 {
		t.Fatalf("visual delete flashed (%d calls), want 0", flasher.calls)
	}
}

// nil flasher: a yank with no flasher wired still writes the register and
// does not panic.
func TestYankFlashNilFlasherNoPanic(t *testing.T) {
	qec, matcher, _ := newOperatorQEC(t)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	// No SetYankFlasher call: flasher stays nil.

	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("only line")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	doubledYank(t, reg, 0)

	if got := matcher.Registers().Get('"'); got != "only line\n" {
		t.Errorf("register \" = %q, want %q (yank must still write with nil flasher)", got, "only line\n")
	}
}
