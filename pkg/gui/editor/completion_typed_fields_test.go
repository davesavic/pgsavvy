package editor

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// Typed presentation-field population: Schema/Function/Keywords
// sources populate Kind/Detail/IsPrimaryKey/NotNull/FKRef from the warmed
// snapshot. All reads go through fakeMeta (the SchemaMetadata fake) — no
// drivers.Session is on any of these paths.

// suggestionFor returns the first suggestion whose Text == name, or a zero
// Suggestion and false when absent.
func suggestionFor(sugs []Suggestion, name string) (Suggestion, bool) {
	for _, s := range sugs {
		if s.Text == name {
			return s, true
		}
	}
	return Suggestion{}, false
}

// TestSchemaSource_WarmedColumn_TypedFields: a warmed column carries
// Kind=column, Detail=DataType, IsPrimaryKey/NotNull from the snapshot, and a
// non-FK column has FKRef=="". Text stays the bare name.
func TestSchemaSource_WarmedColumn_TypedFields(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users",
		models.Column{Name: "id", DataType: "integer", Nullable: false, IsPrimaryKey: true},
		models.Column{Name: "email", DataType: "text", Nullable: true},
	)
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT users.")

	id, ok := suggestionFor(got, "id")
	if !ok {
		t.Fatalf("missing id suggestion; got %v", texts(got))
	}
	if id.Kind != KindColumn {
		t.Errorf("id.Kind = %q; want %q", id.Kind, KindColumn)
	}
	if id.Detail != "integer" {
		t.Errorf("id.Detail = %q; want integer", id.Detail)
	}
	if !id.IsPrimaryKey {
		t.Error("id.IsPrimaryKey = false; want true")
	}
	if !id.NotNull {
		t.Error("id.NotNull = false; want true (Nullable=false)")
	}
	if id.FKRef != "" {
		t.Errorf("id.FKRef = %q; want \"\" (no FK)", id.FKRef)
	}
	if id.Text != "id" {
		t.Errorf("id.Text = %q; want bare name id", id.Text)
	}

	email, _ := suggestionFor(got, "email")
	if email.NotNull {
		t.Error("email.NotNull = true; want false (Nullable=true)")
	}
	if email.IsPrimaryKey {
		t.Error("email.IsPrimaryKey = true; want false")
	}
}

// TestSchemaSource_FKColumn_FKRef: a column with an FK edge carries
// FKRef="refschema.reftable.refcol".
func TestSchemaSource_FKColumn_FKRef(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "orders",
		models.Column{Name: "user_id", DataType: "integer"},
		models.Column{Name: "total", DataType: "numeric"},
	)
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Schema: "public", Table: "orders", Columns: []string{"user_id"},
		RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT orders.")

	userID, _ := suggestionFor(got, "user_id")
	if userID.FKRef != "public.users.id" {
		t.Errorf("user_id.FKRef = %q; want public.users.id", userID.FKRef)
	}
	total, _ := suggestionFor(got, "total")
	if total.FKRef != "" {
		t.Errorf("total.FKRef = %q; want \"\" (no FK)", total.FKRef)
	}
}

