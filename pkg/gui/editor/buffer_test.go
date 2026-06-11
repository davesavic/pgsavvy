package editor

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func bufFromLines(lines ...string) *Buffer {
	b := &Buffer{}
	if len(lines) == 0 {
		return b
	}
	b.Lines = make([]Line, len(lines))
	for i, s := range lines {
		b.Lines[i] = Line{Runes: []rune(s)}
	}
	return b
}

func TestBufferStringEmpty(t *testing.T) {
	b := &Buffer{}
	if got := b.String(); got != "" {
		t.Fatalf("empty Buffer.String() = %q, want \"\"", got)
	}
}

func TestBufferStringJoinsLines(t *testing.T) {
	b := bufFromLines("SELECT 1", "FROM dual")
	if got, want := b.String(), "SELECT 1\nFROM dual"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestBufferStringTrailingEmptyLine(t *testing.T) {
	b := bufFromLines("SELECT 1", "")
	if got, want := b.String(), "SELECT 1\n"; got != want {
		t.Fatalf("String() = %q, want %q (trailing empty line yields trailing newline)", got, want)
	}
}

func TestBufferEmptyVsOneEmptyLineDistinct(t *testing.T) {
	empty := &Buffer{}
	oneEmpty := bufFromLines("")
	// String() collapses both to "" — but Lines counts differ.
	if len(empty.Lines) == len(oneEmpty.Lines) {
		t.Fatalf("expected distinct Lines lengths; got both = %d", len(empty.Lines))
	}
}

func TestBufferApplyInsertSingleLine(t *testing.T) {
	b := bufFromLines("SELECT 1")
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 8}, End: Position{0, 8}},
		Text:  "0",
	})
	if err != nil {
		t.Fatalf("Apply Insert returned %v, want nil", err)
	}
	if got, want := b.String(), "SELECT 10"; got != want {
		t.Fatalf("post-insert String() = %q, want %q", got, want)
	}
	if !b.Dirty {
		t.Fatalf("Dirty should be true after a successful Apply")
	}
}

func TestBufferApplyInsertMultiLine(t *testing.T) {
	b := bufFromLines("ab")
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 1}, End: Position{0, 1}},
		Text:  "X\nY\nZ",
	})
	if err != nil {
		t.Fatalf("Apply Insert returned %v, want nil", err)
	}
	if got, want := b.String(), "aX\nY\nZb"; got != want {
		t.Fatalf("post-multiline-insert String() = %q, want %q", got, want)
	}
}

func TestBufferApplyDeleteSingleLine(t *testing.T) {
	b := bufFromLines("hello world")
	err := b.Apply(Edit{
		Kind:  EditKindDelete,
		Range: Range{Start: Position{0, 5}, End: Position{0, 11}},
	})
	if err != nil {
		t.Fatalf("Apply Delete returned %v, want nil", err)
	}
	if got, want := b.String(), "hello"; got != want {
		t.Fatalf("post-delete String() = %q, want %q", got, want)
	}
}

func TestBufferApplyDeleteMultiLine(t *testing.T) {
	b := bufFromLines("hello", "cruel", "world")
	err := b.Apply(Edit{
		Kind:  EditKindDelete,
		Range: Range{Start: Position{0, 5}, End: Position{2, 0}},
	})
	if err != nil {
		t.Fatalf("Apply Delete returned %v, want nil", err)
	}
	if got, want := b.String(), "helloworld"; got != want {
		t.Fatalf("post-multi-delete String() = %q, want %q", got, want)
	}
}

func TestBufferApplyReplaceAcrossLines(t *testing.T) {
	b := bufFromLines("hello", "world")
	err := b.Apply(Edit{
		Kind:  EditKindReplace,
		Range: Range{Start: Position{0, 5}, End: Position{1, 0}},
		Text:  ", ",
	})
	if err != nil {
		t.Fatalf("Apply Replace returned %v, want nil", err)
	}
	if got, want := b.String(), "hello, world"; got != want {
		t.Fatalf("post-replace String() = %q, want %q", got, want)
	}
}

