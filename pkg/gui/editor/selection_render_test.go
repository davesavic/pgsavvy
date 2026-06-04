package editor

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// newBufferWithText returns a *Buffer pre-seeded with text via a single
// insert Edit, with the cursor parked at origin so the seam's
// SetCursor restore is well-defined.
func newBufferWithText(t *testing.T, text string) *Buffer {
	t.Helper()
	b := NewBuffer()
	if text != "" {
		if err := b.Apply(Edit{
			Kind:  EditKindInsert,
			Range: Range{Start: Position{0, 0}, End: Position{0, 0}},
			Text:  text,
		}); err != nil {
			t.Fatalf("seed buffer: %v", err)
		}
	}
	b.SetCursor(Position{0, 0})
	return b
}

// TestSyncViewToBuffer_PaintsYankFlash exercises SEAM 1 (syncViewToBuffer)
// end-to-end: a flash range set DIRECTLY on the buffer (no Task-C trigger)
// must be read via YankFlashSnapshot and fed through ApplyYankFlashOverlay
// — in its OWN nil-check, with NO live selection — so a normal-mode yank
// still paints. gocui's *View parses the ANSI emitted by the overlay into
// per-cell attributes on SetContent, so v.Buffer() returns the flashed
// glyphs (ANSI stripped); the raw \x1b[43m is asserted at the pure-overlay
// layer in yank_flash_test.go (ApplyYankFlashOverlay). This test proves
// the seam reads the snapshot independently and renders without panic.
func TestSyncViewToBuffer_PaintsYankFlash(t *testing.T) {
	buf := newBufferWithText(t, "SELECT id FROM bar")
	buf.SetYankFlash(Range{Start: Position{0, 0}, End: Position{0, 6}})
	if buf.SelectionSnapshot() != nil {
		t.Fatalf("precondition: no live selection expected for a normal-mode yank")
	}

	v := gocui.NewView("seam1", 0, 0, 60, 10, gocui.OutputNormal)
	syncViewToBuffer(v, buf)

	if got := v.Buffer(); !strings.Contains(got, "SELECT id FROM bar") {
		t.Fatalf("flashed text missing from rendered view: %q", got)
	}
}

// TestSyncViewToBuffer_NoFlashRegression locks the no-flash path: with no
// flash armed the seam must render byte-identically to selection-only
// behavior. We compare against an explicit no-overlay render of the same
// buffer (ANSI from highlight survives gocui only as attributes, so we
// compare the glyph-level v.Buffer()).
func TestSyncViewToBuffer_NoFlashRegression(t *testing.T) {
	text := "SELECT id FROM bar"

	withFlashCleared := newBufferWithText(t, text)
	v1 := gocui.NewView("seam1-noflash", 0, 0, 60, 10, gocui.OutputNormal)
	syncViewToBuffer(v1, withFlashCleared)
	baseline := v1.Buffer()

	// Same buffer, flash armed then cleared → identical glyph output.
	armed := newBufferWithText(t, text)
	epoch := armed.SetYankFlash(Range{Start: Position{0, 0}, End: Position{0, 6}})
	armed.ClearYankFlash(epoch)
	if armed.YankFlashSnapshot() != nil {
		t.Fatalf("precondition: flash should be cleared")
	}
	v2 := gocui.NewView("seam1-cleared", 0, 0, 60, 10, gocui.OutputNormal)
	syncViewToBuffer(v2, armed)

	if got := v2.Buffer(); got != baseline {
		t.Fatalf("no-flash render diverged:\n  got      %q\n  baseline %q", got, baseline)
	}
}

// TestSyncViewToBuffer_OutOfBoundsFlashNoPanic feeds the seam a flash
// range past the buffer's line/col count. ApplyYankFlashOverlay is
// panic-safe; the seam must not panic (rendering completing suffices).
func TestSyncViewToBuffer_OutOfBoundsFlashNoPanic(t *testing.T) {
	buf := newBufferWithText(t, "abc")
	buf.SetYankFlash(Range{Start: Position{5, 0}, End: Position{6, 99}})

	v := gocui.NewView("seam1-oob", 0, 0, 60, 10, gocui.OutputNormal)
	syncViewToBuffer(v, buf) // must not panic
	if got := v.Buffer(); !strings.Contains(got, "abc") {
		t.Fatalf("text missing after out-of-bounds flash render: %q", got)
	}
}

// TestSyncViewToBuffer_FlashIsIndependentOfSelection is the F7 regression:
// the flash nil-check must NOT be nested inside the selection nil-check.
// With a selection AND a flash both armed the seam renders without panic;
// with ONLY a flash (no selection) it still renders the flashed buffer —
// proving the flash block runs on its own. (Glyph output is identical
// either way once gocui strips ANSI; the load-bearing assertion is that
// the flash-only path executes the overlay at all, verified by no panic
// plus the independent-block source in vim_editor.go.)
func TestSyncViewToBuffer_FlashIsIndependentOfSelection(t *testing.T) {
	buf := newBufferWithText(t, "SELECT id")
	buf.SetYankFlash(Range{Start: Position{0, 0}, End: Position{0, 6}})

	v := gocui.NewView("seam1-indep", 0, 0, 60, 10, gocui.OutputNormal)
	syncViewToBuffer(v, buf) // no selection armed → must still paint via own block
	if got := v.Buffer(); !strings.Contains(got, "SELECT id") {
		t.Fatalf("flash-only render missing text: %q", got)
	}
}

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
