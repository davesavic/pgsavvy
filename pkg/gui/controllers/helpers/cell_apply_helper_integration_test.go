//go:build integration

// Integration tests for CellApplyHelper against the docker/postgres
// fixture. Exercises the live BEGIN / UPDATE / IS NOT DISTINCT FROM /
// COMMIT pipeline on transient tables created per-test in an isolated
// schema. Skipped (not failed) when DBSAVVY_TEST_PG is unset — mirrors
// the harness pattern used by fk_forward_integration_test.go.

package helpers_test

import (
	"context"
	"os"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	helpers "github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/models"
)

const cellApplyEnvDSN = "DBSAVVY_TEST_PG"

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
	set := &models.PendingEditSet{Table: models.Ref{Schema: "cell_apply_test", Table: "t"}}
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
	set := &models.PendingEditSet{Table: models.Ref{Schema: "cell_apply_test", Table: "t"}}
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
	set := &models.PendingEditSet{Table: models.Ref{Schema: "cell_apply_test", Table: "cpk"}}
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
	set := &models.PendingEditSet{Table: models.Ref{Schema: "cell_apply_test", Table: "t"}}
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
	set := &models.PendingEditSet{Table: models.Ref{Schema: "cell_apply_test", Table: "t"}}
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
	set := &models.PendingEditSet{Table: models.Ref{Schema: "cell_apply_test", Table: "t"}}
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
