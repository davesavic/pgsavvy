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

// newInsertQEC wires a QueryEditorContext with a real ModeStore so the
// SetMode side effect is observable by tests. The matcher is nil — the
// VimEditorController never reaches into matcher.Registers from the
// insert-mode handlers.
func newInsertQEC(t *testing.T, modes *keys.ModeStore) *context.QueryEditorContext {
	t.Helper()
	return context.NewQueryEditorContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		types.ContextTreeDeps{},
		modes,
		nil,
	)
}

func dispatchAction(t *testing.T, reg *commands.Registry, id string, ec commands.ExecCtx) {
	t.Helper()
	cmd, ok := reg.Get(id)
	if !ok {
		t.Fatalf("registry missing action %q", id)
	}
	if err := cmd.Handler(ec); err != nil {
		t.Fatalf("%s handler err = %v", id, err)
	}
}

func TestInsertEnterFlipsModeAndKeepsCursor(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello")}}
	start := editor.Position{Line: 0, Col: 3}
	buf.SetCursor(start)

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertEnter, commands.ExecCtx{Mode: types.ModeNormal})
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Fatalf("mode after InsertEnter = %v, want ModeInsert", got)
	}
	if buf.CursorPos() != start {
		t.Fatalf("InsertEnter moved cursor: got %+v, want %+v", buf.CursorPos(), start)
	}
}

func TestInsertAppendMovesCursorRightThenInserts(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hi")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertAppend, commands.ExecCtx{Mode: types.ModeNormal})
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 1}) {
		t.Fatalf("cursor after InsertAppend = %+v, want {0,1}", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Fatalf("mode after InsertAppend = %v, want ModeInsert", got)
	}
}

func TestInsertAppendClampsAtLineEnd(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hi")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 2})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertAppend, commands.ExecCtx{Mode: types.ModeNormal})
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 2}) {
		t.Fatalf("cursor after InsertAppend at line-end = %+v, want {0,2}", got)
	}
}

func TestInsertOpenBelowAppendsBlankLine(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("first")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 2})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertOpenBelow, commands.ExecCtx{Mode: types.ModeNormal})
	if got := len(buf.Lines); got != 2 {
		t.Fatalf("lines after o = %d, want 2 (%+v)", got, buf.Lines)
	}
	if string(buf.Lines[0].Runes) != "first" || len(buf.Lines[1].Runes) != 0 {
		t.Fatalf("lines after o = %q / %q, want \"first\" / \"\"", string(buf.Lines[0].Runes), string(buf.Lines[1].Runes))
	}
	if got := buf.CursorPos(); got != (editor.Position{Line: 1, Col: 0}) {
		t.Fatalf("cursor after o = %+v, want {1,0}", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Fatalf("mode after o = %v, want ModeInsert", got)
	}
}

func TestInsertOpenBelowOnEmptyBufferIsNoOp(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	// Empty Lines — Buffer.Apply on Position{0,0} insert succeeds via
	// insertAtLocked's lazy []Line{{}} seed.

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertOpenBelow, commands.ExecCtx{Mode: types.ModeNormal})
	if got := len(buf.Lines); got != 2 {
		t.Fatalf("lines after o on empty = %d, want 2", got)
	}
	if got := buf.CursorPos(); got != (editor.Position{Line: 1, Col: 0}) {
		t.Fatalf("cursor after o on empty = %+v, want {1,0}", got)
	}
}

func TestInsertOpenAbovePushesContentDown(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("first")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 3})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertOpenAbove, commands.ExecCtx{Mode: types.ModeNormal})
	if got := len(buf.Lines); got != 2 {
		t.Fatalf("lines after O = %d, want 2 (%+v)", got, buf.Lines)
	}
	if len(buf.Lines[0].Runes) != 0 || string(buf.Lines[1].Runes) != "first" {
		t.Fatalf("lines after O = %q / %q, want \"\" / \"first\"", string(buf.Lines[0].Runes), string(buf.Lines[1].Runes))
	}
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 0}) {
		t.Fatalf("cursor after O = %+v, want {0,0}", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Fatalf("mode after O = %v, want ModeInsert", got)
	}
}

func TestInsertFirstNonblankJumpsToFirstNonBlank(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("    hello")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 8})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertFirstNonblank, commands.ExecCtx{Mode: types.ModeNormal})
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 4}) {
		t.Fatalf("cursor after I = %+v, want {0,4}", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Fatalf("mode after I = %v, want ModeInsert", got)
	}
}

func TestInsertAppendEndJumpsToLineEnd(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("hello")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 1})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertAppendEnd, commands.ExecCtx{Mode: types.ModeNormal})
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 5}) {
		t.Fatalf("cursor after A = %+v, want {0,5}", got)
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Fatalf("mode after A = %v, want ModeInsert", got)
	}
}

