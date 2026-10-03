package keys

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

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
		in   string
		want Key
	}{
		{"<esc>", Key{Special: KeyEsc}},
		{"<cr>", Key{Special: KeyEnter}},
		{"<tab>", Key{Special: KeyTab}},
		{"<bs>", Key{Special: KeyBs}},
		{"<space>", Key{Code: ' '}},
		{"<up>", Key{Special: KeyUp}},
		{"<down>", Key{Special: KeyDown}},
		{"<f1>", Key{Special: KeyF1}},
		{"<f12>", Key{Special: KeyF12}},
	}
	for _, c := range cases {
		got, err := SequenceFromShorthand(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %#v, want %+v", c.in, got, c.want)
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

func TestReview_space(t *testing.T) {
	for _, tc := range []struct {
		input string
		mod   Modifier
		gmod  gocui.Modifier
		round Modifier
	}{
		{"<space>", 0, 0, 0},
		{"<c-space>", ModCtrl, gocui.ModCtrl, ModCtrl},
		{"<a-space>", ModAlt, gocui.ModAlt, ModAlt},
		{"<s-space>", ModShift, gocui.ModShift, ModShift},
		{"<m-space>", ModMeta, gocui.ModAlt, ModAlt},
		{"<c-a-s-space>", ModCtrl | ModAlt | ModShift, gocui.ModCtrl | gocui.ModAlt | gocui.ModShift, ModCtrl | ModAlt | ModShift},
	} {
		t.Run(tc.input, func(t *testing.T) {
			seq, err := SequenceFromShorthand(tc.input)
			require.NoError(t, err)
			require.Equal(t, []Key{{Code: ' ', Mod: tc.mod}}, seq)
			gk, gmod, err := ChordKeyToGocui(seq[0])
			require.NoError(t, err)
			require.Equal(t, gocui.NewKeyRune(' '), gk)
			require.Equal(t, tc.gmod, gmod)
			// Construct the runtime key independently, including the existing
			// Meta-to-Alt mapping, instead of feeding the parser's key back.
			runtime := gocui.NewKeyStrMod(" ", tc.gmod)
			require.Equal(t, Key{Code: ' ', Mod: tc.round}, KeyFromGocui(runtime))
			legacyKey, legacyMod, err := ChordKeyToGocui(Key{Special: KeySpace, Mod: tc.mod})
			require.NoError(t, err)
			require.Equal(t, gk, legacyKey)
			require.Equal(t, gmod, legacyMod)
		})
	}
	for _, input := range []string{"<leader>Q", "<localleader>Q", "<c-leader>Q"} {
		seq, err := SequenceFromShorthand(input)
		require.NoError(t, err)
		before := append([]Key(nil), seq...)
		expanded := expandLeaderTokens(seq, ' ', ' ')
		wantMod := Modifier(0)
		if input == "<c-leader>Q" {
			wantMod = ModCtrl
		}
		require.Equal(t, []Key{{Code: ' ', Mod: wantMod}, {Code: 'Q'}}, expanded)
		require.Equal(t, before, seq)
	}
	for _, special := range []SpecialKey{KeyLeader, KeyLocalLeader, ^SpecialKey(0)} {
		_, _, err := ChordKeyToGocui(Key{Special: special})
		require.Error(t, err)
	}
	_, err := SpecialKeyToGocui(KeySpace)
	require.Error(t, err, "Space has no named gocui key; use the full encoder")
}
