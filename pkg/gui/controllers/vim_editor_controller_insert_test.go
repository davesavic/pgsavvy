package controllers_test

import (
	stdcontext "context"
	"strings"
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

// fakeSource returns a fixed candidate list filtered by the identifier
// prefix immediately left of the cursor (case-insensitive prefix match),
// so the completion engine behaves like the real prefix-filtering source
// for accept/refilter/empty-set tests.
type fakeSource struct {
	candidates []string
}

func (f fakeSource) Name() string  { return "fake" }
func (f fakeSource) Priority() int { return 100 }
func (f fakeSource) Suggest(_ stdcontext.Context, buf *editor.Buffer, pos editor.Position) []editor.Suggestion {
	prefix := prefixLeftOf(buf, pos)
	var out []editor.Suggestion
	for _, c := range f.candidates {
		if strings.HasPrefix(strings.ToLower(c), strings.ToLower(prefix)) {
			out = append(out, editor.Suggestion{Text: c, Display: c, Source: "fake"})
		}
	}
	return out
}

func prefixLeftOf(buf *editor.Buffer, pos editor.Position) string {
	lines := buf.LinesCopy()
	if pos.Line < 0 || pos.Line >= len(lines) {
		return ""
	}
	runes := lines[pos.Line].Runes
	end := pos.Col
	if end > len(runes) {
		end = len(runes)
	}
	start := end
	for start > 0 {
		r := runes[start-1]
		if r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			start--
			continue
		}
		break
	}
	return string(runes[start:end])
}

func newCompletionRig(t *testing.T, line string, col int, candidates []string) (*controllers.VimEditorController, *commands.Registry, *editor.Buffer, *context.SuggestionsContext, *keys.ModeStore) {
	t.Helper()
	modes := keys.NewModeStore()
	modes.Set(types.QUERY_EDITOR, types.ModeInsert)
	qec := newInsertQEC(t, modes)
	buf := qec.Buffer()
	buf.Lines = []editor.Line{{Runes: []rune(line)}}
	buf.SetCursor(editor.Position{Line: 0, Col: col})

	ctrl := controllers.NewVimEditorController(qec, nil)

	sugg := context.NewSuggestionsContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.SUGGESTIONS,
			ViewName: string(types.SUGGESTIONS),
			Kind:     types.TEMPORARY_POPUP,
		}),
		context.Deps{},
	)
	ctrl.SetSuggestionsContext(sugg)
	ctrl.SetCompletionEngine(editor.NewEngine([]editor.Source{fakeSource{candidates: candidates}}))

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	return ctrl, reg, buf, sugg, modes
}

func TestCompletionAcceptReplacesPartialIdentifier(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible after trigger")
	}
	// Enter via the insert seam = accept.
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("CompletionKey(Enter) returned false; want consumed")
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users" {
		t.Fatalf("line after accept = %q; want %q", got, "SELECT * FROM users")
	}
	if sugg.IsVisible() {
		t.Error("popup still visible after accept")
	}
}

func TestCompletionAcceptViaCtrlY(t *testing.T) {
	ctrl, reg, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	dispatchAction(t, reg, commands.EditorCompletionAccept, commands.ExecCtx{Mode: types.ModeInsert})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users" {
		t.Fatalf("line after <c-y> accept = %q; want %q", got, "SELECT * FROM users")
	}
	if sugg.IsVisible() {
		t.Error("popup still visible after <c-y> accept")
	}
}

func TestCompletionStaleAnchorAbortsReplace(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	// Simulate the prefix being edited out from under the popup: the user
	// deletes "us" so the cursor sits right after "FROM " — before the
	// identifier the popup was filtering. Accept must abort, not corrupt.
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM ")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 14})
	// Enter is consumed (popup was visible) but the replace must abort.
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM " {
		t.Fatalf("buffer corrupted by stale-anchor accept = %q; want unchanged", got)
	}
	if sugg.IsVisible() {
		t.Error("popup not dismissed after stale-anchor accept")
	}
}

func TestCompletionEmptyCandidateSetLeavesPopupHidden(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM zz", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("popup visible with no matching candidates; want hidden")
	}
}

func TestCompletionTabNextWrapsThenAccept(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM u", 15, []string{"users", "usage"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if sugg.Selected() != 0 {
		t.Fatalf("initial selection = %d; want 0", sugg.Selected())
	}
	// Tab via insert seam advances selection.
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyTab}) {
		t.Fatal("CompletionKey(Tab) returned false; want consumed")
	}
	if sugg.Selected() != 1 {
		t.Fatalf("selection after Tab = %d; want 1", sugg.Selected())
	}
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept returned false")
	}
	// candidates sorted by engine; index 1 must have replaced the prefix.
	got := string(buf.Lines[0].Runes)
	if got != "SELECT * FROM users" && got != "SELECT * FROM usage" {
		t.Fatalf("accept produced %q; want a full candidate", got)
	}
}

func TestCompletionTabAndEnterFallThroughWhenHidden(t *testing.T) {
	ctrl, _, _, sugg, _ := newCompletionRig(t, "SELECT", 6, []string{"users"})
	// Popup never triggered -> hidden.
	if sugg.IsVisible() {
		t.Fatal("popup unexpectedly visible")
	}
	if ctrl.CompletionKey(keys.Key{Special: keys.KeyTab}) {
		t.Error("CompletionKey(Tab) consumed key while popup hidden; want fall-through")
	}
	if ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Error("CompletionKey(Enter) consumed key while popup hidden; want fall-through")
	}
}

func TestEscDismissesPopupAndStaysInsert(t *testing.T) {
	ctrl, reg, buf, sugg, modes := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible before Esc")
	}
	dispatchAction(t, reg, commands.ModeNormal, commands.ExecCtx{Mode: types.ModeInsert})
	if sugg.IsVisible() {
		t.Error("Esc did not dismiss popup")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeInsert {
		t.Errorf("Esc with popup visible left Insert; mode = %v want ModeInsert", got)
	}
}

func TestEscWithoutPopupExitsToNormal(t *testing.T) {
	ctrl, reg, _, sugg, modes := newCompletionRig(t, "SELECT", 6, []string{"users"})
	_ = ctrl
	if sugg.IsVisible() {
		t.Fatal("popup unexpectedly visible")
	}
	dispatchAction(t, reg, commands.ModeNormal, commands.ExecCtx{Mode: types.ModeInsert})
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("Esc without popup did not exit Insert; mode = %v want ModeNormal", got)
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
		commands.ModeNormal:          types.ModeInsert | types.ModeOperatorPending,
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
