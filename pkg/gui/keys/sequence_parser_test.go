package keys

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestSequenceFromShorthand_LeaderTr(t *testing.T) {
	got, err := SequenceFromShorthand("<leader>tr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %#v", len(got), got)
	}
	if got[0].Special != KeyLeader {
		t.Errorf("token 0 Special = %v, want KeyLeader", got[0].Special)
	}
	if got[1].Code != 't' || got[1].Special != KeyNone {
		t.Errorf("token 1 = %#v, want {Code:'t'}", got[1])
	}
	if got[2].Code != 'r' || got[2].Special != KeyNone {
		t.Errorf("token 2 = %#v, want {Code:'r'}", got[2])
	}
}

func TestSequenceFromShorthand_LocalLeader(t *testing.T) {
	got, err := SequenceFromShorthand("<localleader>f")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Special != KeyLocalLeader {
		t.Errorf("got %#v, want [<localleader> f]", got)
	}
}

func TestSequenceFromShorthand_CtrlW_V(t *testing.T) {
	got, err := SequenceFromShorthand("<c-w>v")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Code != 'w' || got[0].Mod != ModCtrl {
		t.Errorf("token 0 = %#v, want ctrl+w", got[0])
	}
	if got[1].Code != 'v' || got[1].Mod != 0 {
		t.Errorf("token 1 = %#v, want v", got[1])
	}
}

func TestSequenceFromShorthand_Specials(t *testing.T) {
	cases := []struct {
		in      string
		special SpecialKey
	}{
		{"<esc>", KeyEsc},
		{"<cr>", KeyEnter},
		{"<tab>", KeyTab},
		{"<bs>", KeyBs},
		{"<space>", KeySpace},
		{"<up>", KeyUp},
		{"<down>", KeyDown},
		{"<f1>", KeyF1},
		{"<f12>", KeyF12},
	}
	for _, c := range cases {
		got, err := SequenceFromShorthand(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if len(got) != 1 || got[0].Special != c.special {
			t.Errorf("%s: got %#v, want Special=%v", c.in, got, c.special)
		}
	}
}

func TestSequenceFromShorthand_GG(t *testing.T) {
	got, err := SequenceFromShorthand("gg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Code != 'g' || got[1].Code != 'g' {
		t.Errorf("got %#v, want [g g]", got)
	}
}

func TestSequenceFromShorthand_Errors(t *testing.T) {
	cases := []string{"", "<bogus>", "<leader", "<>"}
	for _, c := range cases {
		if _, err := SequenceFromShorthand(c); err == nil {
			t.Errorf("%q: expected error, got nil", c)
		}
	}
}

func TestExpandLeaderTokens_ReplacesLeader(t *testing.T) {
	seq := []Key{{Special: KeyLeader}, {Code: 't'}, {Code: 'r'}}
	got := expandLeaderTokens(seq, ' ', ',')
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Special != KeyNone || got[0].Code != ' ' {
		t.Errorf("token 0 = %#v, want {Code:' '}", got[0])
	}
	// Original unchanged.
	if seq[0].Special != KeyLeader {
		t.Error("expandLeaderTokens mutated input slice")
	}
}

func TestExpandLeaderTokens_ReplacesLocalLeader(t *testing.T) {
	seq := []Key{{Special: KeyLocalLeader}, {Code: 'f'}}
	got := expandLeaderTokens(seq, ' ', ',')
	if got[0].Special != KeyNone || got[0].Code != ',' {
		t.Errorf("token 0 = %#v, want {Code:','}", got[0])
	}
}

func TestExpandLeaderTokens_NoOpForBareKeys(t *testing.T) {
	seq := []Key{{Code: 'g'}, {Code: 'g'}}
	got := expandLeaderTokens(seq, ' ', ',')
	if got[0] != seq[0] || got[1] != seq[1] {
		t.Errorf("expansion changed bare keys: %#v vs %#v", got, seq)
	}
}

func TestModeBitsFromTokens(t *testing.T) {
	cases := []struct {
		tokens []string
		want   []types.Mode
	}{
		{[]string{"n"}, []types.Mode{types.ModeNormal}},
		{[]string{"i"}, []types.Mode{types.ModeInsert}},
		{[]string{"v"}, []types.Mode{types.ModeVisual}},
		{[]string{"x"}, []types.Mode{types.ModeVisual}}, // alias
		{[]string{"V"}, []types.Mode{types.ModeVisualLine}},
		{[]string{"<c-v>"}, []types.Mode{types.ModeVisualBlock}},
		{[]string{"o"}, []types.Mode{types.ModeOperatorPending}},
		{[]string{"c"}, []types.Mode{types.ModeCommand}},
		{[]string{"n", "v"}, []types.Mode{types.ModeNormal, types.ModeVisual}},
	}
	for _, c := range cases {
		got, err := modeBitsFromTokens(c.tokens)
		if err != nil {
			t.Errorf("%v: unexpected error %v", c.tokens, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%v: len = %d, want %d", c.tokens, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v[%d]: got %v, want %v", c.tokens, i, got[i], c.want[i])
			}
		}
	}
}

func TestModeBitsFromTokens_Errors(t *testing.T) {
	if _, err := modeBitsFromTokens(nil); err == nil {
		t.Error("nil tokens: expected error")
	}
	if _, err := modeBitsFromTokens([]string{"bogus"}); err == nil {
		t.Error("unknown token: expected error")
	}
}
