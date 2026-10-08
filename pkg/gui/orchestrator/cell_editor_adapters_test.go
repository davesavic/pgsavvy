package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"
)

func TestReview_expr_focus_store(t *testing.T) {
	original, other := &models.PendingEditSet{}, &models.PendingEditSet{}
	active := original
	store := cellEditorStore{resolve: func() *models.PendingEditSet { return active }}
	captured := store.Capture()
	active = other
	edit := models.PendingEdit{PrimaryKey: []any{int64(7)}, Column: "value", NewExpr: "now()", Kind: models.Expression}
	require.NoError(t, captured.Add(edit))
	require.True(t, captured.HasEdit(edit.PrimaryKey, edit.Column))
	require.Equal(t, 1, original.Count())
	require.Zero(t, other.Count())
	require.NoError(t, captured.Remove(edit.PrimaryKey, edit.Column))
	require.True(t, original.IsEmpty())
	active = nil
	unavailable := store.Capture()
	active = other
	require.Error(t, unavailable.Add(edit), "missing captured destination must not fall back to a replacement")
	require.Zero(t, other.Count())
}

func TestReview_cell_editor_recovery_native(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_cell_editor_recovery_native_process$", "-test.v")
	cmd.Env = append(os.Environ(), "PGSAVVY_CELL_RECOVERY_PROBE=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "native recovery: %s", out)
}

func TestReview_cell_editor_recovery_native_process(t *testing.T) {
	if os.Getenv("PGSAVVY_CELL_RECOVERY_PROBE") == "" {
		return
	}
	ng, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 130, Height: 42})
	require.NoError(t, err)
	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	g := NewGui(Deps{Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()), ConnectionsProvider: func() []models.Connection { return nil }})
	require.NoError(t, g.UseDriverForTest(newGocuiDriver(ng)))
	defer func() { require.NoError(t, g.Close()) }()
	ng.SetManagerFunc(func(*gocui.Gui) error {
		if err := g.resultTabsH.OpenResultTab("original", nil); err != nil {
			return err
		}
		tab := g.resultTabsH.Active()
		grid := tab.Grid()
		grid.SetColumns([]models.ColumnMeta{{Name: "id"}, {Name: "data", TypeName: "text"}})
		grid.SetEditability(true, []int{0}, "", models.Ref{Schema: "app", Table: "t"})
		grid.AppendRows([]models.Row{{Values: []any{int64(42), "loaded"}}})
		grid.SetCursor(0, 1)
		if err := g.tree.Push(tab.Context()); err != nil {
			return err
		}
		set := &models.PendingEditSet{}
		sample := &cellEditorSample{}
		controller := g.controllers.CellEditor
		controller.SetPicker(cellEditorPicker{tabs: g.resultTabsH, sample: sample})
		controller.SetStore(cellEditorStore{resolve: func() *models.PendingEditSet { return set }, sample: sample})
		if err := controller.Enter(commands.ExecCtx{}); err != nil {
			return err
		}
		if err := g.RunLayout(130, 42); err != nil {
			return err
		}
		view, err := ng.View(g.registry.CellEditor.GetViewName())
		if err != nil {
			return err
		}
		typeDraft := func(view *gocui.View, draft string) {
			view.TextArea.Clear()
			for _, r := range draft {
				view.TextArea.TypeCharacter(string(r))
			}
		}
		typeDraft(view, "recoverable draft")
		grid.SettleCommittedRows(grid.CommitGeneration(), grid.Columns(), []models.Row{{Values: []any{int64(42), "server"}}}, [][]any{{int64(42)}}, true)
		dispatcher := g.MasterEditorForTest(guicontext.CellEditorKey()).(Dispatcher)
		if result, err := dispatcher.Dispatch(view, gocui.NewKeyName(gocui.KeyEnter)); err != nil || result != keys.Dispatched || !strings.Contains(g.toastHelp.Current(), models.ErrCommitSnapshotStale.Error()) {
			return errors.New("native Enter did not reject stale staging")
		}
		if !g.registry.CellEditor.Active() || g.registry.CellEditor.Buffer() != "recoverable draft" || g.tree.Current() != g.registry.CellEditor || !set.IsEmpty() {
			return errors.New("native stale submission lost its draft or focus")
		}
		if result, err := dispatcher.Dispatch(view, gocui.NewKeyStrMod("g", gocui.ModCtrl)); err != nil || result != keys.Dispatched {
			return errors.New("Ctrl-G did not dispatch non-quit cancellation")
		}
		if g.registry.CellEditor.Active() || g.tree.Current() != tab.Context() || !set.IsEmpty() {
			return errors.New("native cancel failed to restore the result without staging")
		}
		if err := controller.Enter(commands.ExecCtx{}); err != nil {
			return err
		}
		if err := g.RunLayout(130, 42); err != nil {
			return err
		}
		view, err = ng.View(g.registry.CellEditor.GetViewName())
		if err != nil {
			return err
		}
		typeDraft(view, "retried draft")
		if _, err := dispatcher.Dispatch(view, gocui.NewKeyName(gocui.KeyEnter)); err != nil {
			return err
		}
		if set.Count() != 1 || set.Edits()[0].OldValue != "server" || set.Edits()[0].NewValue != "retried draft" || g.tree.Current() != tab.Context() {
			return errors.New("fresh reopening did not stage against the new baseline")
		}
		return gocui.ErrQuit
	})
	if err := ng.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatal(err)
	}
}

