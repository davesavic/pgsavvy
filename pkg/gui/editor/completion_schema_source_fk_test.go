package editor

import (
	"context"
	"slices"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// FK-aware JOIN ranking (ko4m.1.4). FK edges are read SYNCHRONOUSLY from the
// snapshot fake (fakeMeta.ForeignKeys); no driver/session call is on this path.
// "First" is enforced by a small additive Score boost (fkColumnBoost) applied to
// FK-participating columns within the schema source, leaving the fuzzy
// matchQuality mechanism (ko4m.3) untouched.

// scoreOf returns the Score of the first suggestion whose Text == name, or -1.
func scoreOf(sugs []Suggestion, name string) int {
	for _, s := range sugs {
		if s.Text == name {
			return s.Score
		}
	}
	return -1
}

// TestSchemaSource_FK_QualifiedON_FKColumnRanksFirst is the headline AC:
// "SELECT * FROM users u JOIN orders o ON o." with orders.user_id -> users.id,
// the FK column user_id outscores the non-FK columns of orders.
func TestSchemaSource_FK_QualifiedON_FKColumnRanksFirst(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users",
		models.Column{Name: "id"}, models.Column{Name: "email"})
	m.setColumns("public", "orders",
		models.Column{Name: "id"}, models.Column{Name: "total"}, models.Column{Name: "user_id"})
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Schema: "public", Table: "orders", Columns: []string{"user_id"},
		RefSchema: "public", RefTable: "users", RefColumns: []string{"id"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON o.")
	if !slices.Contains(texts(got), "user_id") {
		t.Fatalf("got %v; want user_id present", texts(got))
	}
	fk := scoreOf(got, "user_id")
	for _, name := range []string{"id", "total"} {
		if other := scoreOf(got, name); other >= fk {
			t.Errorf("FK column user_id Score=%d must exceed non-FK %q Score=%d", fk, name, other)
		}
	}
}

// Engine-level proof of "first": through the real Engine sort, user_id is the
// top suggestion among orders' columns.
func TestSchemaSource_FK_QualifiedON_FKColumnIsTopViaEngine(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "orders",
		models.Column{Name: "id"}, models.Column{Name: "total"}, models.Column{Name: "user_id"})
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Table: "orders", Columns: []string{"user_id"},
		RefTable: "users", RefColumns: []string{"id"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))
	eng := NewEngine([]Source{src})

	b, p := bufWithCursor("SELECT * FROM users u JOIN orders o ON o.")
	got := texts(eng.Trigger(context.Background(), b, p))
	if len(got) == 0 || got[0] != "user_id" {
		t.Fatalf("top suggestion = %v; want user_id first (FK)", got)
	}
}

// Reverse FK: completing the REFERENCED table's columns in an ON clause. With
// orders.user_id -> users.id and the cursor on `u.` (users), users.id should be
// ranked first because users is the referenced side of an in-scope FK.
func TestSchemaSource_FK_QualifiedON_ReverseSide(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users",
		models.Column{Name: "id"}, models.Column{Name: "email"})
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Table: "orders", Columns: []string{"user_id"},
		RefTable: "users", RefColumns: []string{"id"},
	})
	// orders has no FK of its own pointing back; users itself has no FK rows.
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON u.")
	if id, email := scoreOf(got, "id"), scoreOf(got, "email"); id <= email {
		t.Fatalf("reverse FK: users.id Score=%d must exceed users.email Score=%d", id, email)
	}
}

