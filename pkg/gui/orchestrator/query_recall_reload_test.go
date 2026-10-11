package orchestrator_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
)

type recallReloadDriver struct {
	*testfake.RecorderGuiDriver
	hold    atomic.Bool
	updates chan func() error
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
