package controllers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/stretchr/testify/require"
)

type reviewForwardCache struct {
	composite bool
	tables    []string
}

func (c *reviewForwardCache) Get(_ context.Context, schema, table string) ([]models.ForeignKey, error) {
	c.tables = append(c.tables, schema+"."+table)
	if table == "orders" {
		fk := models.ForeignKey{Schema: "app", Table: "orders", RefSchema: "app", RefTable: "users", Columns: []string{"user_id"}, RefColumns: []string{"id"}}
		if c.composite {
			fk.Columns, fk.RefColumns = []string{"user_tenant", "user_id"}, []string{"tenant", "id"}
		}
		return []models.ForeignKey{fk}, nil
	}
	if table == "users" {
		return []models.ForeignKey{{Schema: "app", Table: "users", RefSchema: "app", RefTable: "teams", Columns: []string{"team_id"}, RefColumns: []string{"id"}}}, nil
	}
	return nil, nil
}

type reviewForwardSession struct {
	launchInner
	queries   []models.Query
	composite bool
	fail      bool
}

func (s *reviewForwardSession) Stream(_ context.Context, q models.Query) (drivers.RowStream, error) {
	s.queries = append(s.queries, q)
	if s.fail && q.SQL != "seed" {
		return nil, errors.New("synthetic parent query failure")
	}
	cols, values := []string{"team_id", "tenant", "id"}, []any{int64(7), int64(17), int64(42)}
	if q.SQL == "seed" {
		cols, values = []string{"user_id"}, []any{int64(42)}
		if s.composite {
			cols, values = []string{"user_tenant", "user_id"}, []any{int64(17), int64(42)}
		}
	} else if strings.Contains(q.SQL, `"teams"`) {
		cols, values = []string{"id", "name"}, []any{int64(7), "red"}
	}
	metadata := make([]models.ColumnMeta, len(cols))
	for i, col := range cols {
		metadata[i] = models.ColumnMeta{Name: col, TableOID: 1, TableAttributeNumber: uint16(i + 1)}
	}
	return &reviewForwardRows{cols: metadata, rows: []models.Row{{Values: values}}}, nil
}

type reviewForwardRows struct {
	eofRowStream
	cols  []models.ColumnMeta
	rows  []models.Row
	index int
}

func (r *reviewForwardRows) Columns() []models.ColumnMeta { return r.cols }
func (r *reviewForwardRows) Next(context.Context) (models.Row, bool, error) {
	if r.index >= len(r.rows) {
		return models.Row{}, false, nil
	}
	row := r.rows[r.index]
	r.index++
	return row, true, nil
}

type reviewForwardBuffer struct{}

func (*reviewForwardBuffer) NewQueryTask(_ string, streamFn func(context.Context) (drivers.RowStream, error), appendRows func([]models.Row), _ int, done func(error)) error {
	stream, err := streamFn(context.Background())
	if err != nil {
		return err
	}
	for {
		row, ok, err := stream.Next(context.Background())
		if err != nil || !ok {
			_ = stream.Close()
			done(err)
			return nil
		}
		appendRows([]models.Row{row})
	}
}
func (*reviewForwardBuffer) Stop()                                                       {}
func (*reviewForwardBuffer) ReadRows(int)                                                {}
func (*reviewForwardBuffer) ReadToEnd(then func())                                       { then() }
func (*reviewForwardBuffer) ReadToEndWithCompletion(_ context.Context, done func(error)) { done(nil) }

func (*reviewForwardBuffer) EstimatedRows() int64   { return 0 }
func (*reviewForwardBuffer) SetEstimatedRows(int64) {}

