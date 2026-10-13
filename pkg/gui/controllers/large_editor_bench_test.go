package controllers_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func BenchmarkLargeEditorTypingWithAutoCompletion(b *testing.B) {
	modes := keys.NewModeStore()
	modes.Set(types.QUERY_EDITOR, types.ModeInsert)
	qec := context.NewQueryEditorContext(
		context.NewBaseContext(context.BaseContextOpts{Key: types.QUERY_EDITOR}),
		types.ContextTreeDeps{}, modes, nil,
	)
	buf := qec.Buffer()
	for range 100000 {
		buf.Lines = append(buf.Lines, editor.Line{Runes: []rune("SELECT 1; -- large editor reproduction")})
	}
	buf.SetCursor(editor.Position{Line: 50000})
	matcher, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{Modes: modes})
	if err != nil {
		b.Fatal(err)
	}
	ctrl := controllers.NewVimEditorController(qec, matcher)
	ctrl.SetSuggestionsContext(context.NewSuggestionsContext(
		context.NewBaseContext(context.BaseContextOpts{Key: types.SUGGESTIONS}), context.Deps{},
	))
	ctrl.SetCompletionEngine(editor.NewEngine([]editor.Source{fakeSource{candidates: []string{"users"}}}))
	ve := editor.NewVimEditor(qec, matcher, types.QUERY_EDITOR)
	ve.SetAutoCompleter(ctrl.AutoTrigger)
	v := gocui.NewView("benchmark", 0, 0, 100, 24, gocui.OutputNormal)
	// Include a layout-style second paint/cursor focus as well as the
	// per-keystroke repaint and the real controller auto-trigger callback.
	editor.PaintBuffer(v, buf)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ve.Edit(v, gocui.NewKeyRune('x'))
		editor.PaintBuffer(v, buf)
		cur := buf.CursorPos()
		v.FocusPoint(cur.Col, cur.Line, true)
		ve.Edit(v, gocui.NewKeyName(gocui.KeyBackspace))
		editor.PaintBuffer(v, buf)
		cur = buf.CursorPos()
		v.FocusPoint(cur.Col, cur.Line, true)
	}
}