func TestBufferApplyOutOfRangeIsAtomic(t *testing.T) {
	b := bufFromLines("hello")
	beforeDirty := b.Dirty
	beforeLines := append([]Line{}, b.Lines...)

	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{5, 0}, End: Position{5, 0}},
		Text:  "X",
	})
	if !errors.Is(err, ErrEditOutOfRange) {
		t.Fatalf("Apply with OOR Position got err=%v, want ErrEditOutOfRange", err)
	}
	if !reflect.DeepEqual(b.Lines, beforeLines) {
		t.Fatalf("Lines mutated after OOR Apply: got %v, want %v", b.Lines, beforeLines)
	}
	if b.Dirty != beforeDirty {
		t.Fatalf("Dirty mutated after OOR Apply: got %v, want %v", b.Dirty, beforeDirty)
	}
	if b.History != nil && b.History.NodeCount() != 0 {
		t.Fatalf("History mutated after OOR Apply: NodeCount = %d, want 0", b.History.NodeCount())
	}
}

// Spec AC: Replace where the End is out-of-range AFTER the embedded
// Delete is detected pre-apply by atomic dual-endpoint validation
// and leaves Lines, History, Dirty all unchanged.
func TestBufferApplyReplaceEndOORLeavesEverythingUnchanged(t *testing.T) {
	b := bufFromLines("abc", "def")
	// Pre-record one Apply so History is populated and we can verify
	// it is unchanged after the OOR Replace.
	if err := b.Apply(Edit{Kind: EditKindInsert, Range: Range{Start: Position{0, 1}, End: Position{0, 1}}, Text: "X"}); err != nil {
		t.Fatalf("seed Apply returned %v", err)
	}
	beforeLines := deepCopyLines(b.Lines)
	beforeDirty := b.Dirty
	beforeCount := b.History.NodeCount()

	err := b.Apply(Edit{
		Kind:  EditKindReplace,
		Range: Range{Start: Position{0, 1}, End: Position{5, 0}}, // End OOR
		Text:  "Q",
	})
	if !errors.Is(err, ErrEditOutOfRange) {
		t.Fatalf("got err=%v, want ErrEditOutOfRange", err)
	}
	if !reflect.DeepEqual(b.Lines, beforeLines) {
		t.Fatalf("Lines changed after OOR Replace")
	}
	if b.Dirty != beforeDirty {
		t.Fatalf("Dirty changed after OOR Replace: got %v, want %v", b.Dirty, beforeDirty)
	}
	if b.History.NodeCount() != beforeCount {
		t.Fatalf("History grew after OOR Replace: %d → %d", beforeCount, b.History.NodeCount())
	}
}

func TestBufferApplyReplaceIsSingleUndoStep(t *testing.T) {
	b := bufFromLines("hello", "world")
	err := b.Apply(Edit{
		Kind:  EditKindReplace,
		Range: Range{Start: Position{0, 5}, End: Position{1, 0}},
		Text:  ", ",
	})
	if err != nil {
		t.Fatalf("Apply Replace returned %v", err)
	}
	if b.History.NodeCount() != 1 {
		t.Fatalf("expected single undo node for Replace, got %d", b.History.NodeCount())
	}
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo returned %v", err)
	}
	if got, want := b.String(), "hello\nworld"; got != want {
		t.Fatalf("post-undo String() = %q, want %q", got, want)
	}
}

func TestBufferUndoEmptyHistoryIsNoOp(t *testing.T) {
	b := &Buffer{}
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo on empty Buffer returned %v, want nil", err)
	}
}

func TestBufferApplyUndoApplyUndoUndo(t *testing.T) {
	b := bufFromLines("a")
	mustApply(t, b, Edit{Kind: EditKindInsert, Range: Range{Start: Position{0, 1}, End: Position{0, 1}}, Text: "B"})
	if got, want := b.String(), "aB"; got != want {
		t.Fatalf("after A: %q, want %q", got, want)
	}
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got, want := b.String(), "a"; got != want {
		t.Fatalf("after undo: %q, want %q", got, want)
	}
	mustApply(t, b, Edit{Kind: EditKindInsert, Range: Range{Start: Position{0, 1}, End: Position{0, 1}}, Text: "C"})
	if got, want := b.String(), "aC"; got != want {
		t.Fatalf("after C: %q, want %q", got, want)
	}
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got, want := b.String(), "a"; got != want {
		t.Fatalf("after undo2: %q, want %q", got, want)
	}
	// Second undo walks past the original "a" state to the empty root.
	// Buffer.Undo no-ops at the root.
	if err := b.Undo(); err != nil {
		t.Fatalf("Undo at root: %v", err)
	}
	if got, want := b.String(), "a"; got != want {
		t.Fatalf("after no-op undo: %q, want %q", got, want)
	}
}

