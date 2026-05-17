package types

import (
	"strings"
	"testing"
)

func TestMode_Has_NormalSentinel(t *testing.T) {
	if !ModeNormal.Has(ModeNormal) {
		t.Errorf("ModeNormal.Has(ModeNormal) = false, want true")
	}
	if ModeInsert.Has(ModeNormal) {
		t.Errorf("ModeInsert.Has(ModeNormal) = true, want false")
	}
}

func TestMode_Has_Composition(t *testing.T) {
	m := ModeInsert
	if !m.Has(ModeInsert | ModeVisual) {
		t.Errorf("ModeInsert.Has(ModeInsert|ModeVisual) = false, want true")
	}
	if m.Has(ModeVisual | ModeCommand) {
		t.Errorf("ModeInsert.Has(ModeVisual|ModeCommand) = true, want false")
	}
	// Composite receiver matches any of its bits.
	composite := ModeVisual | ModeOperatorPending
	if !composite.Has(ModeVisual) {
		t.Errorf("composite.Has(ModeVisual) = false, want true")
	}
	if !composite.Has(ModeOperatorPending) {
		t.Errorf("composite.Has(ModeOperatorPending) = false, want true")
	}
	if composite.Has(ModeInsert) {
		t.Errorf("composite.Has(ModeInsert) = true, want false")
	}
}

func TestMode_Is(t *testing.T) {
	if !ModeInsert.Is(ModeInsert) {
		t.Errorf("ModeInsert.Is(ModeInsert) = false, want true")
	}
	if ModeInsert.Is(ModeVisual) {
		t.Errorf("ModeInsert.Is(ModeVisual) = true, want false")
	}
	if (ModeInsert | ModeVisual).Is(ModeInsert) {
		t.Errorf("composite.Is(ModeInsert) = true, want false (Is is exact match)")
	}
	if !ModeNormal.Is(ModeNormal) {
		t.Errorf("ModeNormal.Is(ModeNormal) = false, want true")
	}
}

func TestMode_String_Canonical(t *testing.T) {
	cases := []struct {
		mode Mode
		want string
	}{
		{ModeNormal, "normal"},
		{ModeInsert, "insert"},
		{ModeVisual, "visual"},
		{ModeVisualLine, "visual-line"},
		{ModeVisualBlock, "visual-block"},
		{ModeOperatorPending, "operator-pending"},
		{ModeCommand, "command"},
		{ModeReplace, "replace"},
	}
	for _, c := range cases {
		got := c.mode.String()
		if got != c.want {
			t.Errorf("Mode(%d).String() = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestMode_String_UnknownComposite(t *testing.T) {
	composite := ModeInsert | ModeVisual
	got := composite.String()
	if got == "" {
		t.Errorf("composite.String() = empty, want non-empty")
	}
	if !strings.HasPrefix(got, "mode(0x") {
		t.Errorf("composite.String() = %q, want prefix 'mode(0x'", got)
	}
	// Deterministic: same input -> same output.
	if got2 := composite.String(); got != got2 {
		t.Errorf("Mode.String() non-deterministic: %q vs %q", got, got2)
	}

	// Out-of-range bit also flows through default.
	bogus := Mode(1 << 30)
	bogusStr := bogus.String()
	if bogusStr == "" {
		t.Errorf("bogus.String() = empty, want non-empty")
	}
	if !strings.HasPrefix(bogusStr, "mode(0x") {
		t.Errorf("bogus.String() = %q, want prefix 'mode(0x'", bogusStr)
	}
}
