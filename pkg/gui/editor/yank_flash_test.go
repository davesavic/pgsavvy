package editor

import (
	"testing"
)

func TestApplyYankFlashOverlay_CharWise_ExactRunes(t *testing.T) {
	// Half-open [0,4) over "FROM bar" must tint EXACTLY the 4 runes
	// "FROM" — the trailing space must NOT be tinted. A would-be
	// endCol++ off-by-one would tint 5 cells; this asserts it does not.
	text := "FROM bar"
	r := Range{
		Start: Position{Line: 0, Col: 0},
		End:   Position{Line: 0, Col: 4},
	}
	got := ApplyYankFlashOverlay(text, r)
	want := "\x1b[43mFROM\x1b[49m bar"
	if got != want {
		t.Fatalf("CharWise exact runes:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplyYankFlashOverlay_CharWise_Col0(t *testing.T) {
	text := "abcdef"
	r := Range{
		Start: Position{Line: 0, Col: 0},
		End:   Position{Line: 0, Col: 1},
	}
	got := ApplyYankFlashOverlay(text, r)
	want := "\x1b[43ma\x1b[49mbcdef"
	if got != want {
		t.Fatalf("Col0:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplyYankFlashOverlay_LineWise_MultiLine(t *testing.T) {
	text := "line0\nline1\nline2"
	r := Range{
		Start:    Position{Line: 0, Col: 0},
		End:      Position{Line: 1, Col: 0},
		LineWise: true,
	}
	got := ApplyYankFlashOverlay(text, r)
	want := "\x1b[43mline0\x1b[0m\n\x1b[43mline1\x1b[0m\nline2"
	if got != want {
		t.Fatalf("LineWise multi-line:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplyYankFlashOverlay_RangePastEOL(t *testing.T) {
	// colEnd past the line's rune count: tints to EOL, no panic.
	text := "abc"
	r := Range{
		Start: Position{Line: 0, Col: 1},
		End:   Position{Line: 0, Col: 99},
	}
	got := ApplyYankFlashOverlay(text, r)
	want := "a\x1b[43mbc\x1b[0m"
	if got != want {
		t.Fatalf("RangePastEOL:\n  got  %q\n  want %q", got, want)
	}
}

func TestApplyYankFlashOverlay_StartLineBeyondLineCount(t *testing.T) {
	// startLine past the last line: output byte-identical, no panic.
	text := "abc\ndef"
	r := Range{
		Start: Position{Line: 5, Col: 0},
		End:   Position{Line: 6, Col: 2},
	}
	got := ApplyYankFlashOverlay(text, r)
	if got != text {
		t.Fatalf("StartLineBeyondLineCount: output not byte-identical:\n  got  %q\n  want %q", got, text)
	}
}

func TestApplyYankFlashOverlay_EmptyText(t *testing.T) {
	got := ApplyYankFlashOverlay("", Range{})
	if got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestBuffer_YankFlashSnapshot_NilWhenUnset(t *testing.T) {
	b := &Buffer{}
	if snap := b.YankFlashSnapshot(); snap != nil {
		t.Fatalf("expected nil snapshot when unset, got %+v", snap)
	}
}

func TestBuffer_YankFlashSnapshot_ReturnsCopy(t *testing.T) {
	b := &Buffer{}
	r := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 3}}
	b.SetYankFlash(r)

	snap := b.YankFlashSnapshot()
	if snap == nil {
		t.Fatalf("expected non-nil snapshot")
	}
	// Mutating the returned copy must not affect stored state.
	snap.End.Col = 999

	snap2 := b.YankFlashSnapshot()
	if snap2 == nil || snap2.End.Col != 3 {
		t.Fatalf("snapshot is not a copy: second read = %+v", snap2)
	}
}

func TestBuffer_ClearYankFlash_MatchingEpoch(t *testing.T) {
	b := &Buffer{}
	r := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 3}}
	epoch := b.SetYankFlash(r)

	b.ClearYankFlash(epoch)
	if snap := b.YankFlashSnapshot(); snap != nil {
		t.Fatalf("expected nil after matching-epoch clear, got %+v", snap)
	}
}

func TestBuffer_ClearYankFlash_StaleGuard(t *testing.T) {
	b := &Buffer{}
	r := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 3}}
	e1 := b.SetYankFlash(r)
	e2 := b.SetYankFlash(r)

	// Stale epoch must NOT clear the current (newer) flash.
	b.ClearYankFlash(e1)
	if snap := b.YankFlashSnapshot(); snap == nil {
		t.Fatalf("stale epoch %d cleared a newer flash", e1)
	}

	// Current epoch clears.
	b.ClearYankFlash(e2)
	if snap := b.YankFlashSnapshot(); snap != nil {
		t.Fatalf("expected nil after current-epoch clear, got %+v", snap)
	}
}

func TestBuffer_YankFlash_ClearedOnMutation(t *testing.T) {
	b := &Buffer{}
	// Seed a line so there's something to edit.
	if err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 0}, End: Position{0, 0}},
		Text:  "abc",
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	b.SetYankFlash(Range{Start: Position{0, 0}, End: Position{0, 3}})
	if snap := b.YankFlashSnapshot(); snap == nil {
		t.Fatalf("flash should be armed before mutation")
	}

	// A real text edit must auto-clear the flash (on_yank parity).
	if err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 3}, End: Position{0, 3}},
		Text:  "X",
	}); err != nil {
		t.Fatalf("mutation insert: %v", err)
	}

	if snap := b.YankFlashSnapshot(); snap != nil {
		t.Fatalf("flash should be cleared after mutation, got %+v", snap)
	}
}
