package editor_test

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
)

// bufFrom constructs an editor.Buffer with newline-split text. Used by
// the operator tests as a compact factory; matches the convention in
// other editor_test files.
func bufFrom(text string) *editor.Buffer {
	b := editor.NewBuffer()
	if text == "" {
		b.Lines = []editor.Line{{Runes: []rune("")}}
		return b
	}
	for line := range strings.SplitSeq(text, "\n") {
		b.Lines = append(b.Lines, editor.Line{Runes: []rune(line)})
	}
	return b
}

func TestShiftWidthIsTwo(t *testing.T) {
	if editor.ShiftWidth != 2 {
		t.Errorf("ShiftWidth = %d, want 2", editor.ShiftWidth)
	}
}

func TestDeleteCharRangeReturnsCutAndApplies(t *testing.T) {
	b := bufFrom("hello world")
	r := editor.Range{
		Start: editor.Position{Line: 0, Col: 0},
		End:   editor.Position{Line: 0, Col: 6},
	}
	cut, err := editor.Delete(b, r)
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if cut != "hello " {
		t.Errorf("cut = %q, want %q", cut, "hello ")
	}
	if got := string(b.Lines[0].Runes); got != "world" {
		t.Errorf("buffer = %q, want %q", got, "world")
	}
}

func TestDeleteLineWiseAppendsTrailingNewlineToCut(t *testing.T) {
	b := bufFrom("aaa\nbbb\nccc")
	r := editor.Range{
		Start:    editor.Position{Line: 1, Col: 0},
		End:      editor.Position{Line: 1, Col: 3},
		LineWise: true,
	}
	cut, err := editor.Delete(b, r)
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if cut != "bbb\n" {
		t.Errorf("cut = %q, want %q", cut, "bbb\n")
	}
	if len(b.Lines) != 2 {
		t.Fatalf("Lines after linewise delete = %d, want 2", len(b.Lines))
	}
}

func TestYankDoesNotMutate(t *testing.T) {
	b := bufFrom("hello world")
	r := editor.Range{
		Start: editor.Position{Line: 0, Col: 0},
		End:   editor.Position{Line: 0, Col: 5},
	}
	yanked := editor.Yank(b, r)
	if yanked != "hello" {
		t.Errorf("yanked = %q, want hello", yanked)
	}
	if got := string(b.Lines[0].Runes); got != "hello world" {
		t.Errorf("buffer mutated to %q (yank must not mutate)", got)
	}
}

func TestYankLineWiseAppendsNewline(t *testing.T) {
	b := bufFrom("first\nsecond")
	r := editor.Range{
		Start:    editor.Position{Line: 0, Col: 0},
		End:      editor.Position{Line: 0, Col: 5},
		LineWise: true,
	}
	got := editor.Yank(b, r)
	if got != "first\n" {
		t.Errorf("yank = %q, want %q", got, "first\n")
	}
}

func TestUpperReplacesRuneCase(t *testing.T) {
	b := bufFrom("hello")
	r := editor.Range{
		Start: editor.Position{Line: 0, Col: 0},
		End:   editor.Position{Line: 0, Col: 5},
	}
	if err := editor.Upper(b, r); err != nil {
		t.Fatalf("Upper err = %v", err)
	}
	if got := string(b.Lines[0].Runes); got != "HELLO" {
		t.Errorf("buffer = %q, want HELLO", got)
	}
}

func TestLowerReplacesRuneCase(t *testing.T) {
	b := bufFrom("HELLO")
	r := editor.Range{
		Start: editor.Position{Line: 0, Col: 0},
		End:   editor.Position{Line: 0, Col: 5},
	}
	if err := editor.Lower(b, r); err != nil {
		t.Fatalf("Lower err = %v", err)
	}
	if got := string(b.Lines[0].Runes); got != "hello" {
		t.Errorf("buffer = %q, want hello", got)
	}
}

func TestIndentRightInsertsShiftWidthSpaces(t *testing.T) {
	b := bufFrom("aaa\nbbb")
	if err := editor.IndentRight(b, 0, 0); err != nil {
		t.Fatalf("IndentRight err = %v", err)
	}
	if got := string(b.Lines[0].Runes); got != "  aaa" {
		t.Errorf("Line 0 = %q, want %q", got, "  aaa")
	}
	if got := string(b.Lines[1].Runes); got != "bbb" {
		t.Errorf("Line 1 unchanged = %q, want %q", got, "bbb")
	}
}

func TestIndentRightMultiLine(t *testing.T) {
	b := bufFrom("aaa\nbbb\nccc")
	if err := editor.IndentRight(b, 0, 2); err != nil {
		t.Fatalf("IndentRight err = %v", err)
	}
	wants := []string{"  aaa", "  bbb", "  ccc"}
	for i, want := range wants {
		if got := string(b.Lines[i].Runes); got != want {
			t.Errorf("Line %d = %q, want %q", i, got, want)
		}
	}
}

func TestIndentLeftStripsUpToShiftWidth(t *testing.T) {
	b := bufFrom("    aaa\n bbb\nccc")
	if err := editor.IndentLeft(b, 0, 2); err != nil {
		t.Fatalf("IndentLeft err = %v", err)
	}
	wants := []string{"  aaa", "bbb", "ccc"}
	for i, want := range wants {
		if got := string(b.Lines[i].Runes); got != want {
			t.Errorf("Line %d = %q, want %q", i, got, want)
		}
	}
}

func TestIndentLeftAtColumnZeroIsNoOp(t *testing.T) {
	b := bufFrom("aaa")
	if err := editor.IndentLeft(b, 0, 0); err != nil {
		t.Fatalf("IndentLeft err = %v", err)
	}
	if got := string(b.Lines[0].Runes); got != "aaa" {
		t.Errorf("Line = %q, want aaa (no-op)", got)
	}
}

func TestNormaliseRangeSwapsReversed(t *testing.T) {
	r := editor.Range{
		Start: editor.Position{Line: 2, Col: 5},
		End:   editor.Position{Line: 0, Col: 0},
	}
	n := editor.NormaliseRange(r)
	if n.Start != (editor.Position{Line: 0, Col: 0}) {
		t.Errorf("Start = %+v, want {0,0}", n.Start)
	}
	if n.End != (editor.Position{Line: 2, Col: 5}) {
		t.Errorf("End = %+v, want {2,5}", n.End)
	}
}

func TestCurrentLineLineWiseSingleLine(t *testing.T) {
	b := bufFrom("aaa\nbbb")
	r := editor.CurrentLineLineWise(b, editor.Position{Line: 0, Col: 1}, 1)
	if !r.LineWise {
		t.Errorf("LineWise = false, want true")
	}
	if r.Start != (editor.Position{Line: 0, Col: 0}) {
		t.Errorf("Start = %+v, want {0,0}", r.Start)
	}
	if r.End.Line != 0 {
		t.Errorf("End.Line = %d, want 0", r.End.Line)
	}
}

func TestCurrentLineLineWiseWithCount(t *testing.T) {
	b := bufFrom("aaa\nbbb\nccc\nddd")
	r := editor.CurrentLineLineWise(b, editor.Position{Line: 1, Col: 0}, 2)
	if r.Start.Line != 1 || r.End.Line != 2 {
		t.Errorf("range = [%d, %d], want [1, 2]", r.Start.Line, r.End.Line)
	}
}
