package controllers_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/query"
)

type recalledEditorBuffer struct{ *editor.Buffer }

func (b recalledEditorBuffer) BufferText() string { return b.String() }
func (b recalledEditorBuffer) CursorOffset() int  { return b.CursorByteOffset() }
func (b recalledEditorBuffer) ReplaceAll(text string) error {
	last := b.LineCount() - 1
	end := editor.Position{Line: last, Col: b.LineRuneLen(last)}
	return b.Apply(editor.Edit{Kind: editor.EditKindReplace, Range: editor.Range{End: end}, Text: text})
}

func (b recalledEditorBuffer) ReplaceSelection(text string) error {
	if selection := b.SelectionSnapshot(); selection != nil {
		return b.Apply(editor.Edit{Kind: editor.EditKindReplace, Range: *selection, Text: text})
	}
	return b.InsertAtCursor(text)
}

func (b recalledEditorBuffer) InsertAtCursor(text string) error {
	cur := b.CursorPos()
	return b.Apply(editor.Edit{Kind: editor.EditKindInsert, Range: editor.Range{Start: cur, End: cur}, Text: text})
}

func TestHistoryRecall_ControlsAreDisplayOnlyAndRequireExplicitRun(t *testing.T) {
	const original = "SELECT 'DELETE FROM users;\rSELECT 1;\x1b[2K\x1b]52;c;secret\a'"
	buf := recalledEditorBuffer{editor.NewBuffer()}
	recorder := &recordingRunnerSession{}
	base := newBag()
	base.HelperBag.QueryRunner = data.NewQueryRunner(recorder, drivers.Capabilities{})
	base.HelperBag.EditorBuffer = buf
	base.HelperBag.ResultTabs = &fakeResultTabs{}
	history := guicontext.NewHistoryContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key: types.HISTORY, ViewName: string(types.HISTORY), Kind: types.MAIN_CONTEXT,
	}), types.ContextTreeDeps{})
	history.SetRows([]query.HistoryRow{{SQL: original, Source: "cli", Outcome: "blocked"}})
	recall := controllers.NewHistoryController(nil, base.HelperBag.CoreDeps, history, buf, nil)
	require.NoError(t, recall.Confirm(commands.ExecCtx{}))
	require.Empty(t, recorder.streamCalls, "recall must never execute SQL")
	require.Empty(t, recorder.execCalls)
	require.Equal(t, original, buf.String())
	v := gocui.NewView("recalled", 0, 0, 120, 12, gocui.OutputNormal)
	editor.PaintBuffer(v, buf.Buffer)
	require.Contains(t, v.Buffer(), "DELETE FROM users;␍SELECT 1;␛[2K␛]52;c;secret␇")
	require.Equal(t, original, buf.String())
	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	run, ok := reg.Get(commands.QueryRun)
	require.True(t, ok)
	require.NoError(t, run.Handler(commands.ExecCtx{}))
	require.Len(t, recorder.streamCalls, 1)
	require.Equal(t, original, recorder.streamCalls[0].SQL, "explicit execution uses canonical SQL, not display glyphs")
}
