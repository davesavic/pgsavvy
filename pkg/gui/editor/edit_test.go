package editor

import "testing"

func TestEditReverseZeroEditReturnsZero(t *testing.T) {
	e := Edit{}
	rev := e.Reverse()
	if rev.Kind != 0 {
		t.Fatalf("zero Edit.Reverse().Kind = %d, want 0", rev.Kind)
	}
}

func TestEditReverseUnrecordedReturnsZero(t *testing.T) {
	// A freshly-constructed Edit (never passed through Buffer.Apply)
	// has no captured reverse — Reverse returns the zero Edit so
	// callers can detect "not invertible without buffer context".
	e := Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 0}, End: Position{0, 0}},
		Text:  "x",
	}
	if rev := e.Reverse(); rev.Kind != 0 {
		t.Fatalf("unrecorded Edit.Reverse().Kind = %d, want 0", rev.Kind)
	}
}

func TestEditReverseAfterApplyCapturesInverse(t *testing.T) {
	b := bufFromLines("ab")
	mustApply(t, b, Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 1}, End: Position{0, 1}},
		Text:  "X",
	})
	cur := b.History.Current()
	if cur == nil {
		t.Fatal("History.Current() is nil after Apply")
	}
	rev := cur.Edit().Reverse()
	if rev.Kind != EditKindDelete {
		t.Fatalf("reverse of Insert.Kind = %d, want EditKindDelete (%d)", rev.Kind, EditKindDelete)
	}
	if rev.Range.Start != (Position{0, 1}) || rev.Range.End != (Position{0, 2}) {
		t.Fatalf("reverse Range = %+v, want Start{0,1} End{0,2}", rev.Range)
	}
}

func TestEditReverseOfDeleteCapturesText(t *testing.T) {
	b := bufFromLines("hello")
	mustApply(t, b, Edit{
		Kind:  EditKindDelete,
		Range: Range{Start: Position{0, 1}, End: Position{0, 4}},
	})
	cur := b.History.Current()
	rev := cur.Edit().Reverse()
	if rev.Kind != EditKindInsert {
		t.Fatalf("reverse of Delete.Kind = %d, want EditKindInsert", rev.Kind)
	}
	if rev.Text != "ell" {
		t.Fatalf("reverse Insert.Text = %q, want %q", rev.Text, "ell")
	}
}

func TestEditReverseOfReplace(t *testing.T) {
	b := bufFromLines("abcdef")
	mustApply(t, b, Edit{
		Kind:  EditKindReplace,
		Range: Range{Start: Position{0, 1}, End: Position{0, 4}},
		Text:  "ZZ",
	})
	// Buffer is now "aZZef".
	cur := b.History.Current()
	rev := cur.Edit().Reverse()
	if rev.Kind != EditKindReplace {
		t.Fatalf("reverse of Replace.Kind = %d, want EditKindReplace", rev.Kind)
	}
	if rev.Text != "bcd" {
		t.Fatalf("reverse Text = %q, want %q", rev.Text, "bcd")
	}
	if rev.Range.End != (Position{0, 3}) {
		t.Fatalf("reverse Range.End = %+v, want {0,3}", rev.Range.End)
	}
}

func TestAdvancePosNoNewlines(t *testing.T) {
	got := advancePos(Position{0, 2}, "abc")
	want := Position{0, 5}
	if got != want {
		t.Fatalf("advancePos = %+v, want %+v", got, want)
	}
}

func TestAdvancePosWithNewlines(t *testing.T) {
	got := advancePos(Position{2, 4}, "a\nbb\nccc")
	want := Position{4, 3}
	if got != want {
		t.Fatalf("advancePos = %+v, want %+v", got, want)
	}
}

func TestAdvancePosUnicode(t *testing.T) {
	got := advancePos(Position{0, 0}, "héllo")
	want := Position{0, 5} // 5 runes regardless of byte length
	if got != want {
		t.Fatalf("advancePos = %+v, want %+v", got, want)
	}
}
