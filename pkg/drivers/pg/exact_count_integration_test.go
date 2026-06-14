//go:build integration

// Integration tests for CountPredicatedRows against the docker/postgres
// fixture. Mirrors the openIntegrationSession pattern from
// predicated_estimate_integration_test.go. Skipped (not failed) when
// DBSAVVY_TEST_PG is unset.

package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

const exactCountTimeout = 750 * time.Millisecond

// TestCountPredicatedRows_ExactCount stands up a parent + child table and
// confirms the COUNT(*) for one parent row's children is EXACT (5), not a
// planner estimate.
func TestCountPredicatedRows_ExactCount(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS exactcount_test CASCADE`,
		`CREATE SCHEMA exactcount_test`,
		`CREATE TABLE exactcount_test.customers (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE exactcount_test.orders (
			id BIGINT PRIMARY KEY,
			customer_id BIGINT NOT NULL REFERENCES exactcount_test.customers(id))`,
		`INSERT INTO exactcount_test.customers (id) SELECT g FROM generate_series(1, 100) g`,
		`INSERT INTO exactcount_test.orders (id, customer_id)
			SELECT g, ((g - 1) / 5) + 1 FROM generate_series(1, 500) g`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS exactcount_test CASCADE`})
	})

	// Inbound FK: child = orders, referencing customers(id) via customer_id.
	fk := models.ForeignKey{
		Schema:     "exactcount_test",
		Table:      "orders",
		Columns:    []string{"customer_id"},
		RefSchema:  "exactcount_test",
		RefTable:   "customers",
		RefColumns: []string{"id"},
	}

	// Each of the first 100 customers has exactly 5 orders (500/100).
	got, err := pg.CountPredicatedRows(ctx, sess, fk, []any{int64(1)}, exactCountTimeout)
	if err != nil {
		t.Fatalf("CountPredicatedRows: %v", err)
	}
	if got != 5 {
		t.Fatalf("exact count = %d, want 5", got)
	}

	// A parent row with no children counts exactly 0.
	if _, err := sess.Execute(ctx, models.Query{SQL: `INSERT INTO exactcount_test.customers (id) VALUES (9999)`}); err != nil {
		t.Fatalf("insert childless parent: %v", err)
	}
	zero, err := pg.CountPredicatedRows(ctx, sess, fk, []any{int64(9999)}, exactCountTimeout)
	if err != nil {
		t.Fatalf("CountPredicatedRows (zero): %v", err)
	}
	if zero != 0 {
		t.Fatalf("exact count = %d, want 0", zero)
	}
}

// TestCountPredicatedRows_TimeoutPropagates forces a statement timeout (the
// child predicate column is wrapped in pg_sleep via a slow expression) and
// confirms a timeout surfaces as an error so the caller can fall back to the
// planner estimate. A 1ms timeout against any real COUNT is reliably exceeded.
func TestCountPredicatedRows_TimeoutPropagates(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS exactcount_to CASCADE`,
		`CREATE SCHEMA exactcount_to`,
		`CREATE TABLE exactcount_to.parent (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE exactcount_to.child (
			id BIGINT PRIMARY KEY,
			parent_id BIGINT NOT NULL REFERENCES exactcount_to.parent(id))`,
		`INSERT INTO exactcount_to.parent (id) VALUES (1)`,
		`INSERT INTO exactcount_to.child (id, parent_id)
			SELECT g, 1 FROM generate_series(1, 200000) g`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS exactcount_to CASCADE`})
	})

	fk := models.ForeignKey{
		Schema:     "exactcount_to",
		Table:      "child",
		Columns:    []string{"parent_id"},
		RefSchema:  "exactcount_to",
		RefTable:   "parent",
		RefColumns: []string{"id"},
	}

	// 1ns timeout: the context deadline is already past by the time the query
	// runs, so the COUNT is cancelled — an error the caller maps to "keep the
	// estimate".
	_, err := pg.CountPredicatedRows(ctx, sess, fk, []any{int64(1)}, 1)
	if err == nil {
		t.Fatal("expected a timeout/cancel error from a 1ns statement timeout")
	}
}
