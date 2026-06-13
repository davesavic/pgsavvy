//go:build integration

// Integration test for FK-aware JOIN candidate ranking (ko4m.1.4) against the
// docker/postgres fixture. It loads REAL column + foreign-key metadata from the
// live fixture (app.user_roles, which has FKs user_id->users(id) and
// role_id->roles(id)), feeds them into the same synchronous SchemaMetadata the
// production snapshot satisfies, and asserts the FK column ranks first in an
// ON-clause completion. Skipped (not failed) when DBSAVVY_TEST_PG is unset.
//
// Mirrors the openIntegrationSession pattern from
// pkg/drivers/pg/editability_integration_test.go (same DSN env, same skip).

package editor

import (
	"context"
	"os"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// openFKIntegrationSession opens a *pg.Session against the docker fixture, or
// skips when DBSAVVY_TEST_PG is unset. Self-contained so this test file does
// not depend on the pg package's test helpers.
func openFKIntegrationSession(t *testing.T) *pg.Session {
	t.Helper()
	dsn := os.Getenv("DBSAVVY_TEST_PG")
	if dsn == "" {
		t.Skipf("DBSAVVY_TEST_PG unset; FK completion integration test requires the docker/postgres fixture")
	}
	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, models.Connection{
		Name:   "fk-completion-test",
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

// loadInto populates a fakeMeta's column + FK tiers for (schema,table) from the
// live session. The fakeMeta is the same SchemaMetadata the SchemaSource reads
// synchronously in production.
func loadInto(t *testing.T, m *fakeMeta, sess *pg.Session, schema, table string) {
	t.Helper()
	ctx := context.Background()
	cols, err := sess.ListColumns(ctx, schema, table)
	if err != nil {
		t.Fatalf("ListColumns(%s.%s): %v", schema, table, err)
	}
	m.setColumns(schema, table, cols...)
	fks, err := sess.ListForeignKeys(ctx, schema, table)
	if err != nil {
		t.Fatalf("ListForeignKeys(%s.%s): %v", schema, table, err)
	}
	m.setForeignKeys(schema, table, fks...)
}

// TestFKCompletion_FixtureUserRolesRanksFKFirst loads app.user_roles' real
// columns and FKs and asserts that, in "... JOIN app.user_roles ur ON ur.",
// the FK columns (user_id, role_id) outrank the non-FK columns of user_roles.
func TestFKCompletion_FixtureUserRolesRanksFKFirst(t *testing.T) {
	sess := openFKIntegrationSession(t)

	m := newFakeMeta()
	loadInto(t, m, sess, "app", "users")
	loadInto(t, m, sess, "app", "user_roles")

	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("app"))

	// FROM app.users u JOIN app.user_roles ur ON ur.<cursor> — ur resolves to
	// user_roles, whose user_id FK references in-scope app.users.
	got := suggestLine(src, "SELECT * FROM app.users u JOIN app.user_roles ur ON ur.")
	if len(got) == 0 {
		t.Fatal("no suggestions for ON ur. context")
	}

	fkScore := scoreOf(got, "user_id")
	if fkScore < 0 {
		t.Fatalf("user_id not in suggestions %v", texts(got))
	}
	// Every non-FK column of user_roles must score strictly below the FK column.
	for _, s := range got {
		if s.Text == "user_id" || s.Text == "role_id" {
			continue
		}
		if s.Score >= fkScore {
			t.Errorf("non-FK column %q Score=%d must be < FK user_id Score=%d", s.Text, s.Score, fkScore)
		}
	}

	// Through the real Engine sort, the FK column is the top result.
	eng := NewEngine([]Source{src})
	b, p := bufWithCursor("SELECT * FROM app.users u JOIN app.user_roles ur ON ur.")
	top := texts(eng.Trigger(context.Background(), b, p))
	if len(top) == 0 || top[0] != "user_id" {
		t.Fatalf("Engine top suggestion = %v; want user_id first (FK to app.users)", top)
	}
}
