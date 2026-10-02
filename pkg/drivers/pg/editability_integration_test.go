//go:build integration

// Integration tests for EditabilityIntrospect against the docker/postgres
// fixture. Mirrors the requirePGSession pattern from execute_test.go (same
// package, same DSN env). Skipped (not failed) when PGSAVVY_TEST_PG is
// unset.

package pg_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// openIntegrationSession opens a *pg.Session against the fixture. Mirrors
// requirePGSession (execute_test.go) but returns the concrete *pg.Session so
// we can call the unexported package surface that EditabilityIntrospect
// needs. The driver type already exposes a sufficiently rich public surface
// via pg.New + drv.Open + conn.AcquireSession to reach here.
func openIntegrationSession(t *testing.T) *pg.Session {
	t.Helper()
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skipf("PGSAVVY_TEST_PG unset; editability integration test requires the docker/postgres fixture")
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
	}, nil)
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

func TestReview_identity_safety_LiveCatalog(t *testing.T) {
	for _, key := range []string{"partial", "full_unique", "primary_key", "partial_and_full", "partial_and_pk"} {
		t.Run(key, func(t *testing.T) {
			sess := openIntegrationSession(t)
			ctx := context.Background()
			schema := fmt.Sprintf("review_identity_%d", time.Now().UnixNano())
			qualified := pg.QuoteQualified(schema, "t")
			exec := func(sql string) {
				t.Helper()
				if _, err := sess.Execute(ctx, models.Query{SQL: sql}); err != nil {
					t.Fatalf("fixture SQL: %v", err)
				}
			}
			exec("BEGIN")
			defer exec("ROLLBACK")
			exec("CREATE SCHEMA " + pg.QuoteIdent(schema))
			exec("CREATE TABLE " + qualified + " (id int NOT NULL, name text, active boolean)")
			if key == "partial" || key == "partial_and_full" || key == "partial_and_pk" {
				exec("CREATE UNIQUE INDEX partial_id ON " + qualified + " (id) WHERE active")
			}
			if key == "full_unique" || key == "partial_and_full" {
				exec("CREATE UNIQUE INDEX full_id ON " + qualified + " (id)")
			}
			if key == "primary_key" || key == "partial_and_pk" {
				exec("ALTER TABLE " + qualified + " ADD PRIMARY KEY (id)")
			}
			exec("INSERT INTO " + qualified + " VALUES (1, 'old', false)")
			if key == "partial" {
				exec("INSERT INTO " + qualified + " VALUES (1, 'old', false)")
			}
			cols := columnsFromSelect(t, sess, "SELECT id, name, active FROM "+qualified)
			ref, id, reason, err := pg.EditabilityIntrospect(ctx, sess, cols)
			if err != nil {
				t.Fatalf("introspection: %v", err)
			}
			if ref != (models.Ref{Schema: schema, Table: "t"}) {
				t.Fatalf("relation=%+v", ref)
			}
			if key == "partial" {
				if reason != "no row identity" || len(id) != 0 {
					t.Fatalf("partial UNIQUE authorized identity=%v reason=%q", id, reason)
				}
			} else if reason != "" || len(id) != 1 || id[0] != 0 {
				t.Fatalf("global key disabled: identity=%v reason=%q", id, reason)
			}
		})
	}
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
	// Create a transient table with no PK / UNIQUE so the fixture stays
	// pristine. Execute autocommits each statement, so a CREATE TEMP TABLE
	// without ON COMMIT DROP survives for the rest of the session (temp
	// tables are session-scoped) and is removed by the cleanup below.
	if _, err := sess.Execute(context.Background(), models.Query{
		SQL: "CREATE TEMP TABLE editability_no_pk_test (a int, b int)",
	}); err != nil {
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

// TestEditabilityIntrospect_AppliesConnectionGate exercises the full
// orchestrator flow: introspect a single editable base table, then fold in
// the connection read-only flag via ApplyConnectionGate (the part wired into
// the orchestrator closure for Gap 2b).
func TestEditabilityIntrospect_AppliesConnectionGate(t *testing.T) {
	sess := openIntegrationSession(t)
	cols := columnsFromSelect(t, sess, "SELECT id, email FROM app.users LIMIT 0")
	_, rowID, reason, err := pg.EditabilityIntrospect(context.Background(), sess, cols)
	if err != nil {
		t.Fatalf("EditabilityIntrospect: %v", err)
	}
	editable := reason == ""

	// Read-write connection: gate leaves an editable result editable.
	gotEditable, gotReason := pg.ApplyConnectionGate(editable, reason, false /*connReadOnly*/, true /*supportsInlineEdit*/)
	if !gotEditable || gotReason != "" {
		t.Fatalf("rw gate: editable=%v reason=%q, want true/\"\"", gotEditable, gotReason)
	}
	if len(rowID) != 1 || rowID[0] != 0 {
		t.Fatalf("rowIdentity = %v, want [0]", rowID)
	}

	// Read-only connection: gate disables with the read-only reason.
	roEditable, roReason := pg.ApplyConnectionGate(editable, reason, true /*connReadOnly*/, true)
	if roEditable {
		t.Fatal("ro gate: expected result NOT editable on a read-only connection")
	}
	if roReason != "read-only connection" {
		t.Fatalf("ro reason = %q", roReason)
	}
}
