package config

import (
	"reflect"
	"testing"
)

func TestParseKeyLabel_BareRune(t *testing.T) {
	got, err := ParseKeyLabel("j")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Key != "j" || len(got.Mods) != 0 {
		t.Errorf("got %#v, want {Mods:[] Key:j}", got)
	}
}

func TestParseKeyLabel_CtrlA(t *testing.T) {
	got, err := ParseKeyLabel("<c-a>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Key != "a" {
		t.Errorf("Key = %q, want a", got.Key)
	}
	if !reflect.DeepEqual(got.Mods, []string{"ctrl"}) {
		t.Errorf("Mods = %#v, want [ctrl]", got.Mods)
	}
}

func TestParseKeyLabel_CaseInsensitiveSpecial(t *testing.T) {
	a, errA := ParseKeyLabel("<leader>")
	b, errB := ParseKeyLabel("<LEADER>")
	c, errC := ParseKeyLabel("<Leader>")
	if errA != nil || errB != nil || errC != nil {
		t.Fatalf("errors: %v %v %v", errA, errB, errC)
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(b, c) {
		t.Errorf("case-variants differ: %#v %#v %#v", a, b, c)
	}
	if a.Key != "leader" {
		t.Errorf("Key = %q, want leader", a.Key)
	}
}

func TestParseKeyLabel_MultipleModsCanonicalOrder(t *testing.T) {
	got, err := ParseKeyLabel("<C-S-x>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.Mods, []string{"ctrl", "shift"}) {
		t.Errorf("Mods = %#v, want [ctrl shift]", got.Mods)
	}
	if got.Key != "x" {
		t.Errorf("Key = %q, want x", got.Key)
	}
	got2, err := ParseKeyLabel("<S-C-x>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got2.Mods, []string{"ctrl", "shift"}) {
		t.Errorf("reverse-order Mods = %#v, want [ctrl shift]", got2.Mods)
	}
}

func TestParseKeyLabel_AltAndMeta(t *testing.T) {
	got, err := ParseKeyLabel("<a-m>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Key != "m" || !reflect.DeepEqual(got.Mods, []string{"alt"}) {
		t.Errorf("got %#v", got)
	}
	got, err = ParseKeyLabel("<M-k>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Key != "k" || !reflect.DeepEqual(got.Mods, []string{"meta"}) {
		t.Errorf("got %#v", got)
	}
}

func TestParseKeyLabel_RuneCaseSensitive(t *testing.T) {
	lower, err := ParseKeyLabel("<c-a>")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := ParseKeyLabel("<c-A>")
	if err != nil {
		t.Fatal(err)
	}
	if lower.Key == upper.Key {
		t.Errorf("expected case-sensitive rune; both = %q", lower.Key)
	}
}

func TestParseKeyLabel_FKeys(t *testing.T) {
	for _, k := range []string{"<f1>", "<f12>"} {
		got, err := ParseKeyLabel(k)
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if got.Key == "" {
			t.Errorf("%s: empty key", k)
		}
	}
}

func TestParseKeyLabel_Errors(t *testing.T) {
	cases := []string{
		"",
		"<bogus",
		"<>",
		"<c->",
		"<f13>",
		"<unknown>",
		"<c-unknown>",
		"ab",
	}
	for _, c := range cases {
		if _, err := ParseKeyLabel(c); err == nil {
			t.Errorf("%q: expected error, got nil", c)
		}
	}
}

func TestParseKeyLabel_SpecialNames(t *testing.T) {
	for _, name := range []string{"<esc>", "<cr>", "<tab>", "<bs>", "<space>"} {
		if _, err := ParseKeyLabel(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
