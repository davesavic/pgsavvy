package editor

import "testing"

// motionBuf constructs a Buffer from a slice of plain strings.
// Helper for table tests — keeps the motion expectations easy to read.
func motionBuf(lines ...string) *Buffer {
	b := NewBuffer()
	if len(lines) == 0 {
		b.Lines = []Line{{Runes: []rune{}}}
		return b
	}
	out := make([]Line, len(lines))
	for i, s := range lines {
		out[i] = Line{Runes: []rune(s)}
	}
	b.Lines = out
	return b
}

func TestCharRightAdvancesAndWrapsNewline(t *testing.T) {
	b := motionBuf("abc", "de")
	got, ok := CharRight(b, Position{Line: 0, Col: 2}, 1)
	if !ok {
		t.Fatal("CharRight on 'c' returned ok=false")
	}
	if got != (Position{Line: 0, Col: 3}) {
		t.Fatalf("CharRight = %+v, want {0,3}", got)
	}
	got, ok = CharRight(b, Position{Line: 0, Col: 3}, 1)
	if !ok || got != (Position{Line: 1, Col: 0}) {
		t.Fatalf("CharRight across newline = %+v ok=%v, want {1,0} ok=true", got, ok)
	}
}

func TestCharLeftWrapsNewlineAndBOFStops(t *testing.T) {
	b := motionBuf("abc", "de")
	got, ok := CharLeft(b, Position{Line: 1, Col: 0}, 1)
	if !ok || got != (Position{Line: 0, Col: 3}) {
		t.Fatalf("CharLeft across newline = %+v ok=%v, want {0,3} ok=true", got, ok)
	}
	if _, ok := CharLeft(b, Position{Line: 0, Col: 0}, 1); ok {
		t.Fatal("CharLeft at BOF returned ok=true")
	}
}

func TestLineDownClampsColumn(t *testing.T) {
	b := motionBuf("hello", "hi")
	got, ok := LineDown(b, Position{Line: 0, Col: 4}, 1)
	if !ok || got != (Position{Line: 1, Col: 2}) {
		t.Fatalf("LineDown clamp = %+v ok=%v, want {1,2} ok=true", got, ok)
	}
}

// TestLineDownOvershootClampsToLastLine pins the AC edge case for `5j`
// on a 3-line buffer: cursor must clamp to the last line (index 2),
// not silently no-op or panic.
func TestLineDownOvershootClampsToLastLine(t *testing.T) {
	b := motionBuf("one", "two", "three")
	got, ok := LineDown(b, Position{Line: 0, Col: 0}, 5)
	if !ok || got != (Position{Line: 2, Col: 0}) {
		t.Fatalf("LineDown count=5 on 3-line buf = %+v ok=%v, want {2,0} ok=true", got, ok)
	}
}

func TestLineEndAndLineStart(t *testing.T) {
	b := motionBuf("  hello world")
	end, ok := LineEnd(b, Position{Line: 0, Col: 0}, 1)
	if !ok || end != (Position{Line: 0, Col: 13}) {
		t.Fatalf("LineEnd = %+v ok=%v, want {0,13}", end, ok)
	}
	start, ok := LineStart(b, Position{Line: 0, Col: 5}, 1)
	if !ok || start != (Position{Line: 0, Col: 0}) {
		t.Fatalf("LineStart = %+v ok=%v, want {0,0}", start, ok)
	}
	fnb, ok := LineFirstNonBlank(b, Position{Line: 0, Col: 0}, 1)
	if !ok || fnb != (Position{Line: 0, Col: 2}) {
		t.Fatalf("LineFirstNonBlank = %+v ok=%v, want {0,2}", fnb, ok)
	}
}

func TestBufferStartAndEnd(t *testing.T) {
	b := motionBuf("one", "two", "three")
	start, ok := BufferStart(b, Position{Line: 2, Col: 3}, 1)
	if !ok || start != (Position{Line: 0, Col: 0}) {
		t.Fatalf("BufferStart = %+v ok=%v, want {0,0}", start, ok)
	}
	end, ok := BufferEnd(b, Position{Line: 0, Col: 0}, 1)
	if !ok || end != (Position{Line: 2, Col: 0}) {
		t.Fatalf("BufferEnd = %+v ok=%v, want {2,0}", end, ok)
	}
}

// TestBufferEndCountedJumpsToLine pins vim `NG` semantics: `5G` lands
// on line 5 (0-indexed line 4), and a count past EOF clamps to the
// last line.
func TestBufferEndCountedJumpsToLine(t *testing.T) {
	b := motionBuf("a", "b", "c", "d", "e", "f", "g")
	got, ok := BufferEnd(b, Position{Line: 0, Col: 0}, 5)
	if !ok || got != (Position{Line: 4, Col: 0}) {
		t.Fatalf("BufferEnd count=5 = %+v ok=%v, want {4,0} ok=true", got, ok)
	}
	// Count past EOF clamps to last line.
	got, ok = BufferEnd(b, Position{Line: 0, Col: 0}, 99)
	if !ok || got != (Position{Line: 6, Col: 0}) {
		t.Fatalf("BufferEnd count=99 = %+v ok=%v, want {6,0} ok=true", got, ok)
	}
}

