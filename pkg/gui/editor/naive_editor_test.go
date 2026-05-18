package editor

import (
	"reflect"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// newViewForTest constructs a freestanding *gocui.View with a usable
// TextArea. NewView populates TextArea = &TextArea{} so the view is
// ready for TypeCharacter / GetContent calls without a running Gui.
func newViewForTest() *gocui.View {
	return gocui.NewView("test", 0, 0, 10, 10, gocui.OutputNormal)
}

// TestNaiveEditorMultiLineInsert verifies that NaiveEditor.Edit
// delegates to DefaultEditor: typing characters and pressing Enter
// produces a multi-line buffer with the expected content.
func TestNaiveEditorMultiLineInsert(t *testing.T) {
	v := newViewForTest()
	ed := New()
	// Type "ab", Enter, "cd". gocui.KeyEnter is the SimpleEditor's
	// newline trigger.
	for _, r := range []rune{'a', 'b'} {
		ed.Edit(v, gocui.NewKeyRune(r))
	}
	ed.Edit(v, gocui.NewKeyName(gocui.KeyEnter))
	for _, r := range []rune{'c', 'd'} {
		ed.Edit(v, gocui.NewKeyRune(r))
	}
	got := Buffer(v)
	if got != "ab\ncd" {
		t.Fatalf("Buffer after multi-line insert = %q, want %q", got, "ab\ncd")
	}
}

func TestNaiveEditorNilViewIsNoOp(t *testing.T) {
	ed := New()
	if ed.Edit(nil, gocui.NewKeyRune('x')) {
		t.Fatalf("Edit(nil, 'x') = true, want false")
	}
}

func TestBufferAndLinesOnNilView(t *testing.T) {
	if got := Buffer(nil); got != "" {
		t.Fatalf("Buffer(nil) = %q, want \"\"", got)
	}
	if got := Lines(nil); got != nil {
		t.Fatalf("Lines(nil) = %#v, want nil", got)
	}
	if got := Cursor(nil); got != (Position{}) {
		t.Fatalf("Cursor(nil) = %+v, want zero", got)
	}
}

func TestSelectionAlwaysFalseInV1(t *testing.T) {
	_, _, ok := Selection(nil)
	if ok {
		t.Fatal("Selection ok = true, want false (no visual mode in v1)")
	}
	v := newViewForTest()
	_, _, ok = Selection(v)
	if ok {
		t.Fatal("Selection on live view ok = true, want false (no visual mode in v1)")
	}
}

func TestLinesSplitsContent(t *testing.T) {
	v := newViewForTest()
	v.TextArea.TypeCharacter("S")
	v.TextArea.TypeCharacter("\n")
	v.TextArea.TypeCharacter("E")
	got := Lines(v)
	want := []string{"S", "E"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines = %#v, want %#v", got, want)
	}
}
