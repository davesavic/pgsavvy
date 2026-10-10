//go:build integration

// Integration tests for CellApplyHelper against the docker/postgres
// fixture. Exercises the live BEGIN / UPDATE / IS NOT DISTINCT FROM /
// COMMIT pipeline on transient tables created per-test in an isolated
// schema. Skipped (not failed) when PGSAVVY_TEST_PG is unset — mirrors
// the harness pattern used by fk_forward_integration_test.go.

package helpers_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	helpers "github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

const cellApplyEnvDSN = "PGSAVVY_TEST_PG"

func TestReview_row_outcome_live_keys(t *testing.T) {
	for _, mode := range []string{"apply", "overwrite", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			conn := openIntegrationConn(t)
			schema := fmt.Sprintf("review_outcome_%d", time.Now().UnixNano())
			qualified := pg.QuoteQualified(schema, "items")
			adminExec(t, conn, "CREATE SCHEMA "+pg.QuoteIdent(schema), "CREATE TABLE "+qualified+" (tenant integer, id integer, payload text, PRIMARY KEY (tenant,id))", "INSERT INTO "+qualified+" VALUES (1,2,'old'),(1,3,'unrelated')", "CREATE FUNCTION "+pg.QuoteQualified(schema, "assign_key")+"() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN NEW.id := NEW.id + 100; RETURN NEW; END'", "CREATE TRIGGER assigned BEFORE UPDATE ON "+qualified+" FOR EACH ROW EXECUTE FUNCTION "+pg.QuoteQualified(schema, "assign_key")+"()")
			t.Cleanup(func() { adminExec(t, conn, "DROP SCHEMA "+pg.QuoteIdent(schema)+" CASCADE") })
			set := newCanonicalApplyFixture(models.Ref{Schema: schema, Table: "items"}, "tenant", "id")
			for _, e := range []models.PendingEdit{
				{PrimaryKey: []any{int32(1), int32(2)}, Column: "id", OldValue: int32(2), Kind: models.Expression, NewExpr: "id + 10"},
				{PrimaryKey: []any{int32(1), int32(2)}, Column: "payload", OldValue: "old", NewValue: "new"},
			} {
				if err := set.Add(e); err != nil {
					t.Fatal(err)
				}
			}
			h := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
			var result helpers.ApplyResult
			var err error
			if mode == "overwrite" {
				result, err = h.Overwrite(context.Background(), set, []string{"tenant", "id"})
			} else {
				var conflicts []models.ConflictedEdit
				result, conflicts, err = h.Apply(context.Background(), set, []string{"tenant", "id"}, mode == "dry_run")
				if len(conflicts) != 0 {
					t.Fatalf("key change stranded subsequent edit: %d conflicts", len(conflicts))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "dry_run" && (len(result.RefetchedRows) != 1 || result.RefetchedRows[0].Values[1] != int32(112) || result.RefetchedRows[0].Values[2] != "new") {
				t.Fatalf("wrong refreshed row: %+v", result.RefetchedRows)
			}
			sess, err := conn.AcquireSession(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sess.Close() }()
			rows, err := sess.Execute(context.Background(), models.Query{SQL: "SELECT id,payload FROM " + qualified + " ORDER BY id"})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows.Rows) != 2 {
				t.Fatalf("unexpected row count: %d", len(rows.Rows))
			}
			if mode == "dry_run" {
				if rows.Rows[0].Values[0] != int32(2) || rows.Rows[0].Values[1] != "old" {
					t.Fatal("dry-run changed database")
				}
			} else {
				if rows.Rows[1].Values[0] != int32(112) || rows.Rows[1].Values[1] != "new" {
					t.Fatal("committed row incorrect")
				}
			}
			unrelated := rows.Rows[0]
			if mode == "dry_run" {
				unrelated = rows.Rows[1]
			}
			if unrelated.Values[0] != int32(3) || unrelated.Values[1] != "unrelated" {
				t.Fatal("unrelated row changed")
			}
		})
	}
}

