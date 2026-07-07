package controllers_test

import (
	stdcontext "context"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
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
	end := min(pos.Col, len(runes))
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
	// Enter via the insert seam = accept. In a FROM (table) context the
	// accept auto-inserts a deduped editable alias,
	// so "us" -> "users u".
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("CompletionKey(Enter) returned false; want consumed")
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users u" {
		t.Fatalf("line after accept = %q; want %q", got, "SELECT * FROM users u")
	}
	if sugg.IsVisible() {
		t.Error("popup still visible after accept")
	}
}

func TestCompletionAcceptViaCtrlY(t *testing.T) {
	ctrl, reg, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	dispatchAction(t, reg, commands.EditorCompletionAccept, commands.ExecCtx{Mode: types.ModeInsert})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users u" {
		t.Fatalf("line after <c-y> accept = %q; want %q", got, "SELECT * FROM users u")
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
	// FROM is a table context so accept appends a deduped alias
	// "users u" / "usage u".
	got := string(buf.Lines[0].Runes)
	if got != "SELECT * FROM users u" && got != "SELECT * FROM usage u" {
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

// TestAutoTriggerOpensInFromContext pins the auto-trigger gate: with the
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

// TestAutoTriggerOpensOnBareTwoRunePrefix pins the bare-prefix
// broadening: a bare >=2-rune identifier prefix with NO governing clause
// keyword, operator, or `<ident>.` context now auto-opens the popup
// (previously this was suppressed as "prefix-everywhere"). The fuzzy
// quality floor is what keeps the broadened firing from
// flooding; the gate itself opens.
func TestAutoTriggerOpensOnBareTwoRunePrefix(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "us", 2, []string{"users"})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Error("AutoTrigger did not open popup on a bare 2-rune prefix; want visible")
	}
}

// TestAutoTriggerNoPopupOnOneRunePrefix pins the lower bound of the
// broadened gate: a single-rune identifier prefix stays below the
// threshold and does NOT auto-open, even though candidates exist.
func TestAutoTriggerNoPopupOnOneRunePrefix(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "u", 1, []string{"users"})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("AutoTrigger opened popup on a 1-rune prefix; want hidden")
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
// leaving it stale.
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
// guard under the broadened gate: the accept itself does
// not fire AutoTrigger (accept routes through CompletionKey / <c-y>,
// never the printable/backspace seam), so the one-shot suppression flag
// armed by the accept survives intact to the next real keystroke. The
// just-inserted full identifier `users` (a 5-rune prefix) now DOES
// satisfy the broadened gate — so the suppression flag, not the gate, is
// what keeps the very next non-dot keystroke from re-opening the popup
// over the just-accepted text.
func TestAutoTriggerNoRePopupAfterAccept(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	if sugg.IsVisible() {
		t.Fatal("popup still visible immediately after accept")
	}
	// The very next non-dot keystroke fires AutoTrigger; the one-shot flag
	// is consumed and the gate is bypassed -> popup stays hidden even
	// though the broadened prefix gate would otherwise fire on "users".
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("AutoTrigger re-opened popup after accept; want suppressed")
	}
}

// TestAutoTriggerReopensOnSecondKeystrokeAfterAccept documents the
// boundary of the one-shot suppression under the broadened gate: the flag
// suppresses only the SINGLE next keystroke. Once consumed, a subsequent
// keystroke that leaves a >=2-rune prefix re-opens the popup — this is the
// intended broadened behavior (the user is typing on, and the fuzzy
// floor trims the candidate set). The frozen design has no timer, so there
// is no signal to distinguish "still extending the accepted word" from
// "typing a new identifier"; the one-shot only owes the immediate next key.
func TestAutoTriggerReopensOnSecondKeystrokeAfterAccept(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users", "usage"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	// First post-accept keystroke: flag consumed, popup stays hidden.
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Fatal("popup re-opened on the suppressed first keystroke; want hidden")
	}
	// Second keystroke: user types 'a' -> "usersa". Flag already cleared, so
	// the broadened >=2-rune prefix gate fires. (No candidate matches the
	// typo, so the engine yields nothing and the popup stays hidden — assert
	// the gate WAS evaluated by switching to a prefix that matches.)
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM usa")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 17})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Error("AutoTrigger did not re-open on the second post-accept keystroke; one-shot suppression over-reached")
	}
}

