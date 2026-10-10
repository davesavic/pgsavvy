package orchestrator

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestPhysicalMutationIdentityLive(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("PGSAVVY_TEST_PG unset; disposable fixture required")
	}
	for _, mode := range []string{"alias_collision", "reordered_composite", "permuted_key_contract"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			drv, err := pg.New(nil)(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := drv.Open(ctx, models.Connection{Name: "A", Driver: "postgres", DSN: dsn}, nil)
			if err != nil {
				t.Fatal("owned fixture connection failed")
			}
			t.Cleanup(func() { _ = conn.Close() })
			session, err := conn.AcquireSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			schema := fmt.Sprintf("review_physical_%d", time.Now().UnixNano())
			table := pg.QuoteQualified(schema, "items")
			exec := func(sql string) models.Result {
				t.Helper()
				result, err := session.Execute(ctx, models.Query{SQL: sql})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			exec("CREATE SCHEMA " + pg.QuoteIdent(schema))
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := session.Execute(cleanupCtx, models.Query{SQL: "DROP SCHEMA " + pg.QuoteIdent(schema) + " CASCADE"}); err != nil {
					t.Errorf("owned schema cleanup failed: %v", err)
				}
			})
			projection, replacement, edited := "id AS key_label, a AS b, b AS a", "b AS a, id AS key_label, a AS b", 1
			if mode == "alias_collision" {
				exec("CREATE TABLE " + table + " (id integer PRIMARY KEY, a text, b text)")
				exec("INSERT INTO " + table + " VALUES (1,'old','old'), (2,'old','old')")
			} else {
				exec("CREATE TABLE " + table + " (a integer, b integer, payload text, PRIMARY KEY (a,b), UNIQUE (b,a))")
				exec("INSERT INTO " + table + " VALUES (1,2,'old'), (2,1,'old')")
				projection, replacement, edited = "a,b,payload", "b,a,payload", 2
			}
			h, _, driver := asyncCommitFixture(t, conn)
			set := h.g.pendingEditReg.For("A", models.Ref{Schema: schema, Table: "items"})
			gv := h.deps.tabs.Active().Grid()
			load := func(projection string) {
				t.Helper()
				order := "id"
				if mode != "alias_collision" {
					order = "a"
				}
				stream, err := session.Stream(ctx, models.Query{SQL: "SELECT " + projection + " FROM " + table + " ORDER BY " + order})
				if err != nil {
					t.Fatal(err)
				}
				cols := stream.Columns()
				var rows []models.Row
				for {
					row, ok, err := stream.Next(ctx)
					if err != nil {
						_ = stream.Close()
						t.Fatal(err)
					}
					if !ok {
						break
					}
					rows = append(rows, row)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				ref, identity, reason, resolved, err := pg.ResolveEditability(ctx, session.(*pg.Session), cols)
				if err != nil || reason != "" {
					t.Fatalf("live target resolution: %v %q", err, reason)
				}
				gv.SetColumns(cols)
				gv.AppendRows(rows)
				gv.SetResolvedEditability(resolved, true, identity, "", ref)
			}
			load(projection)
			gv.SetCursor(0, edited)
			sample := &cellEditorSample{}
			editContext := guicontext.NewCellEditorContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CellEditorKey(), ViewName: "editor", Kind: types.TEMPORARY_POPUP}), guicontext.Deps{})
			controller := controllers.NewCellEditorController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, editContext, &asyncCellEditorFocus{},
				cellEditorPicker{tabs: h.deps.tabs, sample: sample}, cellEditorStore{resolve: func() *models.PendingEditSet { return set }, sample: sample})
			if err := controller.Enter(commands.ExecCtx{}); err != nil {
				t.Fatal(err)
			}
			editContext.SetBuffer("changed")
			if err := controller.Commit(commands.ExecCtx{}); err != nil {
				t.Fatal(err)
			}
			load(replacement)
			if mode == "permuted_key_contract" {
				// Exercise the equally valid UNIQUE(b,a) catalog ordering, not
				// just reordered SELECT positions for the original PRIMARY KEY.
				gv.SetEditability(true, []int{0, 1}, "", set.Table)
			}
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			h.launch(op, false, false, func(receipt commitReceipt) error {
				if receipt.err != nil || !receipt.result.Committed {
					t.Fatalf("physical commit failed: %v", receipt.err)
				}
				return nil
			})
			asyncAwait(t, op.done)
			asyncPublish(t, driver)
			if !set.IsEmpty() || !gv.Editable() {
				t.Fatal("commit did not settle the canonical staged identity and refreshed baseline")
			}
			if mode == "alias_collision" {
				result := exec("SELECT a,b FROM " + table + " ORDER BY id")
				if result.Rows[0].Values[0] != "changed" || result.Rows[0].Values[1] != "old" || result.Rows[1].Values[0] != "old" || result.Rows[1].Values[1] != "old" {
					t.Fatal("alias redirected the physical column or changed the unrelated row")
				}
				rows := gv.AllRows()
				if rows[0].Values[0] != "old" || rows[0].Values[2] != "changed" {
					t.Fatal("physical refetch reversed the aliased baseline")
				}
			} else {
				result := exec("SELECT payload FROM " + table + " ORDER BY a")
				if result.Rows[0].Values[0] != "changed" || result.Rows[1].Values[0] != "old" {
					t.Fatal("reordered projection wrote the crossed-key row")
				}
				rows := gv.AllRows()
				if len(rows) != 2 || rows[0].Values[0] != int32(2) || rows[0].Values[1] != int32(1) || rows[0].Values[2] != "changed" || rows[1].Values[0] != int32(1) || rows[1].Values[1] != int32(2) || rows[1].Values[2] != "old" {
					t.Fatal("receipt refreshed the crossed-key buffer instead of its original row")
				}
				gv.SetCursor(0, 2)
				if err := controller.Enter(commands.ExecCtx{}); err != nil {
					t.Fatal(err)
				}
				editContext.SetBuffer("again")
				if err := controller.Commit(commands.ExecCtx{}); err != nil {
					t.Fatal(err)
				}
				next, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
				if err != nil {
					t.Fatal(err)
				}
				h.launch(next, false, false, func(receipt commitReceipt) error { return receipt.err })
				asyncAwait(t, next.done)
				asyncPublish(t, driver)
				result = exec("SELECT payload FROM " + table + " ORDER BY a")
				if result.Rows[0].Values[0] != "again" || result.Rows[1].Values[0] != "old" || gv.AllRows()[0].Values[2] != "again" || gv.AllRows()[1].Values[2] != "old" {
					t.Fatal("editing the refreshed baseline changed the crossed-key row")
				}
			}
		})
	}
}