func TestWordNextBasic(t *testing.T) {
	b := motionBuf("hello world foo")
	got, ok := WordNext(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got != (Position{Line: 0, Col: 6}) {
		t.Fatalf("WordNext = %+v ok=%v, want {0,6}", got, ok)
	}
	got, ok = WordNext(b, Position{Line: 0, Col: 0}, 2)
	if !ok || got != (Position{Line: 0, Col: 12}) {
		t.Fatalf("WordNext count=2 = %+v ok=%v, want {0,12}", got, ok)
	}
}

func TestWordNextPunctClassTransition(t *testing.T) {
	// "foo.bar" — word w jumps from 'f' to '.' to 'b'.
	b := motionBuf("foo.bar")
	got, ok := WordNext(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got != (Position{Line: 0, Col: 3}) {
		t.Fatalf("WordNext 'foo.bar' first = %+v ok=%v, want {0,3}", got, ok)
	}
	got, ok = WordNext(b, Position{Line: 0, Col: 3}, 1)
	if !ok || got != (Position{Line: 0, Col: 4}) {
		t.Fatalf("WordNext 'foo.bar' second = %+v ok=%v, want {0,4}", got, ok)
	}
}

func TestWORDNextIgnoresPunct(t *testing.T) {
	b := motionBuf("foo.bar baz")
	got, ok := WORDNext(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got != (Position{Line: 0, Col: 8}) {
		t.Fatalf("WORDNext = %+v ok=%v, want {0,8}", got, ok)
	}
}

func TestWordPrev(t *testing.T) {
	b := motionBuf("hello world foo")
	got, ok := WordPrev(b, Position{Line: 0, Col: 12}, 1)
	if !ok || got != (Position{Line: 0, Col: 6}) {
		t.Fatalf("WordPrev = %+v ok=%v, want {0,6}", got, ok)
	}
}

func TestWordEndLandsOnLastRune(t *testing.T) {
	b := motionBuf("hello world")
	got, ok := WordEnd(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got != (Position{Line: 0, Col: 4}) {
		t.Fatalf("WordEnd = %+v ok=%v, want {0,4}", got, ok)
	}
}

func TestWordNextWrapsLine(t *testing.T) {
	b := motionBuf("foo", "bar")
	got, ok := WordNext(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got != (Position{Line: 1, Col: 0}) {
		t.Fatalf("WordNext across newline = %+v ok=%v, want {1,0}", got, ok)
	}
}

func TestParagraphMotions(t *testing.T) {
	b := motionBuf(
		"line 1",
		"line 2",
		"",
		"line 4",
		"line 5",
		"",
		"line 7",
	)
	// Forward from line 0 lands on the first blank (line 2).
	got, ok := ParagraphNext(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got.Line != 2 {
		t.Fatalf("ParagraphNext = %+v ok=%v, want line=2", got, ok)
	}
	got, ok = ParagraphNext(b, Position{Line: 0, Col: 0}, 2)
	if !ok || got.Line != 5 {
		t.Fatalf("ParagraphNext count=2 = %+v ok=%v, want line=5", got, ok)
	}
	// Backward from line 6 lands on blank line 5; another step on blank 2.
	got, ok = ParagraphPrev(b, Position{Line: 6, Col: 0}, 1)
	if !ok || got.Line != 5 {
		t.Fatalf("ParagraphPrev = %+v ok=%v, want line=5", got, ok)
	}
}

func TestSentenceNext(t *testing.T) {
	b := motionBuf("First. Second sentence. Third.")
	got, ok := SentenceNext(b, Position{Line: 0, Col: 0}, 1)
	if !ok || got != (Position{Line: 0, Col: 7}) {
		t.Fatalf("SentenceNext = %+v ok=%v, want {0,7}", got, ok)
	}
}

func TestNegativeCountRejected(t *testing.T) {
	b := motionBuf("hello world")
	if _, ok := WordNext(b, Position{Line: 0, Col: 0}, -1); ok {
		t.Fatal("WordNext(-1) ok=true, want false")
	}
	if _, ok := CharRight(b, Position{Line: 0, Col: 0}, -3); ok {
		t.Fatal("CharRight(-3) ok=true, want false")
	}
	if _, ok := BufferStart(b, Position{Line: 0, Col: 0}, -1); ok {
		t.Fatal("BufferStart(-1) ok=true, want false")
	}
}

func TestZeroCountRejected(t *testing.T) {
	b := motionBuf("abc")
	// Controllers normalise 0→1 before calling, but the motion layer
	// itself treats 0 as a rejection so the convention is explicit.
	if _, ok := CharRight(b, Position{Line: 0, Col: 0}, 0); ok {
		t.Fatal("CharRight(0) ok=true, want false")
	}
}

func TestEmptyBufferRejected(t *testing.T) {
	b := NewBuffer() // No Lines.
	if _, ok := CharRight(b, Position{}, 1); ok {
		t.Fatal("CharRight on empty buf ok=true, want false")
	}
	if _, ok := WordNext(b, Position{}, 1); ok {
		t.Fatal("WordNext on empty buf ok=true, want false")
	}
}

func TestScreenStubsAreBufferRelative(t *testing.T) {
	b := motionBuf("one", "two", "three", "four", "five")
	top, ok := ScreenTop(b, Position{Line: 4, Col: 0}, 1)
	if !ok || top != (Position{Line: 0, Col: 0}) {
		t.Fatalf("ScreenTop = %+v ok=%v, want {0,0}", top, ok)
	}
	mid, ok := ScreenMiddle(b, Position{Line: 0, Col: 0}, 1)
	if !ok || mid.Line != 2 {
		t.Fatalf("ScreenMiddle = %+v ok=%v, want line=2", mid, ok)
	}
	bot, ok := ScreenBottom(b, Position{Line: 0, Col: 0}, 1)
	if !ok || bot != (Position{Line: 4, Col: 0}) {
		t.Fatalf("ScreenBottom = %+v ok=%v, want {4,0}", bot, ok)
	}
}
