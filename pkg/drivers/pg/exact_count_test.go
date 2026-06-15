package pg

import (
	"context"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestExactCountSQL_SimpleFK quotes identifiers and binds one $1.
func TestExactCountSQL_SimpleFK(t *testing.T) {
	fk := models.ForeignKey{
		Schema:     "public",
		Table:      "orders",
		Columns:    []string{"customer_id"},
		RefColumns: []string{"id"},
	}
	got := buildPredicatedCountSQL(fk)
	want := `SELECT COUNT(*) FROM "public"."orders" WHERE "customer_id" = $1`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestExactCountSQL_CompositeFK ANDs positional args in column order.
func TestExactCountSQL_CompositeFK(t *testing.T) {
	fk := models.ForeignKey{
		Schema:     "sales",
		Table:      "line_items",
		Columns:    []string{"order_id", "tenant_id"},
		RefColumns: []string{"id", "tenant"},
	}
	got := buildPredicatedCountSQL(fk)
	want := `SELECT COUNT(*) FROM "sales"."line_items" WHERE "order_id" = $1 AND "tenant_id" = $2`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestExactCountSQL_UnqualifiedSchema omits the schema prefix when blank.
func TestExactCountSQL_UnqualifiedSchema(t *testing.T) {
	fk := models.ForeignKey{
		Table:   "node",
		Columns: []string{"parent_id"},
	}
	got := buildPredicatedCountSQL(fk)
	want := `SELECT COUNT(*) FROM "node" WHERE "parent_id" = $1`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestExactCountSQL_QuotesEmbeddedQuote doubles an embedded double quote in an
// identifier (no injection).
func TestExactCountSQL_QuotesEmbeddedQuote(t *testing.T) {
	fk := models.ForeignKey{
		Schema:  "we\"ird",
		Table:   "ta\"ble",
		Columns: []string{"co\"l"},
	}
	got := buildPredicatedCountSQL(fk)
	want := `SELECT COUNT(*) FROM "we""ird"."ta""ble" WHERE "co""l" = $1`
	if got != want {
		t.Fatalf("SQL = %q, want %q", got, want)
	}
}

// TestCountPredicatedRows_CompositeMismatchRefused refuses (no query) when the
// value count does not match the column count.
func TestCountPredicatedRows_CompositeMismatchRefused(t *testing.T) {
	fk := models.ForeignKey{
		Schema:  "public",
		Table:   "orders",
		Columns: []string{"a", "b"},
	}
	_, err := CountPredicatedRows(context.Background(), &Session{}, fk, []any{1}, time.Second)
	if err != ErrCompositeMismatch {
		t.Fatalf("err = %v, want ErrCompositeMismatch", err)
	}
}

// TestCountPredicatedRows_EmptyColumnsRefused refuses a degenerate FK with no
// referencing columns.
func TestCountPredicatedRows_EmptyColumnsRefused(t *testing.T) {
	fk := models.ForeignKey{Schema: "public", Table: "orders"}
	_, err := CountPredicatedRows(context.Background(), &Session{}, fk, nil, time.Second)
	if err != ErrCompositeMismatch {
		t.Fatalf("err = %v, want ErrCompositeMismatch", err)
	}
}

// TestCountPredicatedRows_NilSession errors without a panic.
func TestCountPredicatedRows_NilSession(t *testing.T) {
	fk := models.ForeignKey{Schema: "public", Table: "orders", Columns: []string{"c"}}
	_, err := CountPredicatedRows(context.Background(), nil, fk, []any{1}, time.Second)
	if err == nil {
		t.Fatal("expected error for nil session")
	}
}

// TestCoerceCount narrows the COUNT(*) scalar across integer widths and tolerates
// an unexpected type (0).
func TestCoerceCount(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(1187), 1187},
		{int32(7), 7},
		{int(0), 0},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := coerceCount(c.in); got != c.want {
			t.Errorf("coerceCount(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