func TestReview_forward_fk_metadata(t *testing.T) {
	for _, composite := range []bool{false, true} {
		for _, scenario := range []string{"chain_sort", "readonly", "pinned", "query_error"} {
			name := "single/" + scenario
			if composite {
				name = "composite/" + scenario
			}
			t.Run(name, func(t *testing.T) {
				base := newBag()
				pump := &uiPump{}
				inner := &reviewForwardSession{composite: composite, fail: scenario == "query_error"}
				sqlSession := session.New(&launchConn{}, inner, session.Options{})
				runner := data.NewQueryRunnerForSession(sqlSession, drivers.Capabilities{})
				introspections := 0
				maxTabs := 4
				if scenario == "pinned" {
					maxTabs = 1
				}
				tabs := &reviewAllocationTabs{ResultTabsHelper: ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{
					MaxTabs: maxTabs, OnUIThread: pump.post,
					StreamFactory: func() ui.StreamRunner { return &reviewForwardBuffer{} },
					IntrospectEditability: func(cols []models.ColumnMeta) (func(context.Context) (bool, []int, string, models.Ref, []models.ColumnMeta), func() bool) {
						introspections++
						require.NotEmpty(t, cols)
						return func(context.Context) (bool, []int, string, models.Ref, []models.ColumnMeta) {
							resolved := append([]models.ColumnMeta(nil), cols...)
							for i := range resolved {
								resolved[i].PhysicalName = resolved[i].Name
							}
							return scenario != "readonly", []int{len(cols) - 1}, "synthetic policy", models.Ref{Schema: "app", Table: "users"}, resolved
						}, func() bool { return true }
					},
				})}
				t.Cleanup(func() { tabs.CloseAll(); runner.Unbind(); require.NoError(t, sqlSession.Close()) })
				seed, err := sqlSession.Stream(context.Background(), models.Query{SQL: "seed"})
				require.NoError(t, err)
				require.NoError(t, tabs.ResultTabsHelper.OpenResultTab("source", seed))
				pump.pumpUntil(t, time.Second, "source drain", func() bool { return tabs.Active().Complete() })
				source := tabs.Active()
				sourceCols := source.Grid().Columns()
				for i := range sourceCols {
					sourceCols[i].PhysicalName = sourceCols[i].Name
				}
				source.Grid().SetResolvedEditability(sourceCols, true, []int{0}, "", models.Ref{Schema: "app", Table: "orders"})
				const sourceSQL = "SELECT * FROM app.orders"
				source.SetIdentity("origin_connection", query.DetectFromQuery(sourceSQL))
				source.SetOrigin(sourceSQL, []any{int64(99)}, "source_hint_not_used")
				source.Grid().SetEditability(true, []int{0}, "", models.Ref{Schema: "app", Table: "orders"})
				edits := &models.PendingEditSet{}
				require.NoError(t, edits.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "user_id", NewValue: int64(43), Kind: models.Literal, LoadedAt: time.Now()}))
				source.Grid().SetPendingEdits(edits)
				generation := source.EditabilityGeneration()
				if scenario == "pinned" {
					require.True(t, tabs.PinActive())
				}
				cache, jumps := &reviewForwardCache{composite: composite}, ui.NewResultJumpList()
				base.HelperBag.FKForward = helpers.NewFKForwardHelper(helpers.FKForwardDeps{Cache: cache, Runner: runner, Tabs: tabs, JumpList: jumps, Limit: 10})
				resultCtrl := controllers.NewResultTabsController(nil, base.HelperBag.CoreDeps, base.HelperBag.UIDeps, base.HelperBag.EditDeps, tabs)
				registry := commands.NewRegistry()
				resultCtrl.RegisterActions(registry)
				runHandler(t, registry, commands.FKJumpForward, commands.ExecCtx{})
				if scenario == "pinned" || scenario == "query_error" {
					require.Same(t, source, tabs.Active())
					require.Equal(t, generation, source.EditabilityGeneration())
					require.True(t, source.Grid().Editable())
					require.Same(t, edits, source.Grid().PendingEdits())
					require.Equal(t, 1, edits.Count())
					gotSQL, gotArgs, schema := source.Origin()
					require.Equal(t, sourceSQL, gotSQL)
					require.Equal(t, []any{int64(99)}, gotArgs)
					require.Equal(t, "source_hint_not_used", schema)
					require.Zero(t, introspections)
					require.NotEmpty(t, base.Toast.msgs)
					if scenario == "pinned" {
						select {
						case <-tabs.opened[0].Done():
						default:
							t.Fatal("rejected parent handle was not disposed")
						}
					}
					return
				}
				parent := tabs.Active()
				require.NotSame(t, source, parent)
				conn, identity := parent.Identity()
				require.Equal(t, "origin_connection", conn)
				require.Equal(t, query.DetectFromQuery(inner.queries[1].SQL), identity)
				gotSQL, args, schema := parent.Origin()
				require.Equal(t, inner.queries[1].SQL, gotSQL)
				wantArgs := []any{int64(42)}
				if composite {
					wantArgs = []any{int64(17), int64(42)}
				}
				require.Equal(t, wantArgs, args)
				require.Empty(t, schema)
				require.Empty(t, inner.queries[1].DefaultSchema)
				pump.pumpUntil(t, time.Second, "parent drain", func() bool { return parent.Complete() })
				require.Equal(t, 1, introspections)
				require.Equal(t, scenario != "readonly", parent.Grid().Editable())
				require.Equal(t, []string{"app.orders"}, cache.tables)
				row, col := parent.Grid().CursorPosition()
				require.Zero(t, row)
				require.Zero(t, col)
				runHandler(t, registry, commands.FKJumpForward, commands.ExecCtx{})
				team := tabs.Active()
				require.NotSame(t, parent, team)
				require.Equal(t, []string{"app.orders", "app.users"}, cache.tables)
				require.Equal(t, []any{int64(7)}, inner.queries[2].Args)
				pump.pumpUntil(t, time.Second, "team drain", func() bool { return team.Complete() })
				base.HelperBag.QueryRunner, base.HelperBag.ResultTabs = runner, tabs
				_ = controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
				tabs.sortHook(0)
				require.Len(t, inner.queries, 4)
				require.Equal(t, "SELECT * FROM (\n"+strings.TrimSuffix(inner.queries[2].SQL, " LIMIT 10")+"\n) _pgsavvy_sort\nORDER BY 1 ASC\nLIMIT 10", inner.queries[3].SQL)
				require.Equal(t, []any{int64(7)}, inner.queries[3].Args)
				require.Empty(t, inner.queries[3].DefaultSchema)
				pump.pumpUntil(t, time.Second, "sorted team drain", func() bool { return team.Complete() })
				require.False(t, team.Grid().Editable(), "wrapped sort remains ineligible")
				require.Equal(t, 3, introspections, "sorting resolves physical names without expanding edit authorization")
				require.NotEmpty(t, team.Grid().Columns()[0].PhysicalName)
				require.Same(t, edits, source.Grid().PendingEdits())
			})
		}
	}
}

