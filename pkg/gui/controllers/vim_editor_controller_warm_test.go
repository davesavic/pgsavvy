package controllers_test

import (
	stdcontext "context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// warmingSource models the async-warm column source: it returns `before`
// candidates until warmed() is flipped, then `after`. This lets a test simulate
// "columns land after the warm" and assert the re-trigger bridge refreshes the
// popup in place.
type warmingSource struct {
	before []string
	after  []string
	warm   *bool
}

func (s warmingSource) Name() string  { return "warming" }
func (s warmingSource) Priority() int { return 90 }
func (s warmingSource) Suggest(_ stdcontext.Context, buf *editor.Buffer, pos editor.Position) []editor.Suggestion {
	names := s.before
	if *s.warm {
		names = s.after
	}
	out := make([]editor.Suggestion, 0, len(names))
	for _, n := range names {
		out = append(out, editor.Suggestion{Text: n, Display: n, Source: "warming"})
	}
	return out
}

func newWarmRig(t *testing.T, line string, col int, src editor.Source) (*controllers.VimEditorController, *editor.Buffer, *context.SuggestionsContext) {
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
	ctrl.SetCompletionEngine(editor.NewEngine([]editor.Source{src}))
	return ctrl, buf, sugg
}

// TestOnWarmLanded_RefreshesPopupWhenCursorUnchanged is the headline scenario:
// the popup is open with whatever was cached; a warm lands; with the cursor
// still at the trigger position and the popup still visible, OnWarmLanded
// re-runs the engine so the now-loaded columns appear without an extra
// keystroke.
func TestOnWarmLanded_RefreshesPopupWhenCursorUnchanged(t *testing.T) {
	warmed := false
	src := warmingSource{before: []string{"id"}, after: []string{"id", "email"}, warm: &warmed}
	ctrl, buf, sugg := newWarmRig(t, "SELECT users.", len("SELECT users."), src)

	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() || len(sugg.Suggestions()) != 1 {
		t.Fatalf("pre-warm popup = visible %v, %d suggestions; want visible, 1", sugg.IsVisible(), len(sugg.Suggestions()))
	}

	// The warm lands: the snapshot now has more columns.
	warmed = true
	ctrl.OnWarmLanded("public", "users")

	if !sugg.IsVisible() {
		t.Fatal("popup hidden after warm landed; want visible")
	}
	if got := len(sugg.Suggestions()); got != 2 {
		t.Fatalf("post-warm suggestions = %d; want 2 (re-triggered with warmed columns)", got)
	}
}

// TestOnWarmLanded_DroppedWhenCursorMoved pins the stale guard: a warm that
// lands after the user moved the cursor must NOT re-trigger completion.
func TestOnWarmLanded_DroppedWhenCursorMoved(t *testing.T) {
	warmed := false
	src := warmingSource{before: []string{"id"}, after: []string{"id", "email"}, warm: &warmed}
	ctrl, buf, sugg := newWarmRig(t, "SELECT users.", len("SELECT users."), src)

	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible after trigger")
	}

	// User moves the cursor to a different line before the warm lands.
	buf.Lines = []editor.Line{{Runes: []rune("SELECT users.")}, {Runes: []rune("")}}
	buf.SetCursor(editor.Position{Line: 1, Col: 0})

	warmed = true
	ctrl.OnWarmLanded("public", "users")

	// The popup state must be untouched by the dropped late warm: still the
	// 1-candidate set from before the move (not re-filtered to 2).
	if got := len(sugg.Suggestions()); got != 1 {
		t.Fatalf("suggestions after stale warm = %d; want 1 (late warm dropped)", got)
	}
}

// TestOnWarmLanded_DroppedWhenPopupHidden: a warm landing while the popup is
// closed must not pop it open.
func TestOnWarmLanded_DroppedWhenPopupHidden(t *testing.T) {
	warmed := true
	src := warmingSource{before: nil, after: []string{"id"}, warm: &warmed}
	ctrl, _, sugg := newWarmRig(t, "SELECT users.", len("SELECT users."), src)

	// Popup never opened.
	if sugg.IsVisible() {
		t.Fatal("popup unexpectedly visible at start")
	}
	ctrl.OnWarmLanded("public", "users")
	if sugg.IsVisible() {
		t.Fatal("OnWarmLanded opened a hidden popup; want it stay hidden")
	}
}
