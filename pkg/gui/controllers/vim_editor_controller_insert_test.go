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

// TestCompletionShiftTabPrevWraps pins that Shift+Tab (Backtab) via the
// insert seam moves the selection backward, wrapping from the first
// candidate to the last — the mirror of Tab -> Next.
func TestCompletionShiftTabPrevWraps(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM u", 15, []string{"users", "usage"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if sugg.Selected() != 0 {
		t.Fatalf("initial selection = %d; want 0", sugg.Selected())
	}
	// Shift+Tab from the first entry wraps to the last.
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyBacktab}) {
		t.Fatal("CompletionKey(Backtab) returned false; want consumed")
	}
	if sugg.Selected() != 1 {
		t.Fatalf("selection after Shift+Tab = %d; want 1 (wrapped to last)", sugg.Selected())
	}
	// And forward again returns to the first.
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyBacktab}) {
		t.Fatal("CompletionKey(Backtab) returned false; want consumed")
	}
	if sugg.Selected() != 0 {
		t.Fatalf("selection after second Shift+Tab = %d; want 0", sugg.Selected())
	}
}

// TestCompletionShiftTabFallsThroughWhenHidden pins that Backtab is not
// consumed while the popup is hidden (keeps its normal Insert meaning).
func TestCompletionShiftTabFallsThroughWhenHidden(t *testing.T) {
	ctrl, _, _, sugg, _ := newCompletionRig(t, "SELECT * FROM u", 15, []string{"users", "usage"})
	if sugg.IsVisible() {
		t.Fatal("popup unexpectedly visible before refilter")
	}
	if ctrl.CompletionKey(keys.Key{Special: keys.KeyBacktab}) {
		t.Error("CompletionKey(Backtab) consumed key while popup hidden; want fall-through")
	}
}

// TestAutoTriggerOpensInFromContext pins the dbsavvy-etp.4 gate: with the
// popup hidden, AutoTrigger opens it only when the cursor sits at an
// AutoTriggerFromContext position (here `FROM us`).
func TestAutoTriggerOpensInFromContext(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	if sugg.IsVisible() {
		t.Fatal("popup unexpectedly visible before auto-trigger")
	}
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("AutoTrigger did not open popup in FROM context")
	}
}

// TestAutoTriggerNoPopupOutsideGate pins that AutoTrigger does NOT open
// the popup for a bare identifier with no governing clause keyword,
// operator, or `<ident>.` context — even though candidates exist — so it
// stays gated rather than prefix-everywhere. (A clause position such as
// `SELECT us` or `WHERE us` IS in-gate; see the column-context cases.)
func TestAutoTriggerNoPopupOutsideGate(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "us", 2, []string{"users"})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("AutoTrigger opened popup outside the context gate; want hidden")
	}
}

// TestAutoTriggerRefiltersWhileVisible pins the in-place refilter: once
// the popup is visible, AutoTrigger refilters at the new cursor even when
// the line no longer satisfies AutoTriggerFromContext on its own (the
// visible-popup branch bypasses the open-gate). Simulates typing `e`
// after `us` to narrow `us` -> `use`.
func TestAutoTriggerRefiltersWhileVisible(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users", "usage"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if len(sugg.Suggestions()) != 2 {
		t.Fatalf("initial candidate count = %d; want 2", len(sugg.Suggestions()))
	}
	// Type 'e' -> "use" narrows to "users" only (usage drops).
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM use")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 17})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup dismissed during refilter; want still visible")
	}
	if len(sugg.Suggestions()) != 1 {
		t.Fatalf("candidate count after refilter = %d; want 1 (narrowed to users)", len(sugg.Suggestions()))
	}
}

// TestAutoTriggerBackspaceRefiltersToEmptyDismisses pins the edge path:
// backspacing the partial identifier down to a non-matching prefix (here
// to a candidate set of zero) dismisses the popup cleanly rather than
// leaving it stale. dbsavvy-etp.4.
func TestAutoTriggerBackspaceRefiltersToEmptyDismisses(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM usz", 17, []string{"users"})
	// Open with a matching prefix first.
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM us")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 16})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible before backspace refilter")
	}
	// Now the live buffer holds a non-matching prefix "usz"; AutoTrigger
	// (as fired by the Backspace hook) refilters -> empty -> Hide.
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM usz")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 17})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("popup left stale after refilter to empty set; want dismissed")
	}
}