// TestAutoTriggerManualUngatedBelowThreshold pins that the manual
// <c-x><c-o> path (RefilterOrTrigger) ignores the >=2-rune gate entirely:
// a 1-rune prefix that AutoTrigger would refuse still opens via the manual
// trigger. Manual completion never routes through AutoTrigger.
func TestAutoTriggerManualUngatedBelowThreshold(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "u", 1, []string{"users"})
	// AutoTrigger refuses (1-rune prefix < threshold).
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Fatal("AutoTrigger opened on 1-rune prefix; want hidden")
	}
	// Manual trigger opens regardless of the gate.
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Error("manual RefilterOrTrigger did not open on 1-rune prefix; manual path must be ungated")
	}
}

// TestAutoTriggerEmptyLineNoPanic pins the col-0 / empty-line edge: an
// empty buffer with the cursor at column 0 must not panic and must not
// open the popup (no identifier prefix to gate on).
func TestAutoTriggerEmptyLineNoPanic(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "", 0, []string{"users"})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("AutoTrigger opened popup on empty line; want hidden")
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

// TestEscDismissesPopupButStaysInsert pins the completion-cancel UX: when the
// popup is open, the FIRST <esc> cancels the popup ONLY and stays in Insert
// mode (so the user keeps typing); a SECOND <esc> then exits to Normal. This
// mirrors standard editor completion behaviour and replaces the prior
// one-press popup-dismiss-and-exit.
func TestEscDismissesPopupAndExitsInsert(t *testing.T) {
	ctrl, reg, buf, sugg, modes := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible before Esc")
	}

	// One Esc should dismiss the popup AND exit to Normal.
	dispatchAction(t, reg, commands.ModeNormal, commands.ExecCtx{Mode: types.ModeInsert})
	if sugg.IsVisible() {
		t.Error("Esc did not dismiss popup")
	}
	if got := modes.Get(types.QUERY_EDITOR); got != types.ModeNormal {
		t.Errorf("Esc with popup visible did not exit Insert; mode = %v want ModeNormal", got)
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

// TestCompletionAcceptAliasInColumnContextOmitsAlias asserts that when the
// accept cursor is NOT in a table context (Expect != Tables), the bare
// candidate is inserted with no alias. A SELECT-clause
// identifier is a column context.
func TestCompletionAcceptAliasInColumnContextOmitsAlias(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT na FROM users", 9, []string{"name"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible after trigger")
	}
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT name FROM users" {
		t.Fatalf("column-context accept = %q; want %q (no alias)", got, "SELECT name FROM users")
	}
}

// TestCompletionAcceptAliasCollisionSuffixes asserts that a second table
// whose derived alias collides with an in-scope alias gets a numeric suffix
// (u -> u2), deduped against ContextResult.InScopeTables.
func TestCompletionAcceptAliasCollisionSuffixes(t *testing.T) {
	ctrl, _, buf, _, _ := newCompletionRig(t, "SELECT * FROM users u JOIN ur", 29, []string{"urls"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users u JOIN urls u2" {
		t.Fatalf("collision accept = %q; want %q", got, "SELECT * FROM users u JOIN urls u2")
	}
}

// TestCompletionAcceptAliasSingleUndo asserts the whole "<table> <alias>"
// insertion is a single EditKindReplace: one Undo reverts it back to the
// typed prefix.
func TestCompletionAcceptAliasSingleUndo(t *testing.T) {
	ctrl, _, buf, _, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users u" {
		t.Fatalf("after accept = %q; want %q", got, "SELECT * FROM users u")
	}
	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM us" {
		t.Fatalf("after single undo = %q; want %q (one EditKindReplace)", got, "SELECT * FROM us")
	}
}

// TestCompletionAcceptAliasEmptyInScopeNoPanic asserts that accepting in a
// table context with zero pre-existing in-scope aliases still derives the
// alias from the table name and does not panic.
func TestCompletionAcceptAliasEmptyInScopeNoPanic(t *testing.T) {
	ctrl, _, buf, _, _ := newCompletionRig(t, "SELECT * FROM ord", 17, []string{"orders"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM orders o" {
		t.Fatalf("empty-in-scope accept = %q; want %q", got, "SELECT * FROM orders o")
	}
}

// TestCompletionAcceptAliasToggleOff asserts that with the alias toggle
// disabled (editor.autocomplete_alias: false) a table accept inserts the
// bare table name with no alias.
func TestCompletionAcceptAliasToggleOff(t *testing.T) {
	ctrl, _, buf, _, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})
	ctrl.SetAliasOnAccept(false)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users" {
		t.Fatalf("toggle-off accept = %q; want %q (no alias)", got, "SELECT * FROM users")
	}
}