func TestReview_expr_focus_native(t *testing.T) {
	for _, action := range []string{"submit", "cancel", "empty"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_expr_focus_native_process$", "-test.v")
			cmd.Env = append(os.Environ(), "PGSAVVY_EXPR_FOCUS_PROBE="+action)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "native probe: %s", out)
			t.Log(string(out))
		})
	}
}

func TestReview_expr_focus_native_process(t *testing.T) {
	action := os.Getenv("PGSAVVY_EXPR_FOCUS_PROBE")
	if action == "" {
		return
	}
	ng, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 130, Height: 42})
	require.NoError(t, err)
	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	g := NewGui(Deps{Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()), ConnectionsProvider: func() []models.Connection { return nil }})
	require.NoError(t, g.UseDriverForTest(newGocuiDriver(ng)))
	defer func() { require.NoError(t, g.Close()) }()
	ng.SetManagerFunc(func(*gocui.Gui) error {
		if err := g.resultTabsH.OpenResultTab("original", nil); err != nil {
			return err
		}
		tab := g.resultTabsH.Active()
		grid := tab.Grid()
		grid.SetColumns([]models.ColumnMeta{{Name: "value", TypeName: "text"}})
		grid.SetEditability(true, []int{0}, "", models.Ref{Schema: "app", Table: "original"})
		grid.AppendRows([]models.Row{{Values: []any{"original"}}})
		if err := g.tree.Push(tab.Context()); err != nil {
			return err
		}
		original, other := &models.PendingEditSet{}, &models.PendingEditSet{}
		active := original
		controller := g.controllers.CellEditor
		controller.SetStore(cellEditorStore{resolve: func() *models.PendingEditSet { return active }})
		if err := controller.Enter(commands.ExecCtx{}); err != nil {
			return err
		}
		if g.tree.Current() != g.registry.CellEditor {
			return errors.New("Enter did not focus editor")
		}
		if err := g.RunLayout(130, 42); err != nil {
			return err
		}
		view, err := ng.View(g.registry.CellEditor.GetViewName())
		if err != nil {
			return err
		}
		view.TextArea.Clear()
		for _, r := range "unfinished draft" {
			view.TextArea.TypeCharacter(string(r))
		}
		active = other
		if err := controller.ExprPrompt(commands.ExecCtx{}); err != nil {
			return err
		}
		if err := g.RunLayout(130, 42); err != nil {
			return err
		}
		promptView, err := ng.View(g.registry.Prompt.GetViewName())
		if err != nil {
			return err
		}
		if g.tree.Current() != g.registry.Prompt || ng.CurrentView() != promptView || !strings.Contains(promptView.Buffer(), "verbatim") {
			return fmt.Errorf("wrong warning prompt frame: %q", promptView.Buffer())
		}
		switch action {
		case "submit":
			err = g.promptHelp.Submit("upper('expr')")
		case "cancel":
			err = g.promptHelp.Cancel()
		case "empty":
			err = g.promptHelp.Submit("")
		default:
			return errors.New("unknown probe action")
		}
		if err != nil {
			return err
		}
		if action != "submit" {
			if err := g.RunLayout(130, 42); err != nil {
				return err
			}
			view, err := ng.View(g.registry.CellEditor.GetViewName())
			if err != nil {
				return err
			}
			if g.tree.Current() != g.registry.CellEditor || ng.CurrentView() != view || !ng.Cursor || !strings.Contains(view.Buffer(), "unfinished draft") || g.registry.CellEditor.Buffer() != "unfinished draft" {
				return fmt.Errorf("wrong editor continuation frame: %q caret=%v", view.Buffer(), ng.Cursor)
			}
			t.Logf("native %s editor body=%q caret=%v", action, view.Buffer(), ng.Cursor)
			if err := controller.Commit(commands.ExecCtx{}); err != nil {
				return err
			}
		}
		if g.tree.Current() != tab.Context() || len(g.tree.Stack()) != 2 {
			return errors.New("result context lost after prompt")
		}
		if err := g.RunLayout(130, 42); err != nil {
			return err
		}
		resultView, err := ng.View(tab.Context().GetViewName())
		if err != nil {
			return err
		}
		if ng.CurrentView() != resultView || !strings.Contains(resultView.Buffer(), "original") {
			return fmt.Errorf("wrong result frame: %q", resultView.Buffer())
		}
		if original.Count() != 1 || other.Count() != 0 || original.Edits()[0].Column != "value" || original.Edits()[0].OldValue != "original" {
			return errors.New("native edit routed to wrong target")
		}
		t.Logf("native %s result body=%q original_edits=1 unrelated_edits=0", action, resultView.Buffer())
		return gocui.ErrQuit
	})
	if err := ng.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatal(err)
	}
}

