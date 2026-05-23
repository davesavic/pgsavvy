//go:build integration

// Integration tests for EditabilityIntrospect against the docker/postgres
// fixture. Mirrors the requirePGSession pattern from execute_test.go (same
// package, same DSN env). Skipped (not failed) when DBSAVVY_TEST_PG is
// unset.

package pg_test

import (
	"context"
	"os"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// openIntegrationSession opens a *pg.Session against the fixture. Mirrors
// requirePGSession (execute_test.go) but returns the concrete *pg.Session so
// we can call the unexported package surface that EditabilityIntrospect
// needs. The driver type already exposes a sufficiently rich public surface
// via pg.New + drv.Open + conn.AcquireSession to reach here.
func openIntegrationSession(t *testing.T) *pg.Session {
	t.Helper()
	dsn := os.Getenv("DBSAVVY_TEST_PG")
	if dsn == "" {
		t.Skipf("DBSAVVY_TEST_PG unset; editability integration test requires the docker/postgres fixture")
	}
	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, models.Connection{
		Name:   "editability-test",
		Driver: "postgres",
		DSN:    dsn,
	})
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	sess, err := conn.AcquireSession(ctx)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("acquire session: %v", err)
	}
	pgSess, ok := sess.(*pg.Session)
	if !ok {
		_ = sess.Close()
		_ = conn.Close()
		t.Fatalf("acquired session is %T, want *pg.Session", sess)
	}
	t.Cleanup(func() {
		_ = sess.Close()
		_ = conn.Close()
	})
	return pgSess
}

// columnsFromSelect streams a query through the driver to collect the
// FieldDescription-derived ColumnMetas. Drains zero rows so the helper is
// quick.
func columnsFromSelect(t *testing.T, sess *pg.Session, sql string) []models.ColumnMeta {
	t.Helper()
	stream, err := sess.Stream(context.Background(), models.Query{SQL: sql})
	if err != nil {
		t.Fatalf("Stream(%q): %v", sql, err)
	}
	defer func() { _ = stream.Close() }()
	return stream.Columns()
}

func TestEditabilityIntrospect_SingleBaseTable(t *testing.T) {
	sess := openIntegrationSession(t)
	cols := columnsFromSelect(t, sess, "SELECT id, email FROM app.users LIMIT 0")
	ref, rowID, reason, err := pg.EditabilityIntrospect(context.Background(), sess, cols)
	if err != nil {
		t.Fatalf("EditabilityIntrospect: %v", err)
	}
	if reason != "" {
		t.Fatalf("expected editable; got reason %q", reason)
	}
	if ref.Schema != "app" || ref.Table != "users" {
		t.Fatalf("ref = %+v, want app.users", ref)
	}
	if len(rowID) != 1 || rowID[0] != 0 {
		t.Fatalf("rowIdentity = %v, want [0]", rowID)
	}
}

func TestEditabilityIntrospect_Join_SpansMultipleTables(t *testing.T) {
	sess := openIntegrationSession(t)
	cols := columnsFromSelect(t, sess,
		"SELECT u.id, r.id FROM app.users u JOIN app.user_roles ur ON ur.user_id = u.id JOIN app.roles r ON r.id = ur.role_id LIMIT 0")
	_, _, reason, err := pg.EditabilityIntrospect(context.Background(), sess, cols)
	if err != nil {
		t.Fatalf("EditabilityIntrospect: %v", err)
	}
	if reason != "result spans multiple tables" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestEditabilityIntrospect_View(t *testing.T) {
	sess := openIntegrationSession(t)
	cols := columnsFromSelect(t, sess, "SELECT * FROM app.published_posts LIMIT 0")
	_, _, reason, err := pg.EditabilityIntrospect(context.Background(), sess, cols)
	if err != nil {
		t.Fatalf("EditabilityIntrospect: %v", err)
	}
	if reason != "base relation is a view" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestEditabilityIntrospect_MaterializedView(t *testing.T) {
	sess := openIntegrationSession(t)
	cols := columnsFromSelect(t, sess, "SELECT * FROM app.posts_summary LIMIT 0")
	_, _, reason, err := pg.EditabilityIntrospect(context.Background(), sess, cols)
	if err != nil {
		t.Fatalf("EditabilityIntrospect: %v", err)
	}
	if reason != "materialized view" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestEditabilityIntrospect_NoPKTable(t *testing.T) {
	sess := openIntegrationSession(t)
	// Create a transient table with no PK / UNIQUE inside a tx so the
	// fixture stays pristine. Use a CREATE TEMP TABLE so it's confined
	// to the session.
	if _, err := sess.Execute(context.Background(), models.Query{
		SQL: "CREATE TEMP TABLE editability_no_pk_test (a int, b int) ON COMMIT DROP",
	}); err != nil {
		// CREATE TEMP TABLE without a transaction stays for the session
		// — drop it on cleanup.
		t.Fatalf("create temp table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(context.Background(), models.Query{SQL: "DROP TABLE IF EXISTS editability_no_pk_test"})
	})

	cols := columnsFromSelect(t, sess, "SELECT a, b FROM editability_no_pk_test LIMIT 0")
	_, _, reason, err := pg.EditabilityIntrospect(context.Background(), sess, cols)
	if err != nil {
		t.Fatalf("EditabilityIntrospect: %v", err)
	}
	// pg_temp schemas trip the "temporary table" gate before "no row identity"
	// so accept either depending on whether the temp-schema branch resolves
	// first. The integration test exists to confirm we DO disable; the
	// specific reason is asserted in the unit tests.
	if reason != "no row identity" && reason != "temporary table" {
		t.Fatalf("reason = %q, want one of {no row identity, temporary table}", reason)
	}
}
