package keys

import "testing"

func TestKey_String_BareRune(t *testing.T) {
	k := Key{Code: 'j'}
	if got := k.String(); got != "j" {
		t.Errorf("Key{Code:'j'}.String() = %q, want %q", got, "j")
	}
}

func TestKey_String_CtrlA(t *testing.T) {
	k := Key{Code: 'a', Mod: ModCtrl}
	if got := k.String(); got != "<c-a>" {
		t.Errorf("ctrl-a String = %q, want <c-a>", got)
	}
}

func TestKey_String_Special(t *testing.T) {
	cases := []struct {
		k    Key
		want string
	}{
		{Key{Special: KeyEsc}, "<esc>"},
		{Key{Special: KeyEnter}, "<cr>"},
		{Key{Special: KeyTab}, "<tab>"},
		{Key{Special: KeyF1}, "<f1>"},
		{Key{Special: KeyF12}, "<f12>"},
		{Key{Special: KeyLeader}, "<leader>"},
		{Key{Special: KeyLocalLeader}, "<localleader>"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("Key{Special:%v}.String() = %q, want %q", c.k.Special, got, c.want)
		}
	}
}

func TestKey_IsLeaderPlaceholder(t *testing.T) {
	if !(Key{Special: KeyLeader}).IsLeaderPlaceholder() {
		t.Error("KeyLeader should be placeholder")
	}
	if !(Key{Special: KeyLocalLeader}).IsLeaderPlaceholder() {
		t.Error("KeyLocalLeader should be placeholder")
	}
	if (Key{Code: 'j'}).IsLeaderPlaceholder() {
		t.Error("bare rune should NOT be placeholder")
	}
	if (Key{Special: KeyEsc}).IsLeaderPlaceholder() {
		t.Error("KeyEsc should NOT be placeholder")
	}
}

func TestSequenceString_LeaderTr(t *testing.T) {
	seq := []Key{{Special: KeyLeader}, {Code: 't'}, {Code: 'r'}}
	got := SequenceString(seq)
	want := "<leader>tr"
	if got != want {
		t.Errorf("SequenceString = %q, want %q", got, want)
	}
}