// Unqualified ON path: "JOIN orders o ON " (no dot). The unioned column list
// should rank FK columns first.
func TestSchemaSource_FK_UnqualifiedON_FKColumnRanksFirst(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"}, models.Column{Name: "email"})
	m.setColumns("public", "orders",
		models.Column{Name: "total"}, models.Column{Name: "user_id"})
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Table: "orders", Columns: []string{"user_id"},
		RefTable: "users", RefColumns: []string{"id"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON ")
	fk := scoreOf(got, "user_id")
	for _, name := range []string{"total", "email"} {
		if other := scoreOf(got, name); other >= fk {
			t.Errorf("unqualified ON: FK user_id Score=%d must exceed %q Score=%d", fk, name, other)
		}
	}
}

// Composite FK (two-column FK) handled without index error; both participating
// columns rank first.
func TestSchemaSource_FK_CompositeFK_BothColumnsRankFirst(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "parent",
		models.Column{Name: "a"}, models.Column{Name: "b"})
	m.setColumns("public", "child",
		models.Column{Name: "x"}, models.Column{Name: "pa"}, models.Column{Name: "pb"})
	m.setForeignKeys("public", "child", models.ForeignKey{
		Table: "child", Columns: []string{"pa", "pb"},
		RefTable: "parent", RefColumns: []string{"a", "b"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM parent p JOIN child c ON c.")
	x := scoreOf(got, "x")
	for _, name := range []string{"pa", "pb"} {
		if fk := scoreOf(got, name); fk <= x {
			t.Errorf("composite FK column %q Score=%d must exceed non-FK x Score=%d", name, fk, x)
		}
	}
}

// No FK between the in-scope tables: full column list returned, no reorder, no
// panic, no FK boost.
func TestSchemaSource_FK_NoEdge_NoReorderNoPanic(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "users", models.Column{Name: "id"}, models.Column{Name: "email"})
	m.setColumns("public", "orders", models.Column{Name: "id"}, models.Column{Name: "total"})
	// FK tier present but empty for both -> no edge.
	m.setForeignKeys("public", "orders")
	m.setForeignKeys("public", "users")
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON o.")
	if !equalStrings(texts(got), []string{"id", "total"}) {
		t.Fatalf("got %v; want [id total] unchanged (no FK)", texts(got))
	}
	for _, s := range got {
		if s.Score != SchemaSourceBias {
			t.Errorf("%q Score=%d; want unboosted SchemaSourceBias=%d", s.Text, s.Score, SchemaSourceBias)
		}
	}
}

// Snapshot returns no FK edges at all (ForeignKeys ok==false for every table):
// normal completion, no crash, no boost. This is the empty/boundary AC.
func TestSchemaSource_FK_SnapshotNoEdges_NormalCompletion(t *testing.T) {
	m := newFakeMeta() // foreignKey map empty -> ForeignKeys returns (nil,false)
	m.setColumns("public", "users", models.Column{Name: "id"})
	m.setColumns("public", "orders", models.Column{Name: "id"}, models.Column{Name: "total"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON o.")
	if !equalStrings(texts(got), []string{"id", "total"}) {
		t.Fatalf("got %v; want [id total] (no FK edges loaded)", texts(got))
	}
	for _, s := range got {
		if s.Score != SchemaSourceBias {
			t.Errorf("%q Score=%d; want unboosted (no FK edges)", s.Text, s.Score)
		}
	}
}

// Finding N: cross-schema FK whose referenced table is in a schema absent from
// the snapshot. The FK row exists on orders but RefSchema=app is not in scope
// (only public.users is), so no in-scope edge matches -> normal completion,
// no crash.
func TestSchemaSource_FK_CrossSchemaRefNotInScope_Fallback(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("public", "orders",
		models.Column{Name: "id"}, models.Column{Name: "tenant_id"})
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Schema: "public", Table: "orders", Columns: []string{"tenant_id"},
		RefSchema: "app", RefTable: "tenants", RefColumns: []string{"id"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	// In scope: public.users (no FK to it) and public.orders. The orders FK
	// points to app.tenants which is NOT in scope -> no boost.
	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON o.")
	if scoreOf(got, "tenant_id") != SchemaSourceBias {
		t.Errorf("tenant_id Score=%d; want unboosted (ref schema app not in scope)", scoreOf(got, "tenant_id"))
	}
}

// Cross-schema FK that IS in scope: both tables schema-qualified, FK across
// schemas resolves and the FK column ranks first.
func TestSchemaSource_FK_CrossSchemaInScope_RanksFirst(t *testing.T) {
	m := newFakeMeta()
	m.setColumns("app", "tenants", models.Column{Name: "id"})
	m.setColumns("public", "orders",
		models.Column{Name: "id"}, models.Column{Name: "tenant_id"})
	m.setForeignKeys("public", "orders", models.ForeignKey{
		Schema: "public", Table: "orders", Columns: []string{"tenant_id"},
		RefSchema: "app", RefTable: "tenants", RefColumns: []string{"id"},
	})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM app.tenants t JOIN public.orders o ON o.")
	if tid, id := scoreOf(got, "tenant_id"), scoreOf(got, "id"); tid <= id {
		t.Fatalf("cross-schema FK: tenant_id Score=%d must exceed id Score=%d", tid, id)
	}
}

// FK edge owner table unloaded (ForeignKeys ok==false for orders) must not
// crash and must not boost — Finding N fallback for an unloaded owner.
func TestSchemaSource_FK_OwnerUnloaded_NoBoostNoPanic(t *testing.T) {
	m := newFakeMeta()
	// orders columns loaded but its FK tier never set -> ForeignKeys (nil,false).
	m.setColumns("public", "orders", models.Column{Name: "id"}, models.Column{Name: "user_id"})
	src := NewSchemaSource(m, &fakeWarmer{}, schemaProv("public"))

	got := suggestLine(src, "SELECT * FROM users u JOIN orders o ON o.")
	if scoreOf(got, "user_id") != SchemaSourceBias {
		t.Errorf("user_id Score=%d; want unboosted (FK tier unloaded)", scoreOf(got, "user_id"))
	}
}
