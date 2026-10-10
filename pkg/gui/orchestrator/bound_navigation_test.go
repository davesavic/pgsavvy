package orchestrator

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/spf13/afero"
)

type navigationSession struct {
	*fkGuardSession
	metadataEntered chan struct{}
	metadataGate    chan struct{}
	queryEntered    chan models.Query
	queryGate       chan struct{}
	closed          atomic.Int32
}

func (s *navigationSession) ListForeignKeys(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	if s.metadataEntered != nil {
		close(s.metadataEntered)
	}
	if s.metadataGate != nil {
		select {
		case <-s.metadataGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.fkGuardSession.ListForeignKeys(ctx, schema, table)
}

func (s *navigationSession) Stream(ctx context.Context, q models.Query) (drivers.RowStream, error) {
	s.queryEntered <- q
	if s.queryGate != nil {
		select {
		case <-s.queryGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &navigationRows{closed: &s.closed}, nil
}

type navigationRows struct{ closed *atomic.Int32 }

func (r *navigationRows) Columns() []models.ColumnMeta {
	return []models.ColumnMeta{{Name: "id", TableOID: 42, TableAttributeNumber: 1}}
}

func (r *navigationRows) Next(context.Context) (models.Row, bool, error) {
	return models.Row{}, false, nil
}
func (r *navigationRows) Close() error            { r.closed.Add(1); return nil }
func (r *navigationRows) RowsAffected() int64     { return 0 }
func (r *navigationRows) QueryID() models.QueryID { return models.QueryID{SessionID: 1, Nonce: 1} }

type navigationOrigin struct{ tab *ui.Tab }

func (o navigationOrigin) Slot() int                   { return o.tab.Slot() }
func (o navigationOrigin) ID() int64                   { return o.tab.ID() }
func (o navigationOrigin) BaseTable() (string, string) { return "app", "children" }
func (o navigationOrigin) ColumnNames() []string       { return []string{"id", "parent_id"} }
func (o navigationOrigin) RowValues(int) ([]any, bool) { return []any{1, 99}, true }

func navigationFixture(t *testing.T, inner *navigationSession) (*Gui, *asyncCommitDriver) {
	t.Helper()
	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	conn := &fkGuardConn{}
	sqlSession := session.New(conn, inner, session.Options{})
	driver := &asyncCommitDriver{queue: make(chan func() error, 64)}
	g := &Gui{deps: Deps{Common: c}, driver: driver, jumpListH: ui.NewResultJumpList()}
	g.queryState.activeSQLSession = sqlSession
	g.queryState.queryRunner = data.NewQueryRunnerForSession(sqlSession, drivers.Capabilities{})
	g.resultTabsH = ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := g.resultTabsH.OpenResultTab("origin", nil); err != nil {
		t.Fatal(err)
	}
	gv := g.resultTabsH.Active().Grid()
	gv.SetColumns([]models.ColumnMeta{{Name: "id", PhysicalName: "id", TableOID: 42, TableAttributeNumber: 1}, {Name: "parent_alias", PhysicalName: "parent_id", TableOID: 42, TableAttributeNumber: 2}})
	gv.SetEditability(true, []int{0}, "", models.Ref{Schema: "app", Table: "children"})
	gv.AppendRows([]models.Row{{Values: []any{1, 99}}})
	g.resultTabsH.Active().SetOrigin("SELECT id,parent_id FROM app.children", []any{7}, "captured_schema")
	t.Cleanup(func() {
		g.resultTabsH.CancelNavigation()
		g.queryState.queryRunner.Unbind()
		_ = sqlSession.Close()
		g.resultTabsH.CloseAll()
	})
	return g, driver
}

func TestBoundForwardFKHistoryAcknowledgmentAndMainLoopResponsiveness(t *testing.T) {
	for _, mode := range []string{"success", "switch-before-history", "switch-before-adoption", "shutdown-before-adoption"} {
		t.Run(mode, func(t *testing.T) {
			entered, gate := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(gate)
			inner := &navigationSession{fkGuardSession: &fkGuardSession{fwd: []models.ForeignKey{{Name: "parent_fk", Columns: []string{"parent_id"}, RefSchema: "app", RefTable: "parents", RefColumns: []string{"id"}}}}, metadataEntered: entered, metadataGate: gate, queryEntered: make(chan models.Query, 1)}
			g, driver := navigationFixture(t, inner)
			origin := g.resultTabsH.Active()
			returned := make(chan error, 1)
			go func() { returned <- g.runBoundForwardFK(context.Background(), navigationOrigin{origin}, 0, 1) }()
			select {
			case err := <-returned:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("FK dispatch blocked on metadata instead of returning to MainLoop")
			}
			asyncAwait(t, entered)
			select {
			case <-inner.queryEntered:
				t.Fatal("parent SELECT ran before metadata and UI history acknowledgment")
			default:
			}
			close(gate)
			if mode == "switch-before-history" {
				if err := g.resultTabsH.OpenResultTab("other", nil); err != nil {
					t.Fatal(err)
				}
				asyncPublish(t, driver)
				select {
				case <-inner.queryEntered:
					t.Fatal("stale FK history continuation executed a query")
				default:
				}
				return
			}
			asyncPublish(t, driver)
			select {
			case q := <-inner.queryEntered:
				if q.SQL != `SELECT * FROM "app"."parents" WHERE "id"=$1 LIMIT 1000` || q.Args[0] != 99 {
					t.Fatal("captured FK did not preserve physical identifiers and arguments")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("history acknowledgment did not dispatch parent SELECT")
			}
			var publication func() error
			select {
			case publication = <-driver.queue:
			case <-time.After(3 * time.Second):
				t.Fatal("parent stream did not reach pending UI adoption")
			}
			if mode == "switch-before-adoption" {
				if err := g.resultTabsH.OpenResultTab("other", nil); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "shutdown-before-adoption" {
				g.closed.Store(true)
				g.resultTabsH.CancelNavigation()
				g.queryState.queryRunner.Unbind()
			}
			if err := publication(); err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				if g.resultTabsH.Active() == origin {
					t.Fatal("acknowledged FK did not allocate a result tab")
				}
			} else {
				deadline := time.Now().Add(time.Second)
				for inner.closed.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if inner.closed.Load() != 1 {
					t.Fatal("unadopted parent stream was not disposed exactly once")
				}
			}
		})
	}
}

var _ helpers.CurrentTab = navigationOrigin{}

func TestBoundSortKeepsCapturedOriginAndQuery(t *testing.T) {
	for _, mode := range []string{"accept", "switch-back", "rebind", "supersede", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			inner := &navigationSession{fkGuardSession: &fkGuardSession{}, queryEntered: make(chan models.Query, 1)}
			g, driver := navigationFixture(t, inner)
			origin := g.resultTabsH.Active()
			if !g.runBoundSort("SELECT sorted", 2*time.Second) {
				t.Fatal("sort did not dispatch")
			}
			select {
			case q := <-inner.queryEntered:
				if q.SQL != "SELECT sorted" || q.Args[0] != 7 || q.DefaultSchema != "captured_schema" || q.Timeout != 2*time.Second {
					t.Fatal("sort lost its captured query parameters or timeout")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sort did not reach its captured session")
			}
			var publication func() error
			select {
			case publication = <-driver.queue:
			case <-time.After(3 * time.Second):
				t.Fatal("sort did not reach UI publication")
			}
			switch mode {
			case "switch-back":
				if err := g.resultTabsH.OpenResultTab("other", nil); err != nil {
					t.Fatal(err)
				}
				g.resultTabsH.SwitchToTabByID("1")
			case "rebind":
				replacement := session.New(&fkGuardConn{}, &fkGuardSession{}, session.Options{})
				t.Cleanup(func() { _ = replacement.Close() })
				g.queryState.queryRunner.Bind(replacement, drivers.Capabilities{})
			case "supersede":
				g.resultTabsH.CaptureNavigation()
			case "shutdown":
				g.closed.Store(true)
				g.resultTabsH.CancelNavigation()
				g.queryState.queryRunner.Unbind()
			}
			if err := publication(); err != nil {
				t.Fatal(err)
			}
			if mode == "accept" {
				if origin.State() != ui.StateSorting || g.resultTabsH.Active() != origin {
					t.Fatal("valid sort did not reattach the original tab")
				}
			} else {
				if origin.State() == ui.StateSorting || len(origin.Grid().AllRows()) != 1 || origin.Grid().AllRows()[0].Values[1] != 99 {
					t.Fatal("stale sort changed the original grid baseline")
				}
				deadline := time.Now().Add(time.Second)
				for inner.closed.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if inner.closed.Load() != 1 {
					t.Fatal("stale sort stream was not disposed exactly once")
				}
			}
		})
	}
}
