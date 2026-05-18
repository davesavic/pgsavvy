package status

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

func TestLabelForMode_NormalEditable(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := LabelForMode(types.ModeNormal, tr, true)
	if got != tr.ModeNormal {
		t.Fatalf("LabelForMode(Normal, forceShow=true) = %q, want %q", got, tr.ModeNormal)
	}
}

func TestLabelForMode_NormalNonEditable(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := LabelForMode(types.ModeNormal, tr, false)
	if got != "" {
		t.Fatalf("LabelForMode(Normal, forceShow=false) = %q, want empty", got)
	}
}

func TestLabelForMode_NonNormalIgnoresForceShow(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	cases := []struct {
		mode types.Mode
		want string
	}{
		{types.ModeInsert, tr.ModeInsert},
		{types.ModeVisual, tr.ModeVisual},
		{types.ModeVisualLine, tr.ModeVisualLine},
		{types.ModeVisualBlock, tr.ModeVisualBlock},
		{types.ModeOperatorPending, tr.ModeOperatorPending},
		{types.ModeCommand, tr.ModeCommand},
		{types.ModeReplace, tr.ModeReplace},
	}
	for _, c := range cases {
		if got := LabelForMode(c.mode, tr, false); got != c.want {
			t.Fatalf("LabelForMode(%s, forceShow=false) = %q, want %q", c.mode, got, c.want)
		}
		if got := LabelForMode(c.mode, tr, true); got != c.want {
			t.Fatalf("LabelForMode(%s, forceShow=true) = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestLabelForMode_NilTranslationSet(t *testing.T) {
	if got := LabelForMode(types.ModeInsert, nil, false); got != "" {
		t.Fatalf("LabelForMode(nil tr) = %q, want empty", got)
	}
}