func TestBufferTextInRangeSingleLine(t *testing.T) {
	b := bufFromLines("hello world")
	got := b.TextInRange(Range{Start: Position{0, 6}, End: Position{0, 11}})
	if got != "world" {
		t.Fatalf("TextInRange = %q, want %q", got, "world")
	}
}

func TestBufferTextInRangeMultiLine(t *testing.T) {
	b := bufFromLines("hello", "cruel", "world")
	got := b.TextInRange(Range{Start: Position{0, 3}, End: Position{2, 2}})
	if got != "lo\ncruel\nwo" {
		t.Fatalf("TextInRange = %q", got)
	}
}

func TestBufferTextInRangeLineWise(t *testing.T) {
	b := bufFromLines("hello", "cruel", "world")
	got := b.TextInRange(Range{Start: Position{0, 0}, End: Position{1, 0}, LineWise: true})
	if got != "hello\ncruel" {
		t.Fatalf("line-wise TextInRange = %q", got)
	}
}

func TestBufferTextInRangeBlockWise(t *testing.T) {
	b := bufFromLines("abcdefg", "ABCDEFG", "1234567")
	// Half-open columns [2,5) over all three rows -> the rectangle cde/CDE/345.
	got := b.TextInRange(Range{Start: Position{0, 2}, End: Position{2, 5}, BlockWise: true})
	if got != "cde\nCDE\n345" {
		t.Fatalf("block-wise TextInRange = %q, want %q", got, "cde\nCDE\n345")
	}
}

func TestBufferTextInRangeBlockWiseRaggedRow(t *testing.T) {
	b := bufFromLines("abcdef", "AB", "123456")
	// Middle row "AB" is shorter than maxCol=5; it must contribute an empty
	// slice rather than panic.
	got := b.TextInRange(Range{Start: Position{0, 2}, End: Position{2, 5}, BlockWise: true})
	if got != "cde\n\n345" {
		t.Fatalf("ragged block TextInRange = %q, want %q", got, "cde\n\n345")
	}
}

func TestBufferTextInRangeBlockWiseColumnsMinMaxed(t *testing.T) {
	b := bufFromLines("abcdefg", "ABCDEFG")
	// Start top-right (0,5), End bottom-left (1,2): lex order keeps Start
	// first, so the rectangle is only correct if columns are min/maxed
	// independently of line order.
	got := b.TextInRange(Range{Start: Position{0, 5}, End: Position{1, 2}, BlockWise: true})
	if got != "cde\nCDE" {
		t.Fatalf("min/maxed block TextInRange = %q, want %q", got, "cde\nCDE")
	}
}

func TestBufferCursorByteOffset(t *testing.T) {
	b := bufFromLines("héllo", "world")
	b.Cursor = Position{Line: 0, Col: 2}
	// "hé" = 1 byte + 2 bytes = 3 bytes
	if got := b.CursorByteOffset(); got != 3 {
		t.Fatalf("offset at h+é = %d, want 3", got)
	}
	b.Cursor = Position{Line: 1, Col: 0}
	// "héllo" = 1+2+1+1+1 = 6 bytes, + 1 newline = 7
	if got := b.CursorByteOffset(); got != 7 {
		t.Fatalf("offset at start of line 2 = %d, want 7", got)
	}
}

func TestBufferLinesCopyIsDeep(t *testing.T) {
	b := bufFromLines("hello", "world")
	cp := b.LinesCopy()
	cp[0].Runes[0] = 'X'
	if string(b.Lines[0].Runes) != "hello" {
		t.Fatalf("LinesCopy aliased Runes: parent now %q", string(b.Lines[0].Runes))
	}
}