func TestResultErrorYankBody(t *testing.T) {
	qe := &drivers.QueryError{
		Raw: errors.New("bad\x07 aggregate é"), Code: "42803", Position: 1,
		Detail: strings.Repeat("diagnostic\x1b[31m\n", 30), Hint: "group by", Constraint: "constraint", Where: "function",
	}
	for _, action := range []string{commands.ResultYankCell, commands.ResultYankRow} {
		for _, err := range []error{qe, fmt.Errorf("wrapped: %w", qe), errors.New("generic\x07 error"), errors.New("")} {
			t.Run(action+"/"+err.Error(), func(t *testing.T) {
				b := newBag()
				mgr := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{YankFormat: "json"})
				t.Cleanup(mgr.CloseAll)
				mgr.ShowError("error", err)
				mgr.Active().SetErrorSQL("é bad\nnot displayed")
				body, ok := mgr.Active().ErrorPanelText()
				require.True(t, ok)
				cb := &fakeClipboard{}
				ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, mgr)
				ctrl.SetClipboard(cb)
				reg := commands.NewRegistry()
				ctrl.RegisterActions(reg)
				runHandler(t, reg, action, commands.ExecCtx{})
				require.Equal(t, []string{body}, cb.writes)
				require.Len(t, b.Toast.msgs, 1)
				require.Equal(t, fmt.Sprintf("yanked error panel (%d bytes)", len(body)), b.Toast.msgs[0].Msg)
			})
		}
	}
}

func TestResultErrorYankFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		missing bool
		want    string
	}{
		{"missing writer", nil, true, "clipboard unavailable"},
		{"unavailable", fmt.Errorf("wrapped: %w", grid.ErrClipboardUnavailable), false, "clipboard unavailable"},
		{"too large", fmt.Errorf("wrapped: %w", grid.ErrClipboardTooLarge), false, "clipboard: value too large"},
		{"backend", errors.New("backend failed"), false, "clipboard: backend failed"},
	} {
		for _, action := range []string{commands.ResultYankCell, commands.ResultYankRow} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				b := newBag()
				mgr := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
				t.Cleanup(mgr.CloseAll)
				mgr.ShowError("error", errors.New("boom"))
				cb := &fakeClipboard{writeErr: tc.err}
				ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, mgr)
				if !tc.missing {
					ctrl.SetClipboard(cb)
				}
				reg := commands.NewRegistry()
				ctrl.RegisterActions(reg)
				runHandler(t, reg, action, commands.ExecCtx{})
				if tc.missing {
					require.Empty(t, cb.writes)
				} else {
					require.Equal(t, []string{"boom"}, cb.writes)
				}
				require.Len(t, b.Toast.msgs, 1)
				require.Equal(t, tc.want, b.Toast.msgs[0].Msg)
			})
		}
	}
}

