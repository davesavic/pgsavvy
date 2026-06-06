package editor

import (
	"errors"
	"testing"
)

func TestNewBufferInitsMarksAndJumps(t *testing.T) {
	b := NewBuffer()
	if b.Marks == nil {
		t.Fatal("NewBuffer().Marks is nil; want non-nil map")
	}
	if b.Jumps == nil {
		t.Fatal("NewBuffer().Jumps is nil; want non-nil JumpList")
	}
	if got := b.Jumps.Len(); got != 0 {
		t.Fatalf("fresh JumpList Len = %d, want 0", got)
	}
}

func TestSetMarkLowercase(t *testing.T) {
	b := NewBuffer()
	pos := Position{Line: 3, Col: 7}
	if err := SetMark(b, 'a', pos); err != nil {
		t.Fatalf("SetMark('a') err = %v, want nil", err)
	}
	got, ok := GetMark(b, 'a')
	if !ok {
		t.Fatal("GetMark('a') ok = false, want true")
	}
	if got != pos {
		t.Fatalf("GetMark('a') = %+v, want %+v", got, pos)
	}
}

func TestSetMarkUppercaseRejected(t *testing.T) {
	b := NewBuffer()
	err := SetMark(b, 'A', Position{Line: 0, Col: 0})
	if !errors.Is(err, ErrInvalidMark) {
		t.Fatalf("SetMark('A') err = %v, want ErrInvalidMark", err)
	}
}

func TestSetMarkNonLetterRejected(t *testing.T) {
	b := NewBuffer()
	for _, r := range []rune{'!', '0', '\'', ' ', '`', '{', '@'} {
		if err := SetMark(b, r, Position{}); !errors.Is(err, ErrInvalidMark) {
			t.Errorf("SetMark(%q) err = %v, want ErrInvalidMark", r, err)
		}
	}
}

func TestGetMarkMissing(t *testing.T) {
	b := NewBuffer()
	if _, ok := GetMark(b, 'z'); ok {
		t.Fatal("GetMark on never-set mark returned ok=true")
	}
}

func TestGetMarkInvalidRuneIsMiss(t *testing.T) {
	b := NewBuffer()
	if _, ok := GetMark(b, 'A'); ok {
		t.Fatal("GetMark('A') ok = true, want false")
	}
}

func TestMarksSurviveNonOverlappingApply(t *testing.T) {
	b := NewBuffer()
	b.Lines = []Line{
		{Runes: []rune("line one")},
		{Runes: []rune("line two")},
		{Runes: []rune("line three")},
	}
	pos := Position{Line: 0, Col: 2}
	if err := SetMark(b, 'a', pos); err != nil {
		t.Fatalf("SetMark err = %v", err)
	}
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{Line: 2, Col: 4}, End: Position{Line: 2, Col: 4}},
		Text:  "X",
	})
	if err != nil {
		t.Fatalf("Apply err = %v", err)
	}
	got, ok := GetMark(b, 'a')
	if !ok || got != pos {
		t.Fatalf("mark after edit on different line: got=(%+v,%v), want=(%+v,true)", got, ok, pos)
	}
}

func TestJumpListPushBelowCap(t *testing.T) {
	j := newJumpList()
	for i := range 5 {
		j.Push(Position{Line: i})
	}
	if got := j.Len(); got != 5 {
		t.Fatalf("Len = %d, want 5", got)
	}
	for i := range 5 {
		if got := j.At(i); got.Line != i {
			t.Errorf("At(%d).Line = %d, want %d", i, got.Line, i)
		}
	}
}

func TestJumpListCapExact100(t *testing.T) {
	j := newJumpList()
	for i := range jumpListCap {
		j.Push(Position{Line: i})
	}
	if got := j.Len(); got != jumpListCap {
		t.Fatalf("Len after exactly cap pushes = %d, want %d", got, jumpListCap)
	}
}

func TestJumpListRingEvictionAt101(t *testing.T) {
	j := newJumpList()
	for i := range jumpListCap + 1 {
		j.Push(Position{Line: i})
	}
	if got := j.Len(); got != jumpListCap {
		t.Fatalf("Len after cap+1 pushes = %d, want %d", got, jumpListCap)
	}
	// Oldest survivor should be Line=1 (index 0 was evicted).
	if got := j.At(0); got.Line != 1 {
		t.Fatalf("At(0).Line after eviction = %d, want 1", got.Line)
	}
	if got := j.At(jumpListCap - 1); got.Line != jumpListCap {
		t.Fatalf("At(last).Line = %d, want %d", got.Line, jumpListCap)
	}
}