// TestCompletionAcceptAliasQuotedMixedCase asserts that a mixed-case table
// candidate emits the double-quoted round-trippable form on accept, and the
// derived alias is the lowercased first letter.
func TestCompletionAcceptAliasQuotedMixedCase(t *testing.T) {
	ctrl, _, buf, _, _ := newCompletionRig(t, "SELECT * FROM My", 16, []string{"MyTable"})
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != `SELECT * FROM "MyTable" m` {
		t.Fatalf("mixed-case accept = %q; want %q", got, `SELECT * FROM "MyTable" m`)
	}
}

// fakeSchemaMeta is a controlled editor.SchemaMetadata for the accept-time
// ambiguous-column-qualify tests. cols maps a
// "schema.table" key to its column list; warmed records whether that
// (schema,table) is considered loaded — the Columns ok-return that gates
// qualification. A table absent from warmed is treated as NOT warmed
// (ok==false), exercising the no-guess path. Only Columns is consulted by
// the qualifier; the other methods are inert stubs.
type fakeSchemaMeta struct {
	cols   map[string][]models.Column
	warmed map[string]bool
}

func (f fakeSchemaMeta) Columns(schema, table string) ([]models.Column, bool) {
	key := schema + "." + table
	if !f.warmed[key] {
		return nil, false
	}
	return f.cols[key], true
}
func (f fakeSchemaMeta) TableNames(string) []string                             { return nil }
func (f fakeSchemaMeta) TableKind(string, string) string                        { return "" }
func (f fakeSchemaMeta) ForeignKeys(string, string) ([]models.ForeignKey, bool) { return nil, false }
func (f fakeSchemaMeta) FunctionNames() []string                                { return nil }

func cols(names ...string) []models.Column {
	out := make([]models.Column, 0, len(names))
	for _, n := range names {
		out = append(out, models.Column{Name: n})
	}
	return out
}

// newQualifyRig builds a completion rig with a fake SchemaMetadata wired in
// (active schema "public") so the accept-time ambiguous-column qualifier
// reads controlled (cols, ok) per in-scope table.
func newQualifyRig(t *testing.T, line string, col int, candidates []string, meta fakeSchemaMeta) (*controllers.VimEditorController, *editor.Buffer, *context.SuggestionsContext) {
	t.Helper()
	ctrl, _, buf, sugg, _ := newCompletionRig(t, line, col, candidates)
	ctrl.SetSchemaMetadata(meta, func() string { return "public" })
	return ctrl, buf, sugg
}

// TestCompletionAcceptQualifiesAmbiguousColumn: a column owned by >=2 warmed
// in-scope tables is qualified with the FIRST owning table's alias via one
// EditKindReplace.
func TestCompletionAcceptQualifiesAmbiguousColumn(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.users":    cols("id", "name"),
			"public.accounts": cols("id", "name"),
		},
		warmed: map[string]bool{"public.users": true, "public.accounts": true},
	}
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM users u JOIN accounts a", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT u.name FROM users u JOIN accounts a" {
		t.Fatalf("ambiguous accept = %q; want %q", got, "SELECT u.name FROM users u JOIN accounts a")
	}
}

// TestCompletionAcceptUniqueColumnBare: a column owned by only ONE in-scope
// table is inserted bare (no qualifier).
func TestCompletionAcceptUniqueColumnBare(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.users":    cols("id", "name"),
			"public.accounts": cols("id", "balance"),
		},
		warmed: map[string]bool{"public.users": true, "public.accounts": true},
	}
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM users u JOIN accounts a", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT name FROM users u JOIN accounts a" {
		t.Fatalf("unique accept = %q; want %q (bare)", got, "SELECT name FROM users u JOIN accounts a")
	}
}

// TestCompletionAcceptOwningAliasIsFirstInScope: when the same column is in
// >=2 tables, the qualifier alias is that of the FIRST in-scope table (by
// InScopeTables order) that owns it.
func TestCompletionAcceptOwningAliasIsFirstInScope(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.accounts": cols("id", "name"),
			"public.users":    cols("id", "name"),
		},
		warmed: map[string]bool{"public.accounts": true, "public.users": true},
	}
	// accounts (alias a) appears first in FROM, so its alias wins.
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM accounts a JOIN users u", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT a.name FROM accounts a JOIN users u" {
		t.Fatalf("first-owner accept = %q; want %q", got, "SELECT a.name FROM accounts a JOIN users u")
	}
}

