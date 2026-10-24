package orchestrator_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/stretchr/testify/require"
)

type recallReloadDriver struct {
	*testfake.RecorderGuiDriver
	hold    atomic.Bool
	updates chan func() error
}

func TestQueryRecall_ExternalCLIActivation(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	path := filepath.Join(t.TempDir(), "history.sqlite")
	g := orchestrator.NewGui(orchestrator.Deps{
		Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()),
		HistoryProvider: func() (*query.History, error) { return query.New(path) },
	})
	d := testfake.NewRecorderGuiDriver()
	d.EnableRealView(string(types.QUERY_EDITOR))
	require.NoError(t, g.UseDriverForTest(d))
	t.Cleanup(func() { _ = g.Close() })
	require.NoError(t, g.ContextTree().Push(g.Registry().QueryRail))
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()
	require.Empty(t, g.Registry().History.Items())
	g.Registry().QueryRail.SetActiveTab(controllers.QueryRailEditorTab)
	const original = "DELETE\tFROM example; -- original literal\n\x1b]52;c;retained\a"
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, query.AppendHistory(ctx, path, query.HistoryEvent{ExecutedAt: 12345, SQL: original, ConnectionID: "cli-profile", Source: "cli", Outcome: "blocked"}))
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()
	selected, ok := g.Registry().History.Selected()
	require.True(t, ok)
	require.Equal(t, original, selected.SQL)
	require.Equal(t, "cli", selected.Source)
	require.Equal(t, "blocked", selected.Outcome)
	require.NoError(t, g.RunLayout(80, 24))
	body := d.GetViewBuffer(string(types.QUERY_EDITOR))
	require.Contains(t, body, "[cli blocked] cli-profile")
	require.NotContains(t, body, "effects unverified")
	require.NotContains(t, body, "retained")
	require.NoError(t, g.HelperBagForTest().EditorBuffer.ReplaceAll(""))
	fireHistoryConfirm(t, g)
	g.WaitForWorkersForTest()
	require.Equal(t, original, g.Registry().QueryEditor.Buffer().String())
	// There is no SQLSession: blocked recall cannot contact a database.
	require.False(t, g.HelperBagForTest().QueryRunner.HasSession())
	// A subsequent failed reload must keep both rows and cursor intact.
	require.NoError(t, g.HistoryStoreForTest().Close())
	fireHistoryOpen(t, g)
	g.WaitForWorkersForTest()
	selected, ok = g.Registry().History.Selected()
	require.True(t, ok)
	require.Equal(t, original, selected.SQL)
	require.Equal(t, 0, g.Registry().History.Cursor())
}

func (d *recallReloadDriver) Update(fn func() error) {
	if d.hold.Load() {
		d.updates <- fn
		return
	}
	d.RecorderGuiDriver.Update(fn)
}

func TestQueryRecallReloadRequestsFullLayout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
	}{
		{name: "saved", action: commands.QuerySavedOpen},
		{name: "history", action: commands.HistoryOpen},
	} {
		for _, switchAway := range []bool{false, true} {
			name := tc.name + "/active"
			if switchAway {
				name = tc.name + "/switched_back_to_editor"
			}
			t.Run(name, func(t *testing.T) {
				fs := afero.NewMemMapFs()
				if err := afero.WriteFile(fs, "/queries.yml", []byte("queries:\n  - name: recall-marker\n    sql: SELECT 42\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
				historyPath := filepath.Join(t.TempDir(), "history.sqlite")
				g := orchestrator.NewGui(orchestrator.Deps{
					Common:      c,
					Store:       common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()),
					QueriesPath: "/queries.yml",
					HistoryProvider: func() (*query.History, error) {
						return query.New(historyPath)
					},
				})
				d := &recallReloadDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver(), updates: make(chan func() error, 8)}
				d.EnableRealView(string(types.QUERY_EDITOR))
				if err := g.UseDriverForTest(d); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = g.Close() })
				g.HistoryStoreForTest().Record("SELECT 42 AS recall_marker", 1, 1, true, "test")
				waitForHistoryRecent(t, g.HistoryStoreForTest(), 1)
				if err := g.ContextTree().Push(g.Registry().QueryRail); err != nil {
					t.Fatal(err)
				}
				if err := g.HelperBagForTest().EditorBuffer.ReplaceAll("SELECT 7 AS editor_marker"); err != nil {
					t.Fatal(err)
				}
				d.hold.Store(true)
				cmd, ok := g.CommandRegistry().Get(tc.action)
				if !ok {
					t.Fatalf("missing action %s", tc.action)
				}
				if err := cmd.Handler(commands.ExecCtx{}); err != nil {
					t.Fatal(err)
				}
				g.WaitForWorkersForTest()
				if switchAway {
					g.Registry().QueryRail.SetActiveTab(controllers.QueryRailEditorTab)
				}
				select {
				case fn := <-d.updates:
					d.hold.Store(false)
					if err := fn(); err != nil {
						t.Fatal(err)
					}
					// Native full updates run Layout after the queued publication.
					if err := g.RunLayout(130, 42); err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatal("row publication did not request a full layout; content-only updates leave the list stale")
				}
				body := d.GetViewBuffer(string(types.QUERY_EDITOR))
				want := "recall-marker"
				if tc.name == "history" {
					want = "recall_marker"
				}
				if switchAway {
					want = "editor_marker"
					view := d.RealView(string(types.QUERY_EDITOR))
					if view == nil {
						t.Fatal("query editor view missing")
					}
					body = view.Buffer()
				}
				if !strings.Contains(body, want) {
					t.Fatalf("shared view did not render the active tab: want %q in %q", want, body)
				}
			})
		}
	}
}