func TestBufferLinesCopyEmpty(t *testing.T) {
	b := &Buffer{}
	if cp := b.LinesCopy(); cp != nil {
		t.Fatalf("LinesCopy on empty Buffer = %v, want nil", cp)
	}
}

// Stress test: concurrent readers + writers under -race. Buffer's
// internal RWMutex must serialise Apply against String/LinesCopy/
// TextInRange.
func TestBufferConcurrentAccessRace(t *testing.T) {
	b := bufFromLines("seed")
	var wg sync.WaitGroup
	stop := make(chan struct{})

	writer := func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = b.Apply(Edit{
				Kind:  EditKindInsert,
				Range: Range{Start: Position{0, 0}, End: Position{0, 0}},
				Text:  "x",
			})
			_ = b.Undo()
		}
	}
	reader := func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = b.String()
			_ = b.LinesCopy()
			_ = b.TextInRange(Range{Start: Position{0, 0}, End: Position{0, 0}})
		}
	}

	for range 3 {
		wg.Add(2)
		go writer()
		go reader()
	}

	// Brief stress window — long enough that -race actually
	// interleaves the goroutines but short enough to keep the suite
	// fast.
	for range 200 {
		_ = b.String()
	}
	close(stop)
	wg.Wait()
}

func TestBufferExitVisualClearsSelection(t *testing.T) {
	b := bufFromLines("hello")
	sel := Range{Start: Position{0, 0}, End: Position{0, 3}}
	b.Selection = &sel
	ExitVisual(b)
	if b.Selection != nil {
		t.Fatalf("ExitVisual did not clear Selection")
	}
}

func TestExitVisualNilSafe(t *testing.T) {
	ExitVisual(nil) // must not panic
}

func TestBufferApplyOnEmptyBuffer(t *testing.T) {
	b := &Buffer{}
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 0}, End: Position{0, 0}},
		Text:  "first",
	})
	if err != nil {
		t.Fatalf("Apply on empty Buffer returned %v", err)
	}
	if b.String() != "first" {
		t.Fatalf("post-insert String() = %q, want %q", b.String(), "first")
	}
}

func TestBufferApplyMultilineInsertOnEmpty(t *testing.T) {
	b := &Buffer{}
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 0}, End: Position{0, 0}},
		Text:  "a\nb\nc",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := b.String(), "a\nb\nc"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if len(b.Lines) != 3 {
		t.Fatalf("Lines len = %d, want 3", len(b.Lines))
	}
}

func TestBufferApplyInsertAtEndOfLine(t *testing.T) {
	b := bufFromLines("abc")
	err := b.Apply(Edit{
		Kind:  EditKindInsert,
		Range: Range{Start: Position{0, 3}, End: Position{0, 3}},
		Text:  "DEF",
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := b.String(); got != "abcDEF" {
		t.Fatalf("%q", got)
	}
}

func deepCopyLines(in []Line) []Line {
	out := make([]Line, len(in))
	for i, l := range in {
		cp := make([]rune, len(l.Runes))
		copy(cp, l.Runes)
		out[i] = Line{Runes: cp}
	}
	return out
}

func mustApply(t *testing.T, b *Buffer, e Edit) {
	t.Helper()
	if err := b.Apply(e); err != nil {
		t.Fatalf("Apply(%+v) = %v", e, err)
	}
}

func TestSplitTextOnNewline(t *testing.T) {
	tests := []struct {
		in   string
		want int // expected number of chunks
	}{
		{"abc", 1},
		{"a\nb", 2},
		{"a\nb\n", 3}, // trailing newline yields a final empty chunk
		{"", 1},
	}
	for _, tc := range tests {
		if got := splitTextOnNewline(tc.in); len(got) != tc.want {
			t.Errorf("splitTextOnNewline(%q): len=%d, want %d", tc.in, len(got), tc.want)
		}
	}
}

func TestStringEndsWithNewlineWhenLastLineEmpty(t *testing.T) {
	b := bufFromLines("a", "")
	if !strings.HasSuffix(b.String(), "\n") {
		t.Fatalf("expected trailing newline; got %q", b.String())
	}
}