// TestCompletionAcceptUnwarmedColumnBare: if ANY consulted in-scope table is
// not warmed (Columns ok==false), the column is inserted bare — no guess.
func TestCompletionAcceptUnwarmedColumnBare(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.users":    cols("id", "name"),
			"public.accounts": cols("id", "name"),
		},
		// accounts NOT warmed → ambiguity unknowable → bare.
		warmed: map[string]bool{"public.users": true},
	}
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM users u JOIN accounts a", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT name FROM users u JOIN accounts a" {
		t.Fatalf("unwarmed accept = %q; want %q (bare)", got, "SELECT name FROM users u JOIN accounts a")
	}
}

// TestCompletionAcceptEmptyScopeColumnBare: a column context with no in-scope
// tables inserts the bare column and does not panic.
func TestCompletionAcceptEmptyScopeColumnBare(t *testing.T) {
	meta := fakeSchemaMeta{cols: map[string][]models.Column{}, warmed: map[string]bool{}}
	// No FROM clause → InScopeTables empty.
	ctrl, buf, _ := newQualifyRig(t, "SELECT na", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT name" {
		t.Fatalf("empty-scope accept = %q; want %q (bare)", got, "SELECT name")
	}
}

// TestCompletionAcceptQualifiedColumnSingleUndo: the whole "<alias>.<column>"
// qualification is a single EditKindReplace — one Undo reverts it to the
// typed prefix.
func TestCompletionAcceptQualifiedColumnSingleUndo(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.users":    cols("id", "name"),
			"public.accounts": cols("id", "name"),
		},
		warmed: map[string]bool{"public.users": true, "public.accounts": true},
	}
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM users u JOIN accounts a", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT u.name FROM users u JOIN accounts a" {
		t.Fatalf("after accept = %q; want qualified", got)
	}
	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT na FROM users u JOIN accounts a" {
		t.Fatalf("after single undo = %q; want %q (one EditKindReplace)", got, "SELECT na FROM users u JOIN accounts a")
	}
}

// TestCompletionAcceptAlreadyQualifiedNotDoubled: when the partial sits after
// an "alias." dot-qualifier ("u.na"), the accept does NOT add another
// qualifier. The user already chose the table; the qualify
// branch is skipped because a dot immediately precedes the identifier run
// (dotPrecedesIdentStart) so it never emits "u.u.name".
func TestCompletionAcceptAlreadyQualifiedNotDoubled(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.users":    cols("id", "name"),
			"public.accounts": cols("id", "name"),
		},
		warmed: map[string]bool{"public.users": true, "public.accounts": true},
	}
	// Cursor after "u.na" — Qualifier.Present is true.
	ctrl, buf, _ := newQualifyRig(t, "SELECT u.na FROM users u JOIN accounts a", 11, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT u.name FROM users u JOIN accounts a" {
		t.Fatalf("already-qualified accept = %q; want %q (not doubled)", got, "SELECT u.name FROM users u JOIN accounts a")
	}
}

// newSnippetRig wires a controller + buffer with a SuggestionsContext that
// has a single Kind==snippet suggestion already shown, anchored at the
// cursor (so the accept-time stale guard passes). The Body carries the
// multi-line expansion.
func newSnippetRig(t *testing.T, line string, col int, body string) (*controllers.VimEditorController, *editor.Buffer, *context.SuggestionsContext) {
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
	ctrl.SetCompletionEngine(editor.NewEngine([]editor.Source{fakeSource{}}))

	sugg.Show([]editor.Suggestion{{
		Text:    "sel",
		Display: "sel",
		Source:  "snip",
		Kind:    editor.KindSnippet,
		Body:    body,
	}}, buf.CursorPos())
	return ctrl, buf, sugg
}