func TestReview_row_outcome_live_NaN(t *testing.T) {
	conn := openIntegrationConn(t)
	schema := fmt.Sprintf("review_nan_%d", time.Now().UnixNano())
	qualified := pg.QuoteQualified(schema, "items")
	adminExec(t, conn, "CREATE SCHEMA "+pg.QuoteIdent(schema), "CREATE TABLE "+qualified+" (id double precision PRIMARY KEY,payload double precision,n integer)", "INSERT INTO "+qualified+" VALUES ('NaN','NaN',0),(1,1,0)")
	t.Cleanup(func() { adminExec(t, conn, "DROP SCHEMA "+pg.QuoteIdent(schema)+" CASCADE") })
	set := newCanonicalApplyFixture(models.Ref{Schema: schema, Table: "items"}, "id")
	for _, e := range []models.PendingEdit{{PrimaryKey: []any{math.NaN()}, Column: "payload", OldValue: math.NaN(), NewValue: math.NaN()}, {PrimaryKey: []any{math.NaN()}, Column: "n", OldValue: int32(0), NewValue: int32(1)}} {
		if err := set.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	h := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	res, conflicts, err := h.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil || len(conflicts) != 0 || !res.Committed || len(res.SQL) != 1 || len(res.RefetchedRows) != 1 || !math.IsNaN(res.RefetchedRows[0].Values[0].(float64)) || res.RefetchedRows[0].Values[2] != int32(1) {
		t.Fatal("live NaN identity did not produce one grouped committed receipt")
	}
	set.RetireCommitted(res.Edits, res.OriginalPKs, res.ResultPKs)
	if !set.IsEmpty() {
		t.Fatal("live committed NaN payload remained staged")
	}
	sess, err := conn.AcquireSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	check, err := sess.Execute(context.Background(), models.Query{SQL: "SELECT n FROM " + qualified + " WHERE id=1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Rows) != 1 || check.Rows[0].Values[0] != int32(0) {
		t.Fatal("NaN edit changed unrelated row")
	}
}

// openIntegrationConn returns a live *pg.Connection ready to hand out
// sessions via AcquireSession. The caller is expected to Close the
// connection via t.Cleanup.
func openIntegrationConn(t *testing.T) drivers.Connection {
	t.Helper()
	dsn := os.Getenv(cellApplyEnvDSN)
	if dsn == "" {
		t.Skipf("%s unset; cell apply integration test requires the docker/postgres fixture", cellApplyEnvDSN)
	}
	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, models.Connection{
		Name:   "cell-apply-test",
		Driver: "postgres",
		DSN:    dsn,
	}, nil)
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// adminExec runs ad-hoc setup SQL through a short-lived session against
// the live connection. Panics on failure — these statements are
// fixture scaffolding, not user input.
func adminExec(t *testing.T, conn drivers.Connection, sqls ...string) {
	t.Helper()
	ctx := context.Background()
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		t.Fatalf("acquire admin session: %v", err)
	}
	defer func() { _ = sess.Close() }()
	for _, s := range sqls {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("admin exec %q: %v", s, err)
		}
	}
}

