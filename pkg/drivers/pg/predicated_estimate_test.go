package pg

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestPredicatedEstimateSQL_SimpleFK quotes identifiers and binds one $1.
func TestPredicatedEstimateSQL_SimpleFK(t *testing.T) {
	fk := models.ForeignKey{
		Schema:     "public",
		Table:      "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	}
	got := buildPredicatedEstimateSQL(fk)
	want := `SELECT * FROM "public"."orders" WHERE "customer_id" = $1`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestPredicatedEstimateSQL_CompositeFK ANDs positional args in column order.
func TestPredicatedEstimateSQL_CompositeFK(t *testing.T) {
	fk := models.ForeignKey{
		Schema:     "sales",
		Table:      "line_items",
		Columns:    []string{"order_id", "tenant_id"},
		RefColumns: []string{"id", "tenant"},
	}
	got := buildPredicatedEstimateSQL(fk)
	want := `SELECT * FROM "sales"."line_items" WHERE "order_id" = $1 AND "tenant_id" = $2`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestPredicatedEstimateSQL_UnqualifiedSchema omits the schema prefix when
// blank (self-referential / search-path resolved).
func TestPredicatedEstimateSQL_UnqualifiedSchema(t *testing.T) {
	fk := models.ForeignKey{
		Table:   "node",
		Columns: []string{"parent_id"},
	}
	got := buildPredicatedEstimateSQL(fk)
	want := `SELECT * FROM "node" WHERE "parent_id" = $1`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestPredicatedEstimateSQL_QuotesEmbeddedQuote doubles an embedded double
// quote in an identifier (no injection).
func TestPredicatedEstimateSQL_QuotesEmbeddedQuote(t *testing.T) {
	fk := models.ForeignKey{
		Schema:  "we\"ird",
		Table:   "ta\"ble",
		Columns: []string{"co\"l"},
	}
	got := buildPredicatedEstimateSQL(fk)
	want := `SELECT * FROM "we""ird"."ta""ble" WHERE "co""l" = $1`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestEstimatePredicatedRows_CompositeMismatchRefused refuses (no query) when
// the value count does not match the column count.
func TestPredicatedEstimate_CompositeMismatchRefused(t *testing.T) {
	fk := models.ForeignKey{
		Schema:  "public",
		Table:   "orders",
		Columns: []string{"a", "b"},
	}
	_, err := EstimatePredicatedRows(context.Background(), &Session{}, fk, []any{1}, time.Second)
	if err != ErrCompositeMismatch {
		t.Fatalf("err = %v, want ErrCompositeMismatch", err)
	}
}

// TestEstimatePredicatedRows_EmptyColumnsRefused refuses a degenerate FK with
// no referencing columns.
func TestPredicatedEstimate_EmptyColumnsRefused(t *testing.T) {
	fk := models.ForeignKey{Schema: "public", Table: "orders"}
	_, err := EstimatePredicatedRows(context.Background(), &Session{}, fk, nil, time.Second)
	if err != ErrCompositeMismatch {
		t.Fatalf("err = %v, want ErrCompositeMismatch", err)
	}
}

// TestEstimatePredicatedRows_NilSession errors without a panic.
func TestPredicatedEstimate_NilSession(t *testing.T) {
	fk := models.ForeignKey{Schema: "public", Table: "orders", Columns: []string{"c"}}
	_, err := EstimatePredicatedRows(context.Background(), nil, fk, []any{1}, time.Second)
	if err == nil {
		t.Fatal("expected error for nil session")
	}
}
