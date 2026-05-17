package config

import "testing"

func TestSafeText_Empty(t *testing.T) {
	if got := SafeText(""); got != "" {
		t.Errorf("SafeText(\"\") = %q, want \"\"", got)
	}
}

func TestSafeText_PreservesMultibyte(t *testing.T) {
	const in = "héllo"
	if got := SafeText(in); got != in {
		t.Errorf("SafeText(%q) = %q, want %q", in, got, in)
	}
}

func TestSafeText_PreservesTab(t *testing.T) {
	const in = "a\tb"
	if got := SafeText(in); got != in {
		t.Errorf("SafeText(%q) = %q, want %q", in, got, in)
	}
}

// TestSafeText_StripsEscOnly documents the minimal-loss policy: only the
// unsafe bytes (\x1b, \x07) are removed; the printable trailing bytes of
// the OSC sequence (]0;, pwned, world) survive. See safe_text.go.
func TestSafeText_StripsEscOnly(t *testing.T) {
	in := "hi\x1b]0;pwned\x07world"
	want := "hi]0;pwnedworld"
	if got := SafeText(in); got != want {
		t.Errorf("SafeText(%q) = %q, want %q", in, got, want)
	}
}

func TestSafeText_StripsEscButKeepsCSITail(t *testing.T) {
	in := "evil\x1b[2J"
	want := "evil[2J"
	if got := SafeText(in); got != want {
		t.Errorf("SafeText(%q) = %q, want %q", in, got, want)
	}
}

func TestSafeText_StripsDel(t *testing.T) {
	in := "a\x7fb"
	want := "ab"
	if got := SafeText(in); got != want {
		t.Errorf("SafeText(%q) = %q, want %q", in, got, want)
	}
}

func TestUserConfig_SanitizeAppliedToBindings(t *testing.T) {
	cfg := &UserConfig{
		Keybindings: []KeybindingConfig{
			{
				Description: "title\x1b]0;hacked\x07",
				Tag:         "grp\x07ok",
				Key:         "<leader>q\x1b",
			},
		},
	}
	cfg.Sanitize()
	if cfg.Keybindings[0].Description != "title]0;hacked" {
		t.Errorf("Description = %q, want %q", cfg.Keybindings[0].Description, "title]0;hacked")
	}
	if cfg.Keybindings[0].Tag != "grpok" {
		t.Errorf("Tag = %q, want %q", cfg.Keybindings[0].Tag, "grpok")
	}
	if cfg.Keybindings[0].Key != "<leader>q" {
		t.Errorf("Key = %q, want %q", cfg.Keybindings[0].Key, "<leader>q")
	}
}

func TestUserConfig_Sanitize_NilSafe(t *testing.T) {
	var cfg *UserConfig
	cfg.Sanitize() // must not panic
}