func TestReview_identity_safety_LiveRollback(t *testing.T) {
	for _, mode := range []string{"apply", "overwrite", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			conn := openIntegrationConn(t)
			schema := fmt.Sprintf("review_mutation_%d", time.Now().UnixNano())
			qualified := pg.QuoteQualified(schema, "t")
			adminExec(t, conn, "CREATE SCHEMA "+pg.QuoteIdent(schema))
			t.Cleanup(func() { adminExec(t, conn, "DROP SCHEMA "+pg.QuoteIdent(schema)+" CASCADE") })
			adminExec(t, conn,
				"CREATE TABLE "+qualified+" (id int NOT NULL, name text, active boolean)",
				"CREATE UNIQUE INDEX partial_id ON "+qualified+" (id) WHERE active",
				"INSERT INTO "+qualified+" VALUES (2, 'old', false), (1, 'old', false), (1, 'old', false)",
			)
			set := newCanonicalApplyFixture(models.Ref{Schema: schema, Table: "t"}, "id")
			for _, id := range []int32{2, 1} {
				mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{id}, Column: "name", OldValue: "old", NewValue: "new", Kind: models.Literal})
			}
			helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
			var err error
			if mode == "overwrite" {
				_, err = helper.Overwrite(context.Background(), set, []string{"id"})
			} else {
				_, _, err = helper.Apply(context.Background(), set, []string{"id"}, mode == "dry_run")
			}
			if err == nil {
				t.Error("multi-row mutation was not rejected")
			}
			sess, err := conn.AcquireSession(context.Background())
			if err != nil {
				t.Fatalf("verify session: %v", err)
			}
			defer func() { _ = sess.Close() }()
			res, err := sess.Execute(context.Background(), models.Query{SQL: "SELECT id, name FROM " + qualified})
			if err != nil || len(res.Rows) != 3 {
				t.Fatalf("verify rows=%d err=%v", len(res.Rows), err)
			}
			for _, row := range res.Rows {
				if row.Values[1] != "old" {
					t.Errorf("batch was not rolled back: id=%v name=%v", row.Values[0], row.Values[1])
				}
			}
		})
	}
}

func TestReview_overwrite_Live(t *testing.T) {
	for _, scenario := range []string{"literal", "null", "expression", "missing", "multi_row"} {
		for _, mode := range []string{"overwrite", "apply", "dry_run"} {
			t.Run(scenario+"/"+mode, func(t *testing.T) {
				conn := openIntegrationConn(t)
				schema := fmt.Sprintf("review_overwrite_%d", time.Now().UnixNano())
				ref := models.Ref{Schema: schema, Table: `t"arget`}
				qualified := pg.QuoteQualified(ref.Schema, ref.Table)
				adminExec(t, conn, "CREATE SCHEMA "+pg.QuoteIdent(schema))
				t.Cleanup(func() { adminExec(t, conn, "DROP SCHEMA "+pg.QuoteIdent(schema)+" CASCADE") })
				adminExec(t, conn,
					"CREATE TABLE "+qualified+` (id int NOT NULL, slot text NOT NULL, "v""alue" text)`,
					"INSERT INTO "+qualified+" VALUES (1, 'x', 'server'), (1, 'y', 'unrelated'), (2, 'x', 'old'), (4, 'x', 'server'), (4, 'x', 'server')",
				)
				set := newCanonicalApplyFixture(ref, "id", "slot")
				e := models.PendingEdit{PrimaryKey: []any{int32(1), "x"}, Column: `v"alue`, OldValue: "stale", NewValue: "new"}
				wantValue := "server"
				if scenario == "null" {
					e.OldValue = nil
				} else if scenario == "expression" {
					e.Kind, e.NewExpr = models.Expression, `'new' || "v""alue"`
				} else if scenario == "missing" || scenario == "multi_row" {
					mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{int32(2), "x"}, Column: `v"alue`, OldValue: "old", NewValue: "first"})
					e.PrimaryKey[0] = int32(3)
					if scenario == "multi_row" {
						e.PrimaryKey[0], e.OldValue = int32(4), "server"
					}
				}
				mustAdd(t, set, e)
				helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
				var res helpers.ApplyResult
				var conflicts []models.ConflictedEdit
				var err error
				if mode == "overwrite" {
					res, err = helper.Overwrite(context.Background(), set, []string{"id", "slot"})
				} else {
					res, conflicts, err = helper.Apply(context.Background(), set, []string{"id", "slot"}, mode == "dry_run")
				}
				wantErr := scenario == "multi_row" || (scenario == "missing" && mode == "overwrite")
				if (err != nil) != wantErr {
					t.Fatalf("err=%v, want error=%v", err, wantErr)
				}
				if wantErr {
					if len(res.RowsAffected) != 0 || len(res.RefetchedRows) != 0 {
						t.Fatalf("failed batch returned success data: %+v", res)
					}
				} else if mode == "overwrite" {
					wantValue = "new"
					if scenario == "expression" {
						wantValue = "newserver"
					}
					if len(res.RowsAffected) != 1 || res.RowsAffected[0] != 1 || len(res.RefetchedRows) != 1 || res.RefetchedRows[0].Values[2] != wantValue {
						t.Fatalf("overwrite result=%+v, want exactly one changed/refetched row", res)
					}
				} else if mode == "apply" {
					if len(conflicts) != 1 {
						t.Fatalf("guarded Apply conflicts=%v, want one", conflicts)
					}
				} else if res.RowsAffected[len(res.RowsAffected)-1] != 0 {
					t.Fatalf("guarded dry-run counts=%v, want final zero", res.RowsAffected)
				}
				sess, err := conn.AcquireSession(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = sess.Close() }()
				rows, err := sess.Execute(context.Background(), models.Query{SQL: `SELECT id, slot, "v""alue" FROM ` + qualified + ` ORDER BY id, slot`})
				if err != nil || len(rows.Rows) != 5 {
					t.Fatalf("verify rows=%d err=%v", len(rows.Rows), err)
				}
				for i, expected := range []string{wantValue, "unrelated", "old", "server", "server"} {
					if rows.Rows[i].Values[2] != expected {
						t.Errorf("row %d value=%v, want %s; wrong target or missing rollback", i, rows.Rows[i].Values[2], expected)
					}
				}
			})
		}
	}
}

