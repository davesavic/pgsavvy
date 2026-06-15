package pg

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestDisplayColumnHeuristic(t *testing.T) {
	tests := []struct {
		name string
		cols []models.Column
		want string
	}{
		{
			name: "prefers a column literally named name",
			cols: []models.Column{
				{Name: "id", DataType: "integer", IsPrimaryKey: true},
				{Name: "code", DataType: "text"},
				{Name: "name", DataType: "varchar"},
			},
			want: "name",
		},
		{
			name: "name beats title beats label",
			cols: []models.Column{
				{Name: "label", DataType: "text"},
				{Name: "title", DataType: "text"},
				{Name: "name", DataType: "text"},
			},
			want: "name",
		},
		{
			name: "title chosen when name absent",
			cols: []models.Column{
				{Name: "label", DataType: "text"},
				{Name: "title", DataType: "text"},
			},
			want: "title",
		},
		{
			name: "first text-like column when no preferred name",
			cols: []models.Column{
				{Name: "id", DataType: "bigint", IsPrimaryKey: true},
				{Name: "description", DataType: "text"},
				{Name: "note", DataType: "varchar(50)"},
			},
			want: "description",
		},
		{
			name: "skips the PK even when it is text-like",
			cols: []models.Column{
				{Name: "slug", DataType: "text", IsPrimaryKey: true},
				{Name: "headline", DataType: "character varying"},
			},
			want: "headline",
		},
		{
			name: "falls back to the PK when no text-like column exists",
			cols: []models.Column{
				{Name: "id", DataType: "integer", IsPrimaryKey: true},
				{Name: "amount", DataType: "numeric"},
				{Name: "created", DataType: "timestamptz"},
			},
			want: "id",
		},
		{
			name: "name-typed column (pg 'name' type) counts as text-like",
			cols: []models.Column{
				{Name: "oid_col", DataType: "oid", IsPrimaryKey: true},
				{Name: "relname", DataType: "name"},
			},
			want: "relname",
		},
		{
			name: "empty when neither text nor PK",
			cols: []models.Column{
				{Name: "amount", DataType: "numeric"},
			},
			want: "",
		},
		{
			name: "nil columns yields empty",
			cols: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayColumn(tt.cols); got != tt.want {
				t.Fatalf("DisplayColumn() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildDisplayValueSQLQuotesIdentifiers(t *testing.T) {
	fk := models.ForeignKey{
		RefSchema:  "public",
		RefTable:   "customers",
		RefColumns: []string{"id"},
	}
	got := buildDisplayValueSQL(fk, "name")
	want := `SELECT "name" FROM "public"."customers" WHERE "id" = $1 LIMIT 1`
	if got != want {
		t.Fatalf("buildDisplayValueSQL() = %q, want %q", got, want)
	}
}

func TestBuildDisplayValueSQLComposite(t *testing.T) {
	fk := models.ForeignKey{
		RefSchema:  "inv",
		RefTable:   "lines",
		RefColumns: []string{"order_id", "seq"},
	}
	got := buildDisplayValueSQL(fk, "descr")
	want := `SELECT "descr" FROM "inv"."lines" WHERE "order_id" = $1 AND "seq" = $2 LIMIT 1`
	if got != want {
		t.Fatalf("buildDisplayValueSQL() composite = %q, want %q", got, want)
	}
}

// TestBuildDisplayValueSQLQuoteContainingIdentifier proves a double-quote in
// any identifier is doubled (PostgreSQL escaping) rather than terminating the
// quote — no injection, no malformed SQL.
func TestBuildDisplayValueSQLQuoteContainingIdentifier(t *testing.T) {
	fk := models.ForeignKey{
		RefSchema:  `we"ird`,
		RefTable:   `ta"ble`,
		RefColumns: []string{`co"l`},
	}
	got := buildDisplayValueSQL(fk, `na"me`)
	// Every embedded " must be doubled; the surrounding quotes must remain
	// balanced (no early termination).
	if strings.Count(got, `""`) != 4 {
		t.Fatalf("expected 4 doubled quotes (escaped identifiers), got SQL: %q", got)
	}
	want := `SELECT "na""me" FROM "we""ird"."ta""ble" WHERE "co""l" = $1 LIMIT 1`
	if got != want {
		t.Fatalf("buildDisplayValueSQL() with quote-containing ids = %q, want %q", got, want)
	}
}
