//go:build integration

// Integration test for FKForwardHelper.Jump against the docker/postgres
// fixture. Stands up a real SQLSession + FKCache + QueryRunner, then
// drives Jump on app.user_roles (which has FKs to app.users + app.roles)
// and asserts the resulting tab carries a parent row.
//
// Skipped (not failed) when PGSAVVY_TEST_PG is unset.

package helpers_test

import (
	"context"
	"os"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	helpers "github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

const envDSN = "PGSAVVY_TEST_PG"

// openIntegrationSQLSession opens a *session.SQLSession against the
// docker/postgres fixture. Mirrors the openIntegrationSession pattern
// from pkg/drivers/pg/editability_integration_test.go.
func openIntegrationSQLSession(t *testing.T) *session.SQLSession {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("%s unset; fk forward integration test requires the docker/postgres fixture", envDSN)
	}
	ctx := context.Background()
	factory := pg.New(nil)
	drv, err := factory(ctx)
	if err != nil {
		t.Fatalf("driver factory: %v", err)
	}
	conn, err := drv.Open(ctx, models.Connection{
		Name:   "fk-forward-test",
		Driver: "postgres",
		DSN:    dsn,
	}, nil)
	if err != nil {
		t.Fatalf("driver open: %v", err)
	}
	drvSess, err := conn.AcquireSession(ctx)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("acquire session: %v", err)
	}
	sqlSess := session.New(conn, drvSess, session.Options{})
	t.Cleanup(func() {
		_ = sqlSess.Close()
		_ = conn.Close()
	})
	return sqlSess
}

// drainRows iterates through every row of the run handle, returning
// them so the test can assert on the parent row content.
func drainRows(t *testing.T, rh *session.RunHandle) []models.Row {
	t.Helper()
	ctx := context.Background()
	stream := rh.Rows()
	out := []models.Row{}
	for {
		row, ok, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("stream.Next: %v", err)
		}
		if !ok {
			break
		}
		out = append(out, row)
	}
	return out
}

// fakeTabsRecording captures the (label, runHandle) pair OpenResultTab
// receives so the integration test can drain the rows directly.
type fakeTabsRecording struct {
	label string
	rh    *session.RunHandle
}

func (f *fakeTabsRecording) OpenResultTab(label string, rh *session.RunHandle) error {
	f.label = label
	f.rh = rh
	return nil
}

func TestFKForward_LiveJump_FromUserRolesToUsers(t *testing.T) {
	sqlSess := openIntegrationSQLSession(t)
	ctx := context.Background()

	// Pick a user_id present in app.user_roles so the parent SELECT will
	// return a row. The fixture seeds user_roles with at least (1,1).
	rh, err := sqlSess.Stream(ctx, models.Query{
		SQL: `SELECT user_id, role_id FROM app.user_roles ORDER BY user_id, role_id LIMIT 1`,
	})
	if err != nil {
		t.Fatalf("seed select: %v", err)
	}
	rows := drainRows(t, rh)
	if len(rows) == 0 {
		t.Skip("fixture has no user_roles rows; cannot exercise gd")
	}
	seedUserID := rows[0].Values[0]
	seedRoleID := rows[0].Values[1]

	// Build the helper against the live FK cache + runner.
	cache := sqlSess.FKCache()
	runner := data.NewQueryRunnerForSession(sqlSess, drivers.Capabilities{})
	tabs := &fakeTabsRecording{}
	jumps := ui.NewResultJumpList()
	h := helpers.NewFKForwardHelper(helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs, Limit: 10,
	})

	// CurrentTab: simulate the user sitting on the user_id cell.
	tab := &fakeTab{
		slot: 0, id: 100,
		schema: "app", table: "user_roles",
		cols: []string{"user_id", "role_id"},
		rows: map[int][]any{0: {seedUserID, seedRoleID}},
	}

	if err := h.Jump(ctx, tab, 0, 0); err != nil {
		t.Fatalf("Jump: %v", err)
	}
	if tabs.rh == nil {
		t.Fatalf("OpenResultTab not invoked (or rh nil)")
	}

	parentRows := drainRows(t, tabs.rh)
	if len(parentRows) == 0 {
		t.Errorf("parent SELECT returned 0 rows; expected at least the seed user")
	}
	// Jump pushed entry.
	if jumps.Len() != 1 {
		t.Errorf("jumps.Len = %d, want 1", jumps.Len())
	}
}

func TestFKForward_LiveCompositeMissingColumn_DisablesBeforeRun(t *testing.T) {
	sqlSess := openIntegrationSQLSession(t)
	ctx := context.Background()

	// Set up a transient composite-FK fixture in its own schema so this
	// test stays isolated from the shared fixture.
	stmts := []string{
		`DROP SCHEMA IF EXISTS fk_forward_test CASCADE`,
		`CREATE SCHEMA fk_forward_test`,
		`CREATE TABLE fk_forward_test.parent (a INT NOT NULL, b INT NOT NULL, PRIMARY KEY (a, b))`,
		`CREATE TABLE fk_forward_test.child (
			a INT NOT NULL,
			b INT NOT NULL,
			extra TEXT,
			CONSTRAINT child_parent_fkey FOREIGN KEY (a, b) REFERENCES fk_forward_test.parent (a, b)
		)`,
		`INSERT INTO fk_forward_test.parent VALUES (1, 2)`,
		`INSERT INTO fk_forward_test.child VALUES (1, 2, 'x')`,
	}
	for _, s := range stmts {
		if _, err := sqlSess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sqlSess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS fk_forward_test CASCADE`})
	})

	runner := data.NewQueryRunnerForSession(sqlSess, drivers.Capabilities{})
	tabs := &fakeTabsRecording{}
	jumps := ui.NewResultJumpList()
	h := helpers.NewFKForwardHelper(helpers.FKForwardDeps{
		Cache: sqlSess.FKCache(), JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	// Result projection has only "a" — the composite guard must fire
	// before any SQL is sent.
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "fk_forward_test", table: "child",
		cols: []string{"a", "extra"},
		rows: map[int][]any{0: {int32(1), "x"}},
	}

	err := h.Jump(ctx, tab, 0, 0)
	if err == nil {
		t.Fatalf("Jump: expected composite-missing error, got nil")
	}
	if tabs.rh != nil {
		t.Errorf("OpenResultTab unexpectedly invoked; guard failed")
	}
	if jumps.Len() != 0 {
		t.Errorf("jumps.Len = %d, want 0 (guard should short-circuit before Push)", jumps.Len())
	}
}