// TestCellApply_LiveHappyPath_LiteralEdit exercises the success path on
// a freshly-created table: BEGIN / UPDATE / COMMIT / refetch. Asserts
// rowsAffected and that the committed value is observable through a
// fresh session.
func TestCellApply_LiveHappyPath_LiteralEdit(t *testing.T) {
	conn := openIntegrationConn(t)
	adminExec(t, conn,
		`DROP SCHEMA IF EXISTS cell_apply_test CASCADE`,
		`CREATE SCHEMA cell_apply_test`,
		`CREATE TABLE cell_apply_test.t (id INT PRIMARY KEY, name TEXT)`,
		`INSERT INTO cell_apply_test.t VALUES (1, 'alice')`,
	)
	t.Cleanup(func() {
		adminExec(t, conn, `DROP SCHEMA IF EXISTS cell_apply_test CASCADE`)
	})

	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	set := newCanonicalApplyFixture(models.Ref{Schema: "cell_apply_test", Table: "t"}, "id")
	if err := set.Add(models.PendingEdit{
		PrimaryKey: []any{int32(1)},
		Column:     "name",
		OldValue:   "alice",
		NewValue:   "bob",
		Kind:       models.Literal,
	}); err != nil {
		t.Fatalf("PendingEditSet.Add: %v", err)
	}

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if len(res.RowsAffected) != 1 || res.RowsAffected[0] != 1 {
		t.Fatalf("rowsAffected = %v, want [1]", res.RowsAffected)
	}
	if len(res.RefetchedRows) != 1 {
		t.Fatalf("refetched %d rows, want 1", len(res.RefetchedRows))
	}

	// Independent re-read to confirm the value committed.
	sess, err := conn.AcquireSession(context.Background())
	if err != nil {
		t.Fatalf("verify acquire: %v", err)
	}
	defer func() { _ = sess.Close() }()
	r, err := sess.Execute(context.Background(), models.Query{
		SQL: `SELECT name FROM cell_apply_test.t WHERE id = $1`, Args: []any{int32(1)},
	})
	if err != nil {
		t.Fatalf("verify read: %v", err)
	}
	if len(r.Rows) != 1 || r.Rows[0].Values[0] != "bob" {
		t.Fatalf("verify read rows = %v, want one row with name='bob'", r.Rows)
	}
}