func TestResultErrorYankActiveAndNoOp(t *testing.T) {
	b := newBag()
	mgr := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	t.Cleanup(mgr.CloseAll)
	cb := &fakeClipboard{}
	ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, mgr)
	ctrl.SetClipboard(cb)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	yank := func() {
		for _, action := range []string{commands.ResultYankCell, commands.ResultYankRow} {
			runHandler(t, reg, action, commands.ExecCtx{})
		}
	}
	yank()
	require.Empty(t, cb.writes)
	require.NoError(t, mgr.OpenPlanTab("plan", models.Plan{}))
	yank()
	require.Empty(t, cb.writes)
	require.Empty(t, b.Toast.msgs)
	mgr.ShowError("A", errors.New("error A"))
	mgr.ShowError("B", errors.New("error B"))
	yank()
	mgr.Cycle(-1)
	yank()
	require.Equal(t, []string{"error B", "error B", "error A", "error A"}, cb.writes)
}

type errorYankStreamRunner struct{ reviewForwardBuffer }

func (*errorYankStreamRunner) NewQueryTask(_ string, streamFn func(context.Context) (drivers.RowStream, error), appendRows func([]models.Row), _ int, done func(error)) error {
	stream, err := streamFn(context.Background())
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	appendRows([]models.Row{{Values: []any{"é", "other"}}, {Values: []any{"last", "row"}}})
	done(errors.New("mid-stream error"))
	return nil
}

func TestResultErrorYankPreservesGrid(t *testing.T) {
	for _, retained := range []bool{false, true} {
		for _, kind := range []string{"cell", "row", "selection"} {
			t.Run(fmt.Sprintf("retained=%v/%s", retained, kind), func(t *testing.T) {
				b := newBag()
				mgr := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{StreamFactory: func() ui.StreamRunner { return &errorYankStreamRunner{} }})
				t.Cleanup(mgr.CloseAll)
				var stream *session.RunHandle
				if retained {
					sqlSession := session.New(&launchConn{}, &reviewForwardSession{composite: true}, session.Options{})
					t.Cleanup(func() { require.NoError(t, sqlSession.Close()) })
					var err error
					stream, err = sqlSession.Stream(context.Background(), models.Query{SQL: "seed"})
					require.NoError(t, err)
				}
				require.NoError(t, mgr.OpenResultTab("grid", stream))
				tab := mgr.Active()
				g := tab.Grid()
				if retained {
					require.Error(t, tab.Err())
				} else {
					g.SetColumns([]models.ColumnMeta{{Name: "a"}, {Name: "b"}})
					g.AppendRows([]models.Row{{Values: []any{"é", "other"}}, {Values: []any{"last", "row"}}})
				}
				_, panel := tab.ErrorPanelText()
				require.False(t, panel)
				gridCB, errorCB := &fakeClipboard{}, &fakeClipboard{}
				g.SetClipboard(gridCB)
				g.SetYankFormat("tsv")
				ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, mgr)
				ctrl.SetClipboard(errorCB)
				reg := commands.NewRegistry()
				ctrl.RegisterActions(reg)
				header := "a\tb\n"
				if retained {
					header = "user_tenant\tuser_id\n"
				}
				action, want := commands.ResultYankRow, header+"é\tother"
				switch kind {
				case "cell":
					action, want = commands.ResultYankCell, "é"
				case "selection":
					mgr.SelectRow()
					mgr.CursorDown()
					want = header + "é\tother\nlast\trow"
				}
				runHandler(t, reg, action, commands.ExecCtx{})
				require.Equal(t, []string{want}, gridCB.writes)
				require.Empty(t, errorCB.writes)
				require.Len(t, b.Toast.msgs, 1)
				require.Contains(t, b.Toast.msgs[0].Msg, "yanked")
			})
		}
	}
}

