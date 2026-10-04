package models

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"
	"testing"
	"time"
)

func TestReview_async_commit_model(t *testing.T) {
	for _, outcome := range []string{"committed", "cancelled", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			s := &PendingEditSet{Table: Ref{Schema: "a", Table: "items"}}
			e := PendingEdit{PrimaryKey: []any{1}, Column: "n", Kind: Expression, NewExpr: "n + 1"}
			if err := s.Add(e); err != nil {
				t.Fatal(err)
			}
			snapshot, err := s.BeginCommit(nil)
			if err != nil {
				t.Fatal(err)
			}
			if !s.CommitInFlight() {
				t.Fatal("operation not marked busy")
			}
			if _, err := s.BeginCommit(nil); !errors.Is(err, ErrCommitInFlight) {
				t.Fatalf("duplicate submission: %v", err)
			}
			otherColumn := e
			otherColumn.Column = "other"
			if err := s.Add(otherColumn); !errors.Is(err, ErrCommitInFlight) {
				t.Fatalf("same-row restaging: %v", err)
			}
			unrelated := e
			unrelated.PrimaryKey = []any{2}
			if err := s.Add(unrelated); err != nil {
				t.Fatal(err)
			}
			s.Remove(e.PrimaryKey, e.Column)
			if err := s.Add(e); !errors.Is(err, ErrCommitInFlight) {
				t.Fatalf("discard bypassed busy guard: %v", err)
			}
			s.SettleCommit(snapshot, outcome == "committed", outcome == "unknown", [][]any{{1}}, [][]any{{11}})
			if s.CommitInFlight() {
				t.Fatal("settlement left busy guard")
			}
			if outcome == "unknown" {
				if !s.CommitOutcomeUnknown() {
					t.Fatal("unknown receipt lost after discard")
				}
				if err := s.Add(e); !errors.Is(err, ErrCommitOutcomeUnknown) {
					t.Fatalf("unknown row retry: %v", err)
				}
				if _, err := s.BeginCommit(nil); !errors.Is(err, ErrCommitOutcomeUnknown) {
					t.Fatalf("unknown operation retry: %v", err)
				}
				if err := s.ReconcileCommitOutcome(false); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Add(e); err != nil {
				t.Fatal(err)
			}
			// A duplicate old receipt must not settle a newer operation.
			next, err := s.BeginCommit(nil)
			if err != nil {
				t.Fatal(err)
			}
			s.SettleCommit(snapshot, true, false, [][]any{{1}}, [][]any{{11}})
			if !s.CommitInFlight() || s.Count() != 2 {
				t.Fatal("old receipt settled new intent")
			}
			s.SettleCommit(next, true, false, [][]any{{1}, {2}}, [][]any{{11}, {2}})
			if !s.IsEmpty() {
				t.Fatal("matching committed versions remain replayable")
			}
		})
	}
}