// TestCellApply_LiveConflict_StaleOldValue_RollsBack confirms that an
// edit whose OldValue no longer matches the server triggers a conflict
// and the original row is left intact.
func TestCellApply_LiveConflict_StaleOldValue_RollsBack(t *testing.T) {
	conn := openIntegrationConn(t)
	adminExec(t, conn,
		`DROP SCHEMA IF EXISTS cell_apply_test CASCADE`,
		`CREATE SCHEMA cell_apply_test`,
		`CREATE TABLE cell_apply_test.t (id INT PRIMARY KEY, name TEXT)`,
		`INSERT INTO cell_apply_test.t VALUES (1, 'serverside')`,
	)
	t.Cleanup(func() {
		adminExec(t, conn, `DROP SCHEMA IF EXISTS cell_apply_test CASCADE`)
	})

	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	set := newCanonicalApplyFixture(models.Ref{Schema: "cell_apply_test", Table: "t"}, "id")
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int32(1)},
		Column:     "name",
		OldValue:   "stale", // does not match server
		NewValue:   "bob",
		Kind:       models.Literal,
	})

	_, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want 1 entry", conflicts)
	}
	if conflicts[0].ServerValue != "serverside" {
		t.Fatalf("ServerValue = %v, want 'serverside'", conflicts[0].ServerValue)
	}

	// Row must still hold the original server value — no partial commit.
	sess, _ := conn.AcquireSession(context.Background())
	defer func() { _ = sess.Close() }()
	r, _ := sess.Execute(context.Background(), models.Query{
		SQL: `SELECT name FROM cell_apply_test.t WHERE id = $1`, Args: []any{int32(1)},
	})
	if r.Rows[0].Values[0] != "serverside" {
		t.Fatalf("post-conflict row = %v, want untouched 'serverside'", r.Rows[0].Values[0])
	}
}

// TestCellApply_LiveCompositePK exercises the composite-PK path: the
// WHERE clause must encode every PK column, and the refetch must use
// the row-constructor IN form.
func TestCellApply_LiveCompositePK(t *testing.T) {
	conn := openIntegrationConn(t)
	adminExec(t, conn,
		`DROP SCHEMA IF EXISTS cell_apply_test CASCADE`,
		`CREATE SCHEMA cell_apply_test`,
		`CREATE TABLE cell_apply_test.cpk (a INT NOT NULL, b TEXT NOT NULL, v TEXT, PRIMARY KEY (a, b))`,
		`INSERT INTO cell_apply_test.cpk VALUES (1, 'x', 'old')`,
	)
	t.Cleanup(func() {
		adminExec(t, conn, `DROP SCHEMA IF EXISTS cell_apply_test CASCADE`)
	})

	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	set := newCanonicalApplyFixture(models.Ref{Schema: "cell_apply_test", Table: "cpk"}, "a", "b")
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int32(1), "x"},
		Column:     "v",
		OldValue:   "old",
		NewValue:   "new",
		Kind:       models.Literal,
	})

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"a", "b"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if res.RowsAffected[0] != 1 {
		t.Fatalf("rowsAffected[0] = %d, want 1", res.RowsAffected[0])
	}
}

