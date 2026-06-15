package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// ---------------------------------------------------------------------------
// cross-feature snippet interaction guards (controller half).
//
// These lock the runtime behaviors that the snippet accept path
// shares with the broader completion/editor machinery:
//   - post-accept suppression of the auto-trigger (ordinary keystroke), with
//     the explicit `<ident>.` dot-context override still re-opening the popup;
//   - vim `.` dot-repeat after a snippet accept being a no-op (snippet expand
//     is intentionally NOT captured into the RepeatStore) — buffer stays
//     byte-identical, no panic;
//   - trailing content after the cursor surviving a multi-line expand.
// All reuse the 7.2 newSnippetRig (vim_editor_controller_insert_test.go).
// ---------------------------------------------------------------------------

// newSnippetRigWithCandidates is newSnippetRig with a configurable completion
// engine candidate set, so the dot-context re-open path has tables/columns to
// surface after the suppression override fires. The shown suggestion is still
// the single Kind==snippet body, anchored at the cursor.
func newSnippetRigWithCandidates(t *testing.T, line string, col int, body string, candidates []string) (*controllers.VimEditorController, *editor.Buffer, *context.SuggestionsContext) {
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

	sugg.Show([]editor.Suggestion{{
		Text:    "sel",
		Display: "sel",
		Source:  "snip",
		Kind:    editor.KindSnippet,
		Body:    body,
	}}, buf.CursorPos())
	return ctrl, buf, sugg
}

// TestSnippetAcceptSuppressesOrdinaryKeystrokeButDotReopens pins the
// suppression/override interplay after a MULTI-LINE snippet accept:
//   - the ordinary next keystroke does NOT auto-open the popup (the one-shot
//     suppressNextAutoTrigger flag is honored), even though the inserted body
//     leaves a >=2-rune identifier prefix that would otherwise satisfy the
//     broadened auto-trigger gate;
//   - an explicit `.` in `<ident>.` dot context DOES re-open it (the
//     IsIdentDotContext override consumes the same flag but bypasses it).
func TestSnippetAcceptSuppressesOrdinaryKeystrokeButDotReopens(t *testing.T) {
	t.Run("ordinary keystroke suppressed", func(t *testing.T) {
		// Multi-line body ending in a bare identifier "users" so the broadened
		// >=2-rune prefix gate WOULD fire if not for the suppression flag.
		ctrl, buf, sugg := newSnippetRigWithCandidates(t, "sel", 3, "SELECT *\nFROM users", []string{"users"})
		if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
			t.Fatal("snippet accept via Enter not consumed")
		}
		if sugg.IsVisible() {
			t.Fatal("popup visible immediately after snippet accept")
		}
		// Cursor sits at end-of-insert (after "users"): a bare >=2-rune prefix.
		// The one-shot flag must swallow this ordinary AutoTrigger.
		ctrl.AutoTrigger(buf, buf.CursorPos())
		if sugg.IsVisible() {
			t.Error("ordinary keystroke after multi-line snippet accept re-opened popup; want suppressed")
		}
	})

	t.Run("explicit dot reopens", func(t *testing.T) {
		// Multi-line body ending in "FROM users"; typing `.` right after lands
		// the cursor in an `<ident>.` column context, which must override the
		// suppression and re-open the popup.
		ctrl, buf, sugg := newSnippetRigWithCandidates(t, "sel", 3, "SELECT *\nFROM users", []string{"id", "name"})
		if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
			t.Fatal("snippet accept via Enter not consumed")
		}
		if sugg.IsVisible() {
			t.Fatal("popup visible immediately after snippet accept")
		}
		// Simulate typing `.` immediately after the inserted "users": append a
		// dot on the final body line, then fire AutoTrigger. The suppression
		// flag is armed; the dot-context override must still open the columns.
		last := len(buf.Lines) - 1
		buf.Lines[last] = editor.Line{Runes: []rune("FROM users.")}
		buf.SetCursor(editor.Position{Line: last, Col: len([]rune("FROM users."))})
		ctrl.AutoTrigger(buf, buf.CursorPos())
		if !sugg.IsVisible() {
			t.Error("`.` after multi-line snippet accept did not open the column popup; suppression swallowed the dot trigger")
		}
	})
}