func TestReview_async_commit_snapshot(t *testing.T) {
	s := &PendingEditSet{Table: Ref{Schema: "a", Table: "items"}}
	value := map[string]any{"items": []any{[]byte{3}}}
	if err := s.Add(PendingEdit{PrimaryKey: []any{[]byte{1}}, Column: "payload", NewValue: value}); err != nil {
		t.Fatal(err)
	}
	selected := s.Edits()
	snapshot, err := s.BeginCommit(selected)
	if err != nil {
		t.Fatal(err)
	}
	selected[0].NewValue.(map[string]any)["items"].([]any)[0].([]byte)[0] = 9
	copy := snapshot.Edits()
	if copy[0].NewValue.(map[string]any)["items"].([]any)[0].([]byte)[0] != 3 {
		t.Fatal("snapshot aliases selection")
	}
	copy[0].PrimaryKey[0].([]byte)[0] = 8
	if snapshot.Edits()[0].PrimaryKey[0].([]byte)[0] != 1 {
		t.Fatal("snapshot aliases reader")
	}
	s.Clear()
	if err := s.Add(PendingEdit{PrimaryKey: []any{[]byte{1}}, Column: "other"}); !errors.Is(err, ErrCommitInFlight) {
		t.Fatalf("Clear bypassed guard: %v", err)
	}
	if err := s.ReconcileCommitOutcome(false); !errors.Is(err, ErrCommitInFlight) {
		t.Fatalf("reconcile during operation: %v", err)
	}
	s.SettleCommit(snapshot, true, false, [][]any{{[]byte{1}}}, [][]any{{[]byte{1}}})
	if err := s.Add(PendingEdit{PrimaryKey: []any{[]byte{1}}, Column: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCommit(selected); !errors.Is(err, ErrCommitSnapshotStale) {
		t.Fatalf("stale selection: %v", err)
	}
	var other PendingEditSet
	if err := other.Add(PendingEdit{PrimaryKey: []any{[]byte{1}}, Column: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginCommit(other.Edits()); !errors.Is(err, ErrCommitSnapshotStale) {
		t.Fatalf("foreign selection: %v", err)
	}
}

func TestReview_row_outcome_snapshot_ownership(t *testing.T) {
	var s PendingEditSet
	key := []any{[]byte{1}}
	value := map[string]any{"items": []any{[]byte{2}}}
	if err := s.Add(PendingEdit{PrimaryKey: key, Column: "payload", NewValue: value}); err != nil {
		t.Fatal(err)
	}
	key[0].([]byte)[0] = 9
	value["items"].([]any)[0].([]byte)[0] = 9
	first := s.Edits()
	if first[0].PrimaryKey[0].([]byte)[0] != 1 || first[0].NewValue.(map[string]any)["items"].([]any)[0].([]byte)[0] != 2 {
		t.Fatal("staged snapshot aliases caller input")
	}
	first[0].PrimaryKey[0].([]byte)[0] = 8
	first[0].NewValue.(map[string]any)["items"].([]any)[0].([]byte)[0] = 8
	second := s.Edits()
	if second[0].PrimaryKey[0].([]byte)[0] != 1 || second[0].NewValue.(map[string]any)["items"].([]any)[0].([]byte)[0] != 2 {
		t.Fatal("Edits snapshot aliases pending state")
	}
}

func TestReview_row_outcome_numeric_pointer_snapshot(t *testing.T) {
	var s PendingEditSet
	value := struct{ Int *big.Int }{Int: big.NewInt(123)}
	if err := s.Add(PendingEdit{PrimaryKey: []any{1}, Column: "numeric", NewValue: value}); err != nil {
		t.Fatal(err)
	}
	value.Int.SetInt64(9)
	copy := s.Edits()[0].NewValue.(struct{ Int *big.Int })
	if copy.Int.Int64() != 123 {
		t.Fatal("numeric snapshot retained caller pointer")
	}
	copy.Int.SetInt64(8)
	if s.Edits()[0].NewValue.(struct{ Int *big.Int }).Int.Int64() != 123 {
		t.Fatal("numeric snapshot retained returned pointer")
	}
}

func TestReview_row_outcome_NaN_retirement(t *testing.T) {
	var s PendingEditSet
	if err := s.Add(PendingEdit{PrimaryKey: []any{math.NaN()}, Column: "n", OldValue: math.NaN(), NewValue: math.NaN()}); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Edits()
	s.Retire(snapshot)
	if !s.IsEmpty() {
		t.Fatal("NaN prevented confirmed snapshot retirement")
	}
}

func TestReview_row_outcome_retire_versions(t *testing.T) {
	var s PendingEditSet
	e := PendingEdit{PrimaryKey: []any{1}, Column: "n", Kind: Expression, NewExpr: "n + 1"}
	if err := s.Add(e); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Edits()
	// Even an identical restaging is a new intent, not the old committed version.
	if err := s.Add(e); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(PendingEdit{PrimaryKey: []any{2}, Column: "n", NewValue: 5}); err != nil {
		t.Fatal(err)
	}
	s.Retire(snapshot)
	if s.Count() != 2 {
		t.Fatal("old receipt retired replacement or unrelated row")
	}
	s.Retire(s.Edits())
	if !s.IsEmpty() {
		t.Fatal("matching committed snapshot not retired")
	}
}

func TestReview_row_outcome_receipt_owner(t *testing.T) {
	var first, second PendingEditSet
	e := PendingEdit{PrimaryKey: []any{1}, Column: "n", NewValue: 2}
	for _, set := range []*PendingEditSet{&first, &second} {
		if err := set.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	first.Retire(second.Edits())
	if first.Count() != 1 {
		t.Fatal("same local revision from another owner retired this set")
	}
	first.Retire(first.Edits())
	if !first.IsEmpty() {
		t.Fatal("correct owner receipt did not retire")
	}
}

func TestReview_row_outcome_surviving_identity(t *testing.T) {
	var s PendingEditSet
	for _, col := range []string{"id", "n"} {
		if err := s.Add(PendingEdit{PrimaryKey: []any{1}, Column: col, NewValue: 2}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := s.Edits()[:1]
	s.RetireCommitted(snapshot, [][]any{{1}}, [][]any{{11}})
	if s.Count() != 1 || !s.HasEdit([]any{11}, "n") || s.HasEdit([]any{1}, "n") {
		t.Fatal("remaining same-row edit kept obsolete identity")
	}
}

func TestReview_row_outcome_unknown_reconciliation(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_committed", true: "committed"}[committed], func(t *testing.T) {
			var s PendingEditSet
			e := PendingEdit{PrimaryKey: []any{1}, Column: "n", Kind: Expression, NewExpr: "n + 1"}
			if err := s.Add(e); err != nil {
				t.Fatal(err)
			}
			s.MarkCommitUnknown(s.Edits(), [][]any{{1}}, [][]any{{1}})
			if err := s.Add(e); !errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatalf("restaging unknown row: %v", err)
			}
			if err := s.Add(PendingEdit{PrimaryKey: []any{2}, Column: "n", NewValue: 2}); err != nil {
				t.Fatal(err)
			}
			if err := s.ReconcileCommitOutcome(committed); err != nil {
				t.Fatal(err)
			}
			want := 2
			if committed {
				want = 1
			}
			if s.Count() != want || s.CommitOutcomeUnknown() {
				t.Fatalf("reconciliation state count=%d unknown=%v", s.Count(), s.CommitOutcomeUnknown())
			}
		})
	}
	var s PendingEditSet
	e := PendingEdit{PrimaryKey: []any{1}, Column: "n", NewValue: 2}
	if err := s.Add(e); err != nil {
		t.Fatal(err)
	}
	s.MarkCommitUnknown(s.Edits(), [][]any{{1}}, [][]any{{1}})
	s.Clear()
	if !s.CommitOutcomeUnknown() {
		t.Fatal("Clear silently reconciled uncertain COMMIT")
	}
	if err := s.Add(e); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("Clear allowed replay: %v", err)
	}
	if err := s.ReconcileCommitOutcome(false); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(e); err != nil {
		t.Fatal(err)
	}
}

func TestReview_row_outcome_unknown_identity_validation(t *testing.T) {
	for _, result := range [][][]any{nil, {{}}, {{nil}}, {{11}, {12}}} {
		t.Run(fmt.Sprint(result), func(t *testing.T) {
			var s PendingEditSet
			e := PendingEdit{PrimaryKey: []any{1}, Column: "n", NewValue: 2}
			if err := s.Add(e); err != nil {
				t.Fatal(err)
			}
			s.MarkCommitUnknown(s.Edits(), [][]any{{1}}, result)
			if err := s.ReconcileCommitOutcome(true); !errors.Is(err, ErrCommitIdentityUnresolved) || !s.CommitOutcomeUnknown() || s.Count() != 1 {
				t.Fatal("invalid mapping silently reconciled uncertain write")
			}
		})
	}
	var s PendingEditSet
	for _, col := range []string{"id", "n"} {
		if err := s.Add(PendingEdit{PrimaryKey: []any{1}, Column: col, NewValue: 2}); err != nil {
			t.Fatal(err)
		}
	}
	result := [][]any{{11}}
	s.MarkCommitUnknown(s.Edits()[:1], [][]any{{1}}, result)
	result[0][0] = 99
	original, returned := s.CommitUnknownIdentities()
	original[0][0] = 99
	returned[0][0] = 99
	if err := s.ReconcileCommitOutcome(true); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 1 || !s.HasEdit([]any{11}, "n") {
		t.Fatal("unknown receipt aliased caller or failed survivor rekey")
	}
}

func TestReview_row_outcome_unknown_ambiguous_mappings(t *testing.T) {
	for _, tc := range []struct {
		name             string
		original, result [][]any
	}{
		{"duplicate_result", [][]any{{1}, {2}}, [][]any{{99}, {99}}},
		{"extraneous_original", [][]any{{1}, {2}, {3}}, [][]any{{11}, {12}, {33}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s PendingEditSet
			for _, id := range []int{1, 2, 3} {
				if err := s.Add(PendingEdit{PrimaryKey: []any{id}, Column: "n", NewValue: 2}); err != nil {
					t.Fatal(err)
				}
			}
			s.MarkCommitUnknown(s.Edits()[:2], tc.original, tc.result)
			if err := s.ReconcileCommitOutcome(true); !errors.Is(err, ErrCommitIdentityUnresolved) || !s.CommitOutcomeUnknown() || s.Count() != 3 || !s.HasEdit([]any{3}, "n") {
				t.Fatal("ambiguous receipt retired snapshots or changed unrelated identity")
			}
		})
	}
}

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
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range perG {
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