// TestCellApply_LiveExpressionEdit confirms NewExpr is inlined into SET
// (NOT parameterized) and produces the expected server-side computation.
func TestCellApply_LiveExpressionEdit(t *testing.T) {
	conn := openIntegrationConn(t)
	adminExec(t, conn,
		`DROP SCHEMA IF EXISTS cell_apply_test CASCADE`,
		`CREATE SCHEMA cell_apply_test`,
		`CREATE TABLE cell_apply_test.t (id INT PRIMARY KEY, n INT)`,
		`INSERT INTO cell_apply_test.t VALUES (1, 10)`,
	)
	t.Cleanup(func() {
		adminExec(t, conn, `DROP SCHEMA IF EXISTS cell_apply_test CASCADE`)
	})

	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	set := newCanonicalApplyFixture(models.Ref{Schema: "cell_apply_test", Table: "t"}, "id")
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int32(1)},
		Column:     "n",
		OldValue:   int32(10),
		NewExpr:    "n + 5",
		Kind:       models.Expression,
	})

	_, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v", conflicts)
	}

	// Confirm post-commit value is 15 (10 + 5).
	sess, _ := conn.AcquireSession(context.Background())
	defer func() { _ = sess.Close() }()
	r, _ := sess.Execute(context.Background(), models.Query{
		SQL: `SELECT n FROM cell_apply_test.t WHERE id = $1`, Args: []any{int32(1)},
	})
	if r.Rows[0].Values[0] != int32(15) {
		t.Fatalf("post-expression value = %v, want 15", r.Rows[0].Values[0])
	}
}

// TestCellApply_LiveDryRun confirms BEGIN/UPDATE/ROLLBACK leaves no
// side-effects on the server.
func TestCellApply_LiveDryRun(t *testing.T) {
	conn := openIntegrationConn(t)
	adminExec(t, conn,
		`DROP SCHEMA IF EXISTS cell_apply_test CASCADE`,
		`CREATE SCHEMA cell_apply_test`,
		`CREATE TABLE cell_apply_test.t (id INT PRIMARY KEY, name TEXT)`,
		`INSERT INTO cell_apply_test.t VALUES (1, 'alice')`,
	)
	t.Cleanup(func() {
		adminExec(t, conn, `DROP SCHEMA IF EXISTS cell_apply_test CASCADE`)
	})

	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	set := newCanonicalApplyFixture(models.Ref{Schema: "cell_apply_test", Table: "t"}, "id")
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int32(1)},
		Column:     "name",
		OldValue:   "alice",
		NewValue:   "bob",
		Kind:       models.Literal,
	})

	res, _, err := helper.Apply(context.Background(), set, []string{"id"}, true)
	if err != nil {
		t.Fatalf("dry-run Apply: %v", err)
	}
	if res.RowsAffected[0] != 1 {
		t.Fatalf("dry-run rowsAffected[0] = %d, want 1 (preview)", res.RowsAffected[0])
	}

	sess, _ := conn.AcquireSession(context.Background())
	defer func() { _ = sess.Close() }()
	r, _ := sess.Execute(context.Background(), models.Query{
		SQL: `SELECT name FROM cell_apply_test.t WHERE id = $1`, Args: []any{int32(1)},
	})
	if r.Rows[0].Values[0] != "alice" {
		t.Fatalf("post-dry-run name = %v, want untouched 'alice'", r.Rows[0].Values[0])
	}
}

// TestCellApply_LiveNullToValueTransition_IsNotDistinctFromHandlesNull
// confirms the IS NOT DISTINCT FROM operator correctly handles
// NULL-to-value transitions where regular `= NULL` would always fail.
func TestCellApply_LiveNullToValueTransition_IsNotDistinctFromHandlesNull(t *testing.T) {
	conn := openIntegrationConn(t)
	adminExec(t, conn,
		`DROP SCHEMA IF EXISTS cell_apply_test CASCADE`,
		`CREATE SCHEMA cell_apply_test`,
		`CREATE TABLE cell_apply_test.t (id INT PRIMARY KEY, name TEXT)`,
		`INSERT INTO cell_apply_test.t VALUES (1, NULL)`,
	)
	t.Cleanup(func() {
		adminExec(t, conn, `DROP SCHEMA IF EXISTS cell_apply_test CASCADE`)
	})

	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: conn})
	set := newCanonicalApplyFixture(models.Ref{Schema: "cell_apply_test", Table: "t"}, "id")
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int32(1)},
		Column:     "name",
		OldValue:   nil, // matches the NULL on the server
		NewValue:   "bob",
		Kind:       models.Literal,
	})

	_, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none (IS NOT DISTINCT FROM matches NULL)", conflicts)
	}
}