// TestFormatForEdit_JSONObject asserts the cell-editor seed for a
// json/jsonb value pgx decoded into a Go map is JSON text, not Go's
// default map formatting (map[plan:pro]) that the user would otherwise
// have to hand-correct. Mirrors the grid renderer so seed and display
// agree. pgsavvy json-cell-format.
func TestFormatForEdit_JSONObject(t *testing.T) {
	got := cellEditorPicker{}.FormatForEdit(
		map[string]any{"plan": "pro", "active": true},
		models.ColumnMeta{TypeName: "jsonb"},
	)
	require.Equal(t, `{"active":true,"plan":"pro"}`, got)
}

// TestFormatForEdit_JSONBytes pins the fix: pgx decodes a
// json/jsonb column whose value arrives as raw text into a Go []byte, which
// the shape-based fallbacks miss (not a map, and FormatArrayLiteral rejects
// []byte), so the editor used to seed Go's byte-slice form "[123 34 ...]".
// With the column meta threaded, a json column routes through FormatJSONValue
// and seeds the raw JSON text the user actually wants to edit.
func TestFormatForEdit_JSONBytes(t *testing.T) {
	got := cellEditorPicker{}.FormatForEdit(
		[]byte(`{"a":1}`),
		models.ColumnMeta{TypeName: "json"},
	)
	require.Equal(t, `{"a":1}`, got)
}

func TestReview_json_scalar_edit_seed(t *testing.T) {
	picker := cellEditorPicker{}
	for _, typ := range []string{"json", "jsonb"} {
		col := models.ColumnMeta{TypeName: typ}
		for _, s := range []string{"", "null", "true", "hello", `{"a":1}`, `[1]`, "quote\"\\\n界"} {
			want, err := json.Marshal(s)
			require.NoError(t, err)
			require.Equal(t, string(want), picker.FormatForEdit(s, col))
			require.Equal(t, string(want), picker.FormatForEdit(json.RawMessage(want), col))
		}
		require.Equal(t, `{"a":1}`, picker.FormatForEdit(json.RawMessage(`{"a":1}`), col))
		require.Equal(t, `[1]`, picker.FormatForEdit([]byte(`[1]`), col))
		require.Equal(t, "", picker.FormatForEdit(nil, col))
	}
	require.Equal(t, "hello", picker.FormatForEdit("hello", models.ColumnMeta{TypeName: "text"}))
}

// TestStreamBlocksEdit pins the edit-gate state policy: inline edits are
// permitted during StateRunning (rows are buffered, appends are
// append-only, pending edits are PK-keyed) but blocked while no stable
// buffer exists — StateQueued (no rows opened) and StateSorting (re-run
// cleared the buffer). Terminal states never block.
func TestStreamBlocksEdit(t *testing.T) {
	cases := []struct {
		state ui.TabState
		block bool
	}{
		{ui.StateQueued, true},
		{ui.StateSorting, true},
		{ui.StateRunning, false},
		{ui.StateComplete, false},
		{ui.StateCancelled, false},
		{ui.StateDetached, false},
		{ui.StateErrored, false},
		{ui.StatePlan, false},
		{ui.StateConnectionLost, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := streamBlocksEdit(tc.state); got != tc.block {
				t.Errorf("streamBlocksEdit(%q) = %v; want %v", tc.state, got, tc.block)
			}
		})
	}
}
