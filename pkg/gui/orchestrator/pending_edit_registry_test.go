package orchestrator

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestPendingEditRegistry_ForReturnsSameInstance(t *testing.T) {
	r := newPendingEditRegistry()
	a := r.For("conn1", "public.users")
	b := r.For("conn1", "public.users")
	if a == nil || a != b {
		t.Fatalf("For returned distinct instances for same key (a=%p b=%p)", a, b)
	}
}

func TestPendingEditRegistry_ForDistinctKeysDistinctSets(t *testing.T) {
	r := newPendingEditRegistry()
	a := r.For("conn1", "public.users")
	bConn := r.For("conn2", "public.users")
	bTable := r.For("conn1", "public.orders")
	if a == nil || bConn == nil || bTable == nil {
		t.Fatalf("unexpected nil: a=%v bConn=%v bTable=%v", a, bConn, bTable)
	}
	if a == bConn || a == bTable || bConn == bTable {
		t.Fatalf("expected distinct sets for distinct keys")
	}
}

func TestPendingEditRegistry_ForEmptyKeyReturnsNil(t *testing.T) {
	r := newPendingEditRegistry()
	cases := []struct {
		conn, table string
	}{
		{"", "public.t"},
		{"c", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := r.For(tc.conn, tc.table); got != nil {
			t.Fatalf("For(%q,%q) = %v, want nil", tc.conn, tc.table, got)
		}
	}
}

func TestPendingEditRegistry_LookupReturnsNilUntilFor(t *testing.T) {
	r := newPendingEditRegistry()
	if got := r.Lookup("conn1", "public.users"); got != nil {
		t.Fatalf("Lookup before For = %v, want nil", got)
	}
	_ = r.For("conn1", "public.users")
	if got := r.Lookup("conn1", "public.users"); got == nil {
		t.Fatalf("Lookup after For = nil, want non-nil")
	}
}

func TestPendingEditRegistry_ForStampsTableRef(t *testing.T) {
	r := newPendingEditRegistry()
	s := r.For("conn1", "public.users")
	want := models.Ref{Schema: "public", Table: "users"}
	if s.Table != want {
		t.Fatalf("Table = %+v, want %+v", s.Table, want)
	}
	bare := r.For("conn1", "users_only")
	if bare.Table != (models.Ref{Table: "users_only"}) {
		t.Fatalf("bare Table = %+v, want Schema=\"\" Table=\"users_only\"", bare.Table)
	}
}

func TestPendingEditSet_HasEdit(t *testing.T) {
	var s models.PendingEditSet
	pk := []any{42}
	if s.HasEdit(pk, "name") {
		t.Fatalf("HasEdit on empty set = true, want false")
	}
	if err := s.Add(models.PendingEdit{PrimaryKey: pk, Column: "name", NewValue: "bob", Kind: models.Literal}); err != nil {
		t.Fatalf("Add err = %v", err)
	}
	if !s.HasEdit(pk, "name") {
		t.Fatalf("HasEdit after Add = false, want true")
	}
	if s.HasEdit(pk, "other") {
		t.Fatalf("HasEdit on missing column = true, want false")
	}
	if s.HasEdit([]any{99}, "name") {
		t.Fatalf("HasEdit on missing pk = true, want false")
	}
}
