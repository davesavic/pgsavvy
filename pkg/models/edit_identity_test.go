package models

import (
	"errors"
	"reflect"
	"testing"
)

func TestPendingIdentityOwnership(t *testing.T) {
	id := EditIdentity{RelationOID: 42, Columns: []IdentityColumn{{1, "a"}, {2, "b"}}}
	reversed := EditIdentity{RelationOID: 42, Columns: []IdentityColumn{{2, "b"}, {1, "a"}}}
	set := &PendingEditSet{Table: Ref{Schema: "app", Table: "items"}}
	first := PendingEdit{PrimaryKey: []any{1, 2}, Column: "value", OldValue: "old", NewValue: "first"}
	if err := set.AddWithIdentity(id, first); err != nil {
		t.Fatal(err)
	}
	id.Columns[0].Name = "caller-mutated"
	if err := set.AddWithIdentity(reversed, PendingEdit{PrimaryKey: []any{2, 1}, Column: "value", OldValue: "not-the-baseline", NewValue: "second"}); err != nil {
		t.Fatal(err)
	}
	if got := set.Edits(); len(got) != 1 || !reflect.DeepEqual(got[0].PrimaryKey, []any{1, 2}) || got[0].OldValue != "old" || got[0].NewValue != "second" {
		t.Fatalf("same physical key was reinterpreted: %+v", got)
	}
	if _, ok := set.FindWithIdentity(reversed, []any{2, 1}, "value"); !ok {
		t.Fatal("same-key reordered lookup did not find the edit")
	}
	alternate := EditIdentity{RelationOID: 42, Columns: []IdentityColumn{{3, "c"}, {4, "d"}}}
	before := set.Edits()
	for _, incompatible := range []EditIdentity{alternate, {RelationOID: 99, Columns: reversed.Columns}, {RelationOID: 42, Columns: []IdentityColumn{{1, "a"}}}} {
		values := make([]any, len(incompatible.Columns))
		for i := range values {
			values[i] = i + 1
		}
		if err := set.AddWithIdentity(incompatible, PendingEdit{PrimaryKey: values, Column: "value"}); !errors.Is(err, ErrEditIdentityMismatch) {
			t.Fatalf("incompatible key admitted: %v", err)
		}
		if !reflect.DeepEqual(set.Edits(), before) {
			t.Fatal("rejected key changed pending edits")
		}
	}
	snapshot, err := set.BeginCommit(nil)
	if err != nil {
		t.Fatal(err)
	}
	copyID := snapshot.Identity()
	copyID.Columns[0].Name = "mutated-snapshot-accessor"
	if snapshot.Identity().Columns[0].Name != "a" || set.Identity().Columns[0].Name != "a" {
		t.Fatal("identity accessor exposes its backing storage")
	}
	set.Clear()
	if err := set.AddWithIdentity(alternate, first); !errors.Is(err, ErrEditIdentityMismatch) {
		t.Fatal("clear released a reserved identity")
	}
	set.SettleCommit(snapshot, false, true, [][]any{{1, 2}}, [][]any{{1, 2}})
	if err := set.AddWithIdentity(alternate, first); !errors.Is(err, ErrEditIdentityMismatch) {
		t.Fatal("unknown commit outcome released its identity")
	}
}

func TestPendingIdentityRemoveAndRebindQuiescent(t *testing.T) {
	id := EditIdentity{RelationOID: 42, Columns: []IdentityColumn{{1, "a"}, {2, "b"}}}
	reverse := EditIdentity{RelationOID: 42, Columns: []IdentityColumn{{2, "b"}, {1, "a"}}}
	set := &PendingEditSet{}
	e := PendingEdit{PrimaryKey: []any{1, 2}, Column: "value"}
	if err := set.AddWithIdentity(id, e); err != nil {
		t.Fatal(err)
	}
	if err := set.RemoveWithIdentity(reverse, []any{2, 1}, "value"); err != nil || !set.IsEmpty() {
		t.Fatalf("reordered discard failed: %v", err)
	}
	id.Columns = []IdentityColumn{{3, "c"}, {4, "d"}}
	if err := set.AddWithIdentity(id, e); err != nil {
		t.Fatalf("fully quiescent identity cannot rebind: %v", err)
	}
}