// TestAutoTriggerNoRePopupAfterAccept pins the re-popup-after-accept
// guard: after accepting a candidate the popup is hidden, and the accept
// itself does not fire AutoTrigger (accept routes through CompletionKey /
// <c-y>, never the printable/backspace seam). A subsequent AutoTrigger
// from the just-inserted full identifier — which no longer ends in a
// trigger boundary — must NOT re-open the popup. dbsavvy-etp.4.
func TestAutoTriggerNoRePopupAfterAccept(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	if sugg.IsVisible() {
		t.Fatal("popup still visible immediately after accept")
	}
	// Re-evaluate at the post-accept cursor (end of "users"): hidden popup
	// + line ends in a complete identifier, not a trigger boundary -> no
	// re-open.
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("AutoTrigger re-opened popup after accept; want suppressed")
	}
}

// TestAutoTriggerDotAfterAcceptOpensColumns pins the dot-after-accept fix:
// accepting a table name arms the post-accept suppression (so the inserted
// identifier does not immediately re-pop the table list), but typing `.`
// right after is an explicit `<ident>.` column trigger that MUST still open
// the popup. Typing the table name out by hand never arms the flag, so the
// dot always worked there — this pins parity between the two paths.
// Regression for the "accept posts_summary then `.` shows nothing" bug.
func TestAutoTriggerDotAfterAcceptOpensColumns(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM posts_summar", 26, []string{"posts_summary"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	if sugg.IsVisible() {
		t.Fatal("popup still visible immediately after accept")
	}
	// Simulate typing `.` immediately after the accepted identifier; the
	// suppression flag is armed and the `.` is the next keystroke.
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM posts_summary.")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 28})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("`.` after accept did not open the column popup; suppression swallowed the dot trigger")
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

func TestEscDismissesPopupAndExitsToNormal(t *testing.T) {
	ctrl, reg, buf, sugg, modes := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible before Esc")
	}
	dispatchAction(t, reg, commands.ModeNormal, commands.ExecCtx{Mode: types.ModeInsert})
	if sugg.IsVisible() {
		t.Error("exit action did not dismiss popup")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("exit action with popup visible did not exit Insert; mode = %v want ModeNormal", got)
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
		commands.EditorUndo:          types.ModeNormal,
		commands.EditorRedo:          types.ModeNormal,
	}
	// mode.normal is published TWICE: `<esc>` (Insert|OperatorPending) and
	// the `jk` alias (Insert only). They share an ActionID, so assert by
	// sequence.
	jkSeq, err := keys.SequenceFromShorthand("jk")
	if err != nil {
		t.Fatalf("SequenceFromShorthand(jk): %v", err)
	}
	escSeq, err := keys.SequenceFromShorthand("<esc>")
	if err != nil {
		t.Fatalf("SequenceFromShorthand(<esc>): %v", err)
	}
	seqEq := func(a, b []keys.Key) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	seen := map[string]bool{}
	var sawJK, sawEsc bool
	for _, kb := range kbs {
		if kb.ActionID == commands.ModeNormal {
			if kb.Scope != types.QUERY_EDITOR {
				t.Errorf("mode.normal scope = %s, want QUERY_EDITOR", kb.Scope)
			}
			switch {
			case seqEq(kb.Sequence, jkSeq):
				sawJK = true
				if kb.Mode != types.ModeInsert {
					t.Errorf("jk mode = %v, want %v", kb.Mode, types.ModeInsert)
				}
			case seqEq(kb.Sequence, escSeq):
				sawEsc = true
				if kb.Mode != types.ModeInsert|types.ModeOperatorPending {
					t.Errorf("<esc> mode = %v, want %v", kb.Mode, types.ModeInsert|types.ModeOperatorPending)
				}
			}
			continue
		}
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
	if !sawJK {
		t.Errorf("jk mode.normal binding not published")
	}
	if !sawEsc {
		t.Errorf("<esc> mode.normal binding not published")
	}
}
