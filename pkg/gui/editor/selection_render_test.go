package editor

import (
	"testing"
)

func TestTintWholeLine(t *testing.T) {
	line := "\x1b[31mSELECT\x1b[0m foo"
	got := tintWholeLine(line)
	want := "\x1b[100m\x1b[31mSELECT\x1b[0;100m foo\x1b[0m"
	if got != want {
		t.Fatalf("tintWholeLine:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_LineWise(t *testing.T) {
	text := "line0\nline1\nline2"
	sel := Range{
		Start:    Position{Line: 0, Col: 0},
		End:      Position{Line: 1, Col: 0},
		LineWise: true,
	}
	got := ApplySelectionOverlay(text, sel)
	want := "\x1b[100mline0\x1b[0m\n\x1b[100mline1\x1b[0m\nline2"
	if got != want {
		t.Fatalf("LineWise:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_CharWise_SingleLine(t *testing.T) {
	// Selection [2,6] inclusive → renders [2,7) half-open
	text := "hello world"
	sel := Range{
		Start: Position{Line: 0, Col: 2},
		End:   Position{Line: 0, Col: 6},
	}
	got := ApplySelectionOverlay(text, sel)
	want := "he\x1b[100mllo w\x1b[49morld"
	if got != want {
		t.Fatalf("CharWise single line:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_CharWise_MultiLine(t *testing.T) {
	// Selection [0,1] to [2,1] inclusive → end col renders as 2
	text := "aaa\nbbb\nccc"
	sel := Range{
		Start: Position{Line: 0, Col: 1},
		End:   Position{Line: 2, Col: 1},
	}
	got := ApplySelectionOverlay(text, sel)
	want := "a\x1b[100maa\x1b[0m\n\x1b[100mbbb\x1b[0m\n\x1b[100mcc\x1b[49mc"
	if got != want {
		t.Fatalf("CharWise multi-line:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_BackwardSelection(t *testing.T) {
	// Backward: End < Start, normalised to [1,3] inclusive → [1,4) half-open
	text := "abcdef"
	sel := Range{
		Start: Position{Line: 0, Col: 3},
		End:   Position{Line: 0, Col: 1},
	}
	got := ApplySelectionOverlay(text, sel)
	want := "a\x1b[100mbcd\x1b[49mef"
	if got != want {
		t.Fatalf("Backward:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_SingleChar(t *testing.T) {
	// v with no motion: Start == End, should highlight one char
	text := "abcdef"
	sel := Range{
		Start: Position{Line: 0, Col: 0},
		End:   Position{Line: 0, Col: 0},
	}
	got := ApplySelectionOverlay(text, sel)
	want := "\x1b[100ma\x1b[49mbcdef"
	if got != want {
		t.Fatalf("SingleChar:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_WithANSI(t *testing.T) {
	text := "\x1b[31mSELECT\x1b[0m \x1b[34m*\x1b[0m"
	// Select cols 0..7 inclusive = all 8 visible runes → [0, 8) half-open
	sel := Range{
		Start: Position{Line: 0, Col: 0},
		End:   Position{Line: 0, Col: 7},
	}
	got := ApplySelectionOverlay(text, sel)
	want := "\x1b[31m\x1b[100mSELECT\x1b[0;100m \x1b[34m*\x1b[49m\x1b[0m"
	if got != want {
		t.Fatalf("WithANSI:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplySelectionOverlay_EmptyText(t *testing.T) {
	got := ApplySelectionOverlay("", Range{})
	if got != "" {
		t.Fatalf("empty: got %q", got)
	}
}