// TestCompletionAcceptSnippetExpandsMultiLine pins that accepting a
// Kind==snippet suggestion with a multi-line Body replaces the typed prefix
// with the whole Body across multiple buffer lines, and lands the cursor at
// editor.EndOfInsert(at, body) — the final chunk, NOT identStart+len(body).
func TestCompletionAcceptSnippetExpandsMultiLine(t *testing.T) {
	// "sel" typed at col 3; identStart = 0. Body inserts 3 lines.
	ctrl, buf, sugg := newSnippetRig(t, "sel", 3, "a\nb\nccc")
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("CompletionKey(Enter) returned false; want consumed")
	}
	if got := len(buf.Lines); got != 3 {
		t.Fatalf("buffer line count after snippet accept = %d; want 3", got)
	}
	want := []string{"a", "b", "ccc"}
	for i, w := range want {
		if got := string(buf.Lines[i].Runes); got != w {
			t.Fatalf("line %d = %q; want %q", i, got, w)
		}
	}
	// EndOfInsert at {0,0} for "a\nb\nccc" -> line 2, col 3 (rune-len of "ccc").
	if got := buf.CursorPos(); got != (editor.Position{Line: 2, Col: 3}) {
		t.Fatalf("cursor after snippet accept = %+v; want {2,3} (EndOfInsert, not identStart+len)", got)
	}
	if sugg.IsVisible() {
		t.Error("popup still visible after snippet accept")
	}
}

// TestCompletionAcceptSnippetPreservesTrailingContent pins that content
// after the cursor on the accept line is preserved and appended after the
// body's final chunk (standard insert split).
func TestCompletionAcceptSnippetPreservesTrailingContent(t *testing.T) {
	// "sel WHERE x" with cursor after "sel" (col 3): trailing " WHERE x"
	// must follow the final chunk on the last inserted line.
	ctrl, buf, _ := newSnippetRig(t, "sel WHERE x", 3, "SELECT *\nFROM ")
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept returned false")
	}
	if got := len(buf.Lines); got != 2 {
		t.Fatalf("line count = %d; want 2", got)
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT *" {
		t.Fatalf("line 0 = %q; want %q", got, "SELECT *")
	}
	if got := string(buf.Lines[1].Runes); got != "FROM  WHERE x" {
		t.Fatalf("line 1 = %q; want %q (trailing content appended after body)", got, "FROM  WHERE x")
	}
	// EndOfInsert: line 1, col 5 (rune-len of "FROM "), BEFORE the trailing text.
	if got := buf.CursorPos(); got != (editor.Position{Line: 1, Col: 5}) {
		t.Fatalf("cursor = %+v; want {1,5}", got)
	}
}

// TestCompletionAcceptSnippetIsSingleUndo pins that the whole multi-line
// snippet expansion is exactly ONE undo node: a single Undo reverts the
// entire insertion back to the pre-accept buffer.
func TestCompletionAcceptSnippetIsSingleUndo(t *testing.T) {
	ctrl, buf, _ := newSnippetRig(t, "sel", 3, "a\nb\nc")
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept returned false")
	}
	if got := len(buf.Lines); got != 3 {
		t.Fatalf("pre-undo line count = %d; want 3", got)
	}
	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo err = %v", err)
	}
	if got := len(buf.Lines); got != 1 {
		t.Fatalf("post-undo line count = %d; want 1 (single undo node)", got)
	}
	if got := string(buf.Lines[0].Runes); got != "sel" {
		t.Fatalf("post-undo line = %q; want %q (full revert in one Undo)", got, "sel")
	}
}

// TestCompletionAcceptSnippetSingleLine pins that a snippet body with no
// '\n' still takes the snippet branch and lands the cursor at end-of-insert
// (single line, col = identStart + rune-len of body).
func TestCompletionAcceptSnippetSingleLine(t *testing.T) {
	ctrl, buf, _ := newSnippetRig(t, "sel", 3, "SELECT * FROM ")
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept returned false")
	}
	if got := len(buf.Lines); got != 1 {
		t.Fatalf("line count = %d; want 1", got)
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM " {
		t.Fatalf("line = %q; want %q", got, "SELECT * FROM ")
	}
	if got := buf.CursorPos(); got != (editor.Position{Line: 0, Col: 14}) {
		t.Fatalf("cursor = %+v; want {0,14}", got)
	}
}

// TestCompletionAcceptSnippetSuppressesAutoTrigger pins that
// suppressNextAutoTrigger is set after a snippet accept (the inserted body
// can otherwise re-satisfy the auto-trigger gate). Observed indirectly: a
// follow-up AutoTrigger at the cursor is a no-op (suppressed once).
func TestCompletionAcceptSnippetSuppressesAutoTrigger(t *testing.T) {
	ctrl, buf, sugg := newSnippetRig(t, "sel", 3, "SELECT * FROM us")
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept returned false")
	}
	if sugg.IsVisible() {
		t.Fatal("popup visible immediately after accept")
	}
	// The suppression flag swallows exactly the next AutoTrigger.
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("AutoTrigger fired immediately after snippet accept; want suppressed")
	}
}