// TestSchemaSource_CompositeFK_PositionalRef: a composite FK pairs Columns[i]
// with RefColumns[i] positionally.
func TestSchemaSource_CompositeFK_PositionalRef(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "line_items",
		models.Column{Name: "order_id", DataType: "integer"},
		models.Column{Name: "tenant_id", DataType: "integer"},
	)
	m.setForeignKeys("public", "line_items", models.ForeignKey{
		Schema: "public", Table: "line_items",
		Columns:   []string{"order_id", "tenant_id"},
		RefSchema: "public", RefTable: "orders",
		RefColumns: []string{"id", "tenant"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT line_items.")

	orderID, _ := suggestionFor(got, "order_id")
	if orderID.FKRef != "public.orders.id" {
		t.Errorf("order_id.FKRef = %q; want public.orders.id (Columns[0]->RefColumns[0])", orderID.FKRef)
	}
	tenantID, _ := suggestionFor(got, "tenant_id")
	if tenantID.FKRef != "public.orders.tenant" {
		t.Errorf("tenant_id.FKRef = %q; want public.orders.tenant (Columns[1]->RefColumns[1])", tenantID.FKRef)
	}
}

// TestSchemaSource_UnwarmedTable_ZeroTypedFields: an unwarmed table fires a
// warm and returns empty (no suggestion to carry typed fields). This is the
// ok==false branch — no error, no block, no driver call.
func TestSchemaSource_UnwarmedTable_ZeroTypedFields(t *testing.T) {
	m := newFakeMeta() // users not loaded
	w := &fakeWarmer{}
	src := NewSchemaSource(m, w, schemaProv("public"))
	got := suggestLine(src, "SELECT users.")
	if len(got) != 0 {
		t.Fatalf("got %v; want empty (unwarmed columns)", texts(got))
	}
}

// TestSchemaSource_WarmedColumnsNoFKEdge_FKRefEmpty: columns warmed but the
// table has no FK edge loaded (ForeignKeys ok==false) — FKRef stays empty,
// IsPrimaryKey reflects the column, no block.
func TestSchemaSource_WarmedColumnsNoFKEdge_FKRefEmpty(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id", DataType: "integer", IsPrimaryKey: true})
	// No setForeignKeys -> ForeignKeys returns ok==false.
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT users.")
	id, _ := suggestionFor(got, "id")
	if id.FKRef != "" {
		t.Errorf("id.FKRef = %q; want \"\" (no FK edges loaded)", id.FKRef)
	}
	if !id.IsPrimaryKey {
		t.Error("id.IsPrimaryKey = false; want true (PK still surfaced without FK edges)")
	}
}

// TestSchemaSource_TableSuggestion_KindTable: a table-context suggestion
// carries Kind=table. (View distinction is unavailable from the name-only
// eager tier — documented ASSUMPTION in tableSuggestion.)
func TestSchemaSource_TableSuggestion_KindTable(t *testing.T) {
	m := newFakeMeta()
	m.setTables("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	got := suggestLine(src, "SELECT * FROM ")
	users, ok := suggestionFor(got, "users")
	if !ok {
		t.Fatalf("missing users; got %v", texts(got))
	}
	if users.Kind != KindTable {
		t.Errorf("users.Kind = %q; want %q", users.Kind, KindTable)
	}
	if users.Detail != "" {
		t.Errorf("users.Detail = %q; want \"\" (table needs no detail)", users.Detail)
	}
}

// TestFunctionSource_TypedFields: a function suggestion carries Kind=function,
// Detail="fn".
func TestFunctionSource_TypedFields(t *testing.T) {
	m := newFakeMeta()
	m.setFunctions("now", "count")
	src := NewFunctionSource(m)
	b, p := bufWithCursor("SELECT no")
	got := src.Suggest(context.Background(), b, p)
	now, ok := suggestionFor(got, "now")
	if !ok {
		t.Fatalf("missing now; got %v", texts(got))
	}
	if now.Kind != KindFunction {
		t.Errorf("now.Kind = %q; want %q", now.Kind, KindFunction)
	}
	if now.Detail != "fn" {
		t.Errorf("now.Detail = %q; want fn", now.Detail)
	}
}

// TestKeywordsSource_TypedFields: a keyword suggestion carries Kind=keyword,
// Detail="kw".
func TestKeywordsSource_TypedFields(t *testing.T) {
	src := KeywordsSource{PriorityVal: KeywordSourceBias}
	b, p := bufWithCursor("SEL")
	got := src.Suggest(context.Background(), b, p)
	sel, ok := suggestionFor(got, "SELECT")
	if !ok {
		t.Fatalf("missing SELECT; got %v", texts(got))
	}
	if sel.Kind != KindKeyword {
		t.Errorf("SELECT.Kind = %q; want %q", sel.Kind, KindKeyword)
	}
	if sel.Detail != "kw" {
		t.Errorf("SELECT.Detail = %q; want kw", sel.Detail)
	}
}
