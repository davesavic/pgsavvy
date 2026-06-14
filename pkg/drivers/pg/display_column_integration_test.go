//go:build integration

// Integration tests for ResolveDisplayValue against the docker/postgres
// fixture. Mirrors the openIntegrationSession pattern from
// fk_loader_integration_test.go. Skipped (not failed) when DBSAVVY_TEST_PG
// is unset.

package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/models"
)

const displayValueTimeout = 2 * time.Second

// TestResolveDisplayValue_HumanReadablePreview stands up a transient parent
// table with a name column and confirms ResolveDisplayValue returns the
// parent row's display value for an outbound FK reference.
func TestResolveDisplayValue_HumanReadablePreview(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS dispval_test CASCADE`,
		`CREATE SCHEMA dispval_test`,
		`CREATE TABLE dispval_test.customers (id BIGINT PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO dispval_test.customers (id, name) VALUES (42, 'Acme Corp')`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS dispval_test CASCADE`})
	})

	fk := models.ForeignKey{
		RefSchema:  "dispval_test",
		RefTable:   "customers",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	}
	got, err := pg.ResolveDisplayValue(ctx, sess, fk, []any{int64(42)}, displayValueTimeout)
	if err != nil {
		t.Fatalf("ResolveDisplayValue: %v", err)
	}
	if got != "Acme Corp" {
		t.Fatalf("display value = %v, want \"Acme Corp\"", got)
	}
}

// TestResolveDisplayValue_QuoteContainingIdentifier proves a table whose
// identifier contains a double-quote is resolved safely (no injection, no
// malformed SQL) — the negative-security path against live PG.
func TestResolveDisplayValue_QuoteContainingIdentifier(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// A table name containing a literal double quote. Identifiers are quoted
	// via QuoteIdent (doubles the embedded "), so this must round-trip.
	stmts := []string{
		`DROP SCHEMA IF EXISTS dispval_quote CASCADE`,
		`CREATE SCHEMA dispval_quote`,
		`CREATE TABLE dispval_quote."we""ird" (id INT PRIMARY KEY, "na""me" TEXT)`,
		`INSERT INTO dispval_quote."we""ird" (id, "na""me") VALUES (1, 'safe')`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS dispval_quote CASCADE`})
	})

	fk := models.ForeignKey{
		RefSchema:  "dispval_quote",
		RefTable:   `we"ird`,
		Columns:    []string{"ref"},
		RefColumns: []string{"id"},
	}
	got, err := pg.ResolveDisplayValue(ctx, sess, fk, []any{int32(1)}, displayValueTimeout)
	if err != nil {
		t.Fatalf("ResolveDisplayValue (quote id): %v", err)
	}
	if got != "safe" {
		t.Fatalf("display value = %v, want \"safe\"", got)
	}
}

// TestResolveDisplayValue_CompositeMismatchRefused confirms a mismatched
// composite FK is refused without issuing a query.
func TestResolveDisplayValue_CompositeMismatchRefused(t *testing.T) {
	sess := openIntegrationSession(t)
	fk := models.ForeignKey{
		RefSchema:  "public",
		RefTable:   "anything",
		RefColumns: []string{"a", "b"},
	}
	_, err := pg.ResolveDisplayValue(context.Background(), sess, fk, []any{1}, displayValueTimeout)
	if err != pg.ErrCompositeMismatch {
		t.Fatalf("err = %v, want ErrCompositeMismatch", err)
	}
}
