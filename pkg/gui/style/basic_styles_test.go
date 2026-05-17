package style

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func TestTextStyle_SettersAreChainableAndImmutable(t *testing.T) {
	base := New().SetFg("red")
	bolded := base.SetBold(true)

	if base.Fg != "red" {
		t.Fatalf("base.Fg = %q, want %q", base.Fg, "red")
	}
	if base.Bold {
		t.Fatal("base.Bold = true; setter mutated the receiver")
	}
	if bolded.Fg != "red" {
		t.Fatalf("bolded.Fg = %q, want %q (chain should carry prior fields)", bolded.Fg, "red")
	}
	if !bolded.Bold {
		t.Fatal("bolded.Bold = false; SetBold(true) did not stick")
	}
}

func TestTextStyle_SetBgUnderlineItalic(t *testing.T) {
	got := New().SetBg("#000000").SetUnderline(true).SetItalic(true)
	if got.Bg != "#000000" {
		t.Fatalf("Bg = %q, want %q", got.Bg, "#000000")
	}
	if !got.Underline {
		t.Fatal("Underline = false, want true")
	}
	if !got.Italic {
		t.Fatal("Italic = false, want true")
	}
}

func TestTextStyle_MergeStyleEmptyEmpty(t *testing.T) {
	got := New().MergeStyle(New())
	if got != (TextStyle{}) {
		t.Fatalf("MergeStyle(empty, empty) = %+v, want zero TextStyle", got)
	}
}

func TestTextStyle_MergeStyleOtherWins(t *testing.T) {
	a := New().SetFg("red").SetBg("black")
	b := New().SetFg("blue").SetBold(true)
	got := a.MergeStyle(b)
	if got.Fg != "blue" {
		t.Fatalf("Fg = %q, want %q (other wins on non-empty)", got.Fg, "blue")
	}
	if got.Bg != "black" {
		t.Fatalf("Bg = %q, want %q (other empty must preserve receiver)", got.Bg, "black")
	}
	if !got.Bold {
		t.Fatal("Bold = false, want true (booleans OR'd)")
	}
	// Receiver untouched.
	if a.Fg != "red" {
		t.Fatalf("a.Fg mutated to %q", a.Fg)
	}
}

func TestTextStyle_SprintPassthrough(t *testing.T) {
	if got := New().SetFg("red").Sprint("hi"); got != "hi" {
		t.Fatalf("Sprint(hi) = %q, want %q", got, "hi")
	}
}

func TestTextStyle_ToTypesAndFromTypesRoundTrip(t *testing.T) {
	s := New().SetFg("#ff4d4d").SetBg("black").SetBold(true)
	tt := s.ToTypes()
	want := types.TextStyle{Fg: "#ff4d4d", Bg: "black", Bold: true}
	if tt != want {
		t.Fatalf("ToTypes = %+v, want %+v", tt, want)
	}
	back := FromTypes(tt)
	if back.Fg != s.Fg || back.Bg != s.Bg || back.Bold != s.Bold {
		t.Fatalf("FromTypes round-trip dropped fields: %+v vs %+v", back, s)
	}
}