func TestResultYankRowDuplicateJSON(t *testing.T) {
	for _, format := range []string{"json", "ndjson"} {
		for _, branch := range []string{"row", "selection"} {
			t.Run(format+"/"+branch, func(t *testing.T) {
				b := newBag()
				mgr := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
				if err := mgr.OpenResultTab("test", nil); err != nil {
					t.Fatal(err)
				}
				g := mgr.Active().Grid()
				g.SetColumns([]models.ColumnMeta{{Name: "x"}, {Name: "x"}})
				g.AppendRows([]models.Row{{Values: []any{1, 2}}})
				g.SetYankFormat(format)
				cb := &fakeClipboard{writes: []string{"previous clipboard"}}
				g.SetClipboard(cb)
				if branch == "selection" {
					mgr.SelectRow()
				}
				ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, mgr)
				reg := commands.NewRegistry()
				ctrl.RegisterActions(reg)
				runHandler(t, reg, commands.ResultYankRow, commands.ExecCtx{})

				const want = "clipboard: grid: duplicate JSON column names at columns 1 and 2; use distinct SQL aliases"
				if len(b.Toast.msgs) != 1 || b.Toast.msgs[0].Msg != want {
					t.Errorf("toasts = %+v; want %q", b.Toast.msgs, want)
				}
				if len(cb.writes) != 1 || cb.writes[0] != "previous clipboard" {
					t.Errorf("clipboard writes changed: %q", cb.writes)
				}
			})
		}
	}
}

// TestResultTabsControllerGBindingJumpsLast guards the regression:
// `G` in the result grid must dispatch ResultJumpLast (jump
// cursor to the last loaded row, symmetric with gg=ResultJumpFirst). It
// was previously bound to ResultReadToEnd, whose handler no-ops in the
// default grid view once the stream is complete — so `G` appeared to do
// nothing while `gg` worked. `]G` keeps the explicit drain-to-end.
func TestResultTabsControllerGBindingJumpsLast(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, nil)

	found := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.RESULT_GRID || len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Code == 'G' && k.Special == types.KeyNone {
			if kb.ActionID != commands.ResultJumpLast {
				t.Fatalf("G binding ActionID = %q, want %q", kb.ActionID, commands.ResultJumpLast)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no single-rune G binding found in RESULT_GRID scope")
	}
}

// TestResultTabsControllerYankBindings guards pgsavvy U4: `y` yanks the
// focused cell (ResultYankCell) and `yy` yanks the focused row
// (ResultYankRow), both RESULT_GRID-scoped, Normal mode.
func TestResultTabsControllerYankBindings(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewResultTabsController(nil, b.HelperBag.CoreDeps, b.HelperBag.UIDeps, b.HelperBag.EditDeps, nil)

	var foundCell, foundRow bool
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.RESULT_GRID {
			continue
		}
		// `y` — single bare-rune sequence.
		if len(kb.Sequence) == 1 {
			k := kb.Sequence[0]
			if k.Code == 'y' && k.Special == types.KeyNone {
				if kb.ActionID != commands.ResultYankCell {
					t.Fatalf("y binding ActionID = %q, want %q", kb.ActionID, commands.ResultYankCell)
				}
				if kb.Mode != types.ModeNormal {
					t.Fatalf("y binding Mode = %v, want ModeNormal", kb.Mode)
				}
				foundCell = true
			}
		}
		// `yy` — two-rune sequence.
		if len(kb.Sequence) == 2 {
			k0, k1 := kb.Sequence[0], kb.Sequence[1]
			if k0.Code == 'y' && k1.Code == 'y' && k0.Special == types.KeyNone && k1.Special == types.KeyNone {
				if kb.ActionID != commands.ResultYankRow {
					t.Fatalf("yy binding ActionID = %q, want %q", kb.ActionID, commands.ResultYankRow)
				}
				foundRow = true
			}
		}
	}
	if !foundCell {
		t.Fatal("no `y` (ResultYankCell) binding found in RESULT_GRID scope")
	}
	if !foundRow {
		t.Fatal("no `yy` (ResultYankRow) binding found in RESULT_GRID scope")
	}

	var foundSelectCell bool
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != types.RESULT_GRID || len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Code == 'v' && k.Special == types.KeyNone && k.Mod == 0 {
			if kb.ActionID != commands.ResultSelectCell {
				t.Fatalf("v binding ActionID = %q, want %q", kb.ActionID, commands.ResultSelectCell)
			}
			if kb.Mode != types.ModeNormal {
				t.Fatalf("v binding Mode = %v, want ModeNormal", kb.Mode)
			}
			foundSelectCell = true
		}
	}
	if !foundSelectCell {
		t.Fatal("no `v` (ResultSelectCell) binding found in RESULT_GRID scope")
	}
}