// TestSnippetAcceptDotRepeatIsNoOp pins that pressing vim `.` (dot-repeat)
// immediately after a snippet accept leaves the buffer BYTE-IDENTICAL to the
// post-accept state: the snippet expansion is intentionally not captured into
// the RepeatStore, so Replay() returns ok=false and the `.` handler no-ops.
// No panic, no replayed insertion, prior operator semantics untouched.
func TestSnippetAcceptDotRepeatIsNoOp(t *testing.T) {
	ctrl, buf, _ := newSnippetRig(t, "sel", 3, "a\nb\nccc")
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("snippet accept via Enter not consumed")
	}

	// Snapshot the exact post-accept buffer (lines + cursor).
	postAccept := joinLines(buf)
	postCursor := buf.CursorPos()

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	// Dispatch the vim `.` action. With no operator captured (snippet accept
	// never calls RepeatStore.Capture), Replay() is ok=false -> silent no-op.
	dispatchAction(t, reg, commands.EditorRepeat, commands.ExecCtx{Mode: types.ModeNormal})

	if got := joinLines(buf); got != postAccept {
		t.Fatalf("`.` after snippet accept mutated the buffer:\n got = %q\nwant = %q (byte-identical, no replay)", got, postAccept)
	}
	if got := buf.CursorPos(); got != postCursor {
		t.Fatalf("`.` after snippet accept moved the cursor: got %+v; want %+v", got, postCursor)
	}
}

// TestSnippetAcceptTrailingContentSplit pins the trailing-content-split
// cross-feature behavior: accepting a multi-line snippet on a line that has
// text AFTER the cursor preserves that trailing text appended to the body's
// final line (standard insert split), and the cursor lands at end-of-insert
// BEFORE the trailing text. Strengthens the 7.2 single-case version by also
// asserting the no-trailing-content edge (vacuous case) under the same rig.
func TestSnippetAcceptTrailingContentSplit(t *testing.T) {
	t.Run("trailing content lands after body final line", func(t *testing.T) {
		// "sel ORDER BY 1" with cursor after "sel" (col 3): the trailing
		// " ORDER BY 1" must follow the body's final chunk "FROM ".
		ctrl, buf, _ := newSnippetRig(t, "sel ORDER BY 1", 3, "SELECT *\nFROM ")
		if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
			t.Fatal("snippet accept via Enter not consumed")
		}
		if got := len(buf.Lines); got != 2 {
			t.Fatalf("line count = %d; want 2", got)
		}
		if got := string(buf.Lines[0].Runes); got != "SELECT *" {
			t.Fatalf("line 0 = %q; want %q", got, "SELECT *")
		}
		if got := string(buf.Lines[1].Runes); got != "FROM  ORDER BY 1" {
			t.Fatalf("line 1 = %q; want %q (trailing content appended after body final line)", got, "FROM  ORDER BY 1")
		}
		// EndOfInsert: line 1, col 5 (rune-len of "FROM "), BEFORE the trailing text.
		if got := buf.CursorPos(); got != (editor.Position{Line: 1, Col: 5}) {
			t.Fatalf("cursor = %+v; want {1,5} (end-of-insert, before trailing content)", got)
		}
	})

	t.Run("no trailing content edge is vacuous", func(t *testing.T) {
		// Cursor at end of line (col 3 == end of "sel"): no trailing content,
		// so the final body line ends exactly at the body, no appended tail.
		ctrl, buf, _ := newSnippetRig(t, "sel", 3, "SELECT *\nFROM ")
		if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
			t.Fatal("snippet accept via Enter not consumed")
		}
		if got := string(buf.Lines[len(buf.Lines)-1].Runes); got != "FROM " {
			t.Fatalf("final line = %q; want %q (no trailing tail to append)", got, "FROM ")
		}
		if got := buf.CursorPos(); got != (editor.Position{Line: 1, Col: 5}) {
			t.Fatalf("cursor = %+v; want {1,5}", got)
		}
	})
}

// joinLines renders the buffer as a single newline-joined string for
// byte-identity assertions.
func joinLines(buf *editor.Buffer) string {
	lines := buf.LinesCopy()
	out := make([]rune, 0, 64)
	for i, l := range lines {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, l.Runes...)
	}
	return string(out)
}