func TestJumpListLenStableAt100ThroughManyPushes(t *testing.T) {
	j := newJumpList()
	for i := range 250 {
		j.Push(Position{Line: i})
	}
	if got := j.Len(); got != jumpListCap {
		t.Fatalf("Len after 250 pushes = %d, want %d", got, jumpListCap)
	}
	// Oldest survivor is 250-100 = 150; newest is 249.
	if got := j.At(0); got.Line != 150 {
		t.Errorf("At(0).Line = %d, want 150", got.Line)
	}
	if got := j.At(jumpListCap - 1); got.Line != 249 {
		t.Errorf("At(last).Line = %d, want 249", got.Line)
	}
}

func TestJumpListAtOutOfRange(t *testing.T) {
	j := newJumpList()
	if got := j.At(0); got != (Position{}) {
		t.Errorf("At on empty = %+v, want zero", got)
	}
	if got := j.At(-1); got != (Position{}) {
		t.Errorf("At(-1) = %+v, want zero", got)
	}
	j.Push(Position{Line: 7})
	if got := j.At(5); got != (Position{}) {
		t.Errorf("At past Len = %+v, want zero", got)
	}
}

func TestSetCursorEmptyBufferErrors(t *testing.T) {
	b := NewBuffer()
	if err := SetCursor(b, Position{Line: 0, Col: 0}); !errors.Is(err, ErrEmptyBuffer) {
		t.Fatalf("SetCursor on empty err = %v, want ErrEmptyBuffer", err)
	}
}

func TestSetCursorLineOutOfRange(t *testing.T) {
	b := NewBuffer()
	b.Lines = []Line{{Runes: []rune("abc")}}
	if err := SetCursor(b, Position{Line: 5, Col: 0}); !errors.Is(err, ErrCursorOutOfRange) {
		t.Fatalf("SetCursor line=5 err = %v, want ErrCursorOutOfRange", err)
	}
	if err := SetCursor(b, Position{Line: -1, Col: 0}); !errors.Is(err, ErrCursorOutOfRange) {
		t.Fatalf("SetCursor line=-1 err = %v, want ErrCursorOutOfRange", err)
	}
}

func TestSetCursorClampsCol(t *testing.T) {
	b := NewBuffer()
	b.Lines = []Line{{Runes: []rune("hello")}}
	if err := SetCursor(b, Position{Line: 0, Col: 99}); err != nil {
		t.Fatalf("SetCursor clamp err = %v, want nil", err)
	}
	if got := b.CursorPos(); got.Col != 5 {
		t.Fatalf("Col after clamp-high = %d, want 5 (rune-len)", got.Col)
	}
	if err := SetCursor(b, Position{Line: 0, Col: -3}); err != nil {
		t.Fatalf("SetCursor neg-col err = %v, want nil", err)
	}
	if got := b.CursorPos(); got.Col != 0 {
		t.Fatalf("Col after clamp-neg = %d, want 0", got.Col)
	}
}

func TestSetCursorAcceptsLineEnd(t *testing.T) {
	b := NewBuffer()
	b.Lines = []Line{{Runes: []rune("xyz")}}
	if err := SetCursor(b, Position{Line: 0, Col: 3}); err != nil {
		t.Fatalf("SetCursor col=rune-len err = %v, want nil (append-past-end is valid)", err)
	}
	if got := b.CursorPos(); got != (Position{Line: 0, Col: 3}) {
		t.Fatalf("Cursor = %+v, want {0,3}", got)
	}
}

func TestSetCursorMultibyteClampsToRuneLen(t *testing.T) {
	b := NewBuffer()
	b.Lines = []Line{{Runes: []rune("héllo")}} // 5 runes, 6 bytes
	if err := SetCursor(b, Position{Line: 0, Col: 99}); err != nil {
		t.Fatalf("SetCursor err = %v", err)
	}
	if got := b.CursorPos(); got.Col != 5 {
		t.Fatalf("Col = %d, want 5 (clamp to rune count, not byte count)", got.Col)
	}
}
