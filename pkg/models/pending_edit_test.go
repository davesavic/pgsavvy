package models

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPendingEditSet_AddAndCount(t *testing.T) {
	var s PendingEditSet
	if err := s.Add(PendingEdit{
		PrimaryKey: []any{1},
		Column:     "name",
		OldValue:   "alice",
		NewValue:   "bob",
		Kind:       Literal,
		LoadedAt:   time.Unix(100, 0),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1", got)
	}
	if s.IsEmpty() {
		t.Fatal("IsEmpty = true, want false")
	}
}

func TestPendingEditSet_AddReplacesPreservingOldValueAndLoadedAt(t *testing.T) {
	var s PendingEditSet
	firstLoaded := time.Unix(100, 0)
	if err := s.Add(PendingEdit{
		PrimaryKey: []any{1},
		Column:     "name",
		OldValue:   "alice",
		NewValue:   "bob",
		Kind:       Literal,
		LoadedAt:   firstLoaded,
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	secondLoaded := time.Unix(200, 0)
	if err := s.Add(PendingEdit{
		PrimaryKey: []any{1},
		Column:     "name",
		OldValue:   "different-old", // must be ignored
		NewValue:   "carol",
		Kind:       Literal,
		LoadedAt:   secondLoaded, // must be ignored
	}); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1 (replacement)", got)
	}
	edits := s.Edits()
	if edits[0].NewValue != "carol" {
		t.Errorf("NewValue = %v, want carol", edits[0].NewValue)
	}
	if edits[0].OldValue != "alice" {
		t.Errorf("OldValue = %v, want preserved alice", edits[0].OldValue)
	}
	if !edits[0].LoadedAt.Equal(firstLoaded) {
		t.Errorf("LoadedAt = %v, want preserved %v", edits[0].LoadedAt, firstLoaded)
	}
}

func TestPendingEditSet_AddReplaceSwitchesKindAndExpression(t *testing.T) {
	var s PendingEditSet
	if err := s.Add(PendingEdit{
		PrimaryKey: []any{7},
		Column:     "count",
		OldValue:   1,
		NewValue:   2,
		Kind:       Literal,
		LoadedAt:   time.Unix(1, 0),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(PendingEdit{
		PrimaryKey: []any{7},
		Column:     "count",
		NewExpr:    "count + 1",
		Kind:       Expression,
		LoadedAt:   time.Unix(2, 0),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	edits := s.Edits()
	if len(edits) != 1 {
		t.Fatalf("len = %d, want 1", len(edits))
	}
	if edits[0].Kind != Expression || edits[0].NewExpr != "count + 1" {
		t.Errorf("Kind=%v NewExpr=%q, want Expression / 'count + 1'", edits[0].Kind, edits[0].NewExpr)
	}
	if edits[0].OldValue != 1 {
		t.Errorf("OldValue = %v, want preserved 1", edits[0].OldValue)
	}
}

func TestPendingEditSet_CompositePKDistinctRows(t *testing.T) {
	var s PendingEditSet
	mustAdd(t, &s, PendingEdit{PrimaryKey: []any{1, "a"}, Column: "x", NewValue: "v1", Kind: Literal})
	mustAdd(t, &s, PendingEdit{PrimaryKey: []any{1, "b"}, Column: "x", NewValue: "v2", Kind: Literal})
	if got := s.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2 (composite PK should be distinct)", got)
	}
}

func TestPendingEditSet_Remove(t *testing.T) {
	cases := []struct {
		name      string
		removePK  []any
		removeCol string
		wantCount int
	}{
		{"existing", []any{1}, "name", 0},
		{"missing column", []any{1}, "other", 1},
		{"missing pk", []any{2}, "name", 1},
		{"missing both", []any{99}, "nope", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s PendingEditSet
			mustAdd(t, &s, PendingEdit{PrimaryKey: []any{1}, Column: "name", NewValue: "bob", Kind: Literal})
			// Should never panic.
			s.Remove(tc.removePK, tc.removeCol)
			if got := s.Count(); got != tc.wantCount {
				t.Fatalf("Count after Remove = %d, want %d", got, tc.wantCount)
			}
		})
	}
}

func TestPendingEditSet_Clear(t *testing.T) {
	var s PendingEditSet
	mustAdd(t, &s, PendingEdit{PrimaryKey: []any{1}, Column: "a", NewValue: 1, Kind: Literal})
	mustAdd(t, &s, PendingEdit{PrimaryKey: []any{2}, Column: "b", NewValue: 2, Kind: Literal})
	s.Clear()
	if !s.IsEmpty() {
		t.Fatal("IsEmpty = false after Clear, want true")
	}
	if got := s.Count(); got != 0 {
		t.Fatalf("Count = %d after Clear, want 0", got)
	}
}

func TestPendingEditSet_AddEmptyPrimaryKey(t *testing.T) {
	cases := []struct {
		name string
		pk   []any
	}{
		{"nil", nil},
		{"empty", []any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s PendingEditSet
			err := s.Add(PendingEdit{PrimaryKey: tc.pk, Column: "c", NewValue: 1, Kind: Literal})
			if !errors.Is(err, ErrEmptyPrimaryKey) {
				t.Fatalf("err = %v, want ErrEmptyPrimaryKey", err)
			}
			if got := s.Count(); got != 0 {
				t.Fatalf("Count = %d, want 0 after rejected Add", got)
			}
		})
	}
}

func TestPendingEditSet_EditsReturnsCopy(t *testing.T) {
	var s PendingEditSet
	mustAdd(t, &s, PendingEdit{PrimaryKey: []any{1}, Column: "name", NewValue: "bob", Kind: Literal})
	got := s.Edits()
	got[0].NewValue = "MUTATED"
	again := s.Edits()
	if again[0].NewValue != "bob" {
		t.Fatalf("internal state mutated via returned slice: %v", again[0].NewValue)
	}
}

func TestPendingEditSet_ConcurrentRace(t *testing.T) {
	var s PendingEditSet
	const goroutines = 32
	const perG = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				pk := []any{g, i}
				_ = s.Add(PendingEdit{
					PrimaryKey: pk,
					Column:     "c",
					OldValue:   nil,
					NewValue:   i,
					Kind:       Literal,
					LoadedAt:   time.Now(),
				})
				_ = s.Count()
				_ = s.Edits()
				if i%5 == 0 {
					s.Remove(pk, "c")
				}
			}
		}()
	}
	wg.Wait()
	// Final state consistent: Count matches Edits length, IsEmpty matches.
	c := s.Count()
	e := s.Edits()
	if c != len(e) {
		t.Fatalf("Count=%d, len(Edits)=%d", c, len(e))
	}
	if (c == 0) != s.IsEmpty() {
		t.Fatalf("IsEmpty inconsistent with Count=%d", c)
	}
}

func mustAdd(t *testing.T, s *PendingEditSet, e PendingEdit) {
	t.Helper()
	if err := s.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}
}
