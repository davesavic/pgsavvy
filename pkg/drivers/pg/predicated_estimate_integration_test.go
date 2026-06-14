//go:build integration

// Integration tests for EstimatePredicatedRows against the docker/postgres
// fixture. Mirrors the openIntegrationSession pattern from
// editability_integration_test.go. Skipped (not failed) when DBSAVVY_TEST_PG
// is unset.

package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

const predicatedEstimateTimeout = 2 * time.Second

// TestEstimatePredicatedRows_PlannerEstimate stands up a parent + child table,
// analyzes them, then confirms the predicated planner estimate for one parent
// row's children is a sane positive number (each parent has 5 children).
func TestEstimatePredicatedRows_PlannerEstimate(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS predest_test CASCADE`,
		`CREATE SCHEMA predest_test`,
		`CREATE TABLE predest_test.customers (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE predest_test.orders (
			id BIGINT PRIMARY KEY,
			customer_id BIGINT NOT NULL REFERENCES predest_test.customers(id))`,
		`INSERT INTO predest_test.customers (id) SELECT g FROM generate_series(1, 100) g`,
		`INSERT INTO predest_test.orders (id, customer_id)
			SELECT g, ((g - 1) / 5) + 1 FROM generate_series(1, 500) g`,
		`ANALYZE predest_test.customers`,
		`ANALYZE predest_test.orders`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS predest_test CASCADE`})
	})

	// Inbound FK: child = orders, referencing customers(id) via customer_id.
	fk := models.ForeignKey{
		Schema:     "predest_test",
		Table:      "orders",
		Columns:    []string{"customer_id"},
		RefSchema:  "predest_test",
		RefTable:   "customers",
		RefColumns: []string{"id"},
	}

	est, err := pg.EstimatePredicatedRows(ctx, sess, fk, []any{int64(1)}, predicatedEstimateTimeout)
	if err != nil {
		t.Fatalf("EstimatePredicatedRows: %v", err)
	}
	// 500 orders across 100 customers = ~5 each. The planner estimate should be
	// a small positive number, not the whole-table 500.
	if est <= 0 || est > 100 {
		t.Fatalf("estimate = %d, want a small positive per-row estimate (~5)", est)
	}
}

// TestEstimatePredicatedRows_QuoteContainingIdentifier proves a child table
// whose identifier contains a double quote is planned safely.
func TestEstimatePredicatedRows_QuoteContainingIdentifier(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS predest_quote CASCADE`,
		`CREATE SCHEMA predest_quote`,
		`CREATE TABLE predest_quote.parent (id INT PRIMARY KEY)`,
		`CREATE TABLE predest_quote."ch""ild" (
			id INT PRIMARY KEY,
			"pa""rent" INT NOT NULL REFERENCES predest_quote.parent(id))`,
		`INSERT INTO predest_quote.parent (id) VALUES (1)`,
		`INSERT INTO predest_quote."ch""ild" (id, "pa""rent") VALUES (1, 1), (2, 1)`,
		`ANALYZE predest_quote.parent`,
		`ANALYZE predest_quote."ch""ild"`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS predest_quote CASCADE`})
	})

	fk := models.ForeignKey{
		Schema:     "predest_quote",
		Table:      `ch"ild`,
		Columns:    []string{`pa"rent`},
		RefSchema:  "predest_quote",
		RefTable:   "parent",
		RefColumns: []string{"id"},
	}
	est, err := pg.EstimatePredicatedRows(ctx, sess, fk, []any{int32(1)}, predicatedEstimateTimeout)
	if err != nil {
		t.Fatalf("EstimatePredicatedRows (quote id): %v", err)
	}
	if est < 0 {
		t.Fatalf("estimate = %d, want >= 0", est)
	}
}