func TestInsertAppendEndOnEmptyLineLandsAtColZero(t *testing.T) {
	modes := keys.NewModeStore()
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune{}}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.InsertAppendEnd, commands.ExecCtx{Mode: types.ModeNormal})
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 0}) {
		t.Fatalf("cursor after A on empty line = %+v, want {0,0}", got)
	}
}

func TestModeNormalExitsInsert(t *testing.T) {
	modes := keys.NewModeStore()
	modes.Set(types.QUERY_EDITOR, types.ModeInsert)
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("abc")}}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.ModeNormal, commands.ExecCtx{Mode: types.ModeInsert})
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Fatalf("mode after <esc> = %v, want ModeNormal", got)
	}
}

func TestUndoRewindsLastEdit(t *testing.T) {
	qec := newInsertQEC(t, nil)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("abc")}}
	// Apply an insert so the History has a node to undo.
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: editor.Position{Line: 0, Col: 3}, End: editor.Position{Line: 0, Col: 3}},
		Text:  "d",
	}); err != nil {
		t.Fatalf("seed Apply err = %v", err)
	}
	if string(buf.Lines[0].Runes) != "abcd" {
		t.Fatalf("seed buffer = %q, want %q", string(buf.Lines[0].Runes), "abcd")
	}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.EditorUndo, commands.ExecCtx{Mode: types.ModeNormal})
	if string(buf.Lines[0].Runes) != "abc" {
		t.Fatalf("buffer after undo = %q, want %q", string(buf.Lines[0].Runes), "abc")
	}
}

func TestRedoReplaysUndoneEdit(t *testing.T) {
	qec := newInsertQEC(t, nil)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("abc")}}
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: editor.Position{Line: 0, Col: 3}, End: editor.Position{Line: 0, Col: 3}},
		Text:  "d",
	}); err != nil {
		t.Fatalf("seed Apply err = %v", err)
	}
	if err := buf.Undo(); err != nil {
		t.Fatalf("seed Undo err = %v", err)
	}
	if string(buf.Lines[0].Runes) != "abc" {
		t.Fatalf("buffer after seed undo = %q, want %q", string(buf.Lines[0].Runes), "abc")
	}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.EditorRedo, commands.ExecCtx{Mode: types.ModeNormal})
	if string(buf.Lines[0].Runes) != "abcd" {
		t.Fatalf("buffer after redo = %q, want %q", string(buf.Lines[0].Runes), "abcd")
	}
}

func TestUndoOnEmptyHistoryIsNoOp(t *testing.T) {
	qec := newInsertQEC(t, nil)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("abc")}}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.EditorUndo, commands.ExecCtx{Mode: types.ModeNormal})
	if string(buf.Lines[0].Runes) != "abc" {
		t.Fatalf("buffer changed by undo on empty history: got %q, want %q", string(buf.Lines[0].Runes), "abc")
	}
}

func TestRedoWithoutUndoIsNoOp(t *testing.T) {
	qec := newInsertQEC(t, nil)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune("abc")}}
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: editor.Position{Line: 0, Col: 3}, End: editor.Position{Line: 0, Col: 3}},
		Text:  "d",
	}); err != nil {
		t.Fatalf("seed Apply err = %v", err)
	}

	ctrl := controllers.NewVimEditorController(qec, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dispatchAction(t, reg, commands.EditorRedo, commands.ExecCtx{Mode: types.ModeNormal})
	if string(buf.Lines[0].Runes) != "abcd" {
		t.Fatalf("redo without prior undo changed buffer: %q, want %q", string(buf.Lines[0].Runes), "abcd")
	}
}

func TestVimEditorPublishesInsertAndHistoryBindings(t *testing.T) {
	ctrl := controllers.NewVimEditorController(newInsertQEC(t, nil), nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	want := map[string]types.Mode{
		commands.InsertEnter:         types.ModeNormal,
		commands.InsertAppend:        types.ModeNormal,
		commands.InsertOpenBelow:     types.ModeNormal,
		commands.InsertOpenAbove:     types.ModeNormal,
		commands.InsertFirstNonblank: types.ModeNormal,
		commands.InsertAppendEnd:     types.ModeNormal,
		commands.ModeNormal:          types.ModeInsert,
		commands.EditorUndo:          types.ModeNormal,
		commands.EditorRedo:          types.ModeNormal,
	}
	seen := map[string]bool{}
	for _, kb := range kbs {
		wantMode, ok := want[kb.ActionID]
		if !ok {
			continue
		}
		seen[kb.ActionID] = true
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("kb %s scope = %s, want QUERY_EDITOR", kb.ActionID, kb.Scope)
		}
		if kb.Mode != wantMode {
			t.Errorf("kb %s mode = %v, want %v", kb.ActionID, kb.Mode, wantMode)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("action %q not published", id)
		}
	}
}
