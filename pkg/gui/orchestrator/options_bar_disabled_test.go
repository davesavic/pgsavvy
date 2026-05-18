package orchestrator

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// buildDisabledOptionsBarTrieSet wires a single ShowInBar leaf whose
// Command carries the supplied disabled descriptor. The helper mirrors
// buildOptionsBarTrieSet but allows the caller to inject GetDisabled /
// DisabledReasonStatic directly on the resolved *commands.Command, so
// the trie leaf's Action pointer reflects the disabled shape.
func buildDisabledOptionsBarTrieSet(t *testing.T, cmd *commands.Command, seq string, mode types.Mode, scope types.ContextKey) *keys.TrieSet {
	t.Helper()
	kseq, err := keys.SequenceFromShorthand(seq)
	if err != nil {
		t.Fatalf("SequenceFromShorthand(%q): %v", seq, err)
	}
	bld := keys.NewTrieBuilder()
	bld.InsertDefault(&keys.ChordBinding{
		Sequence:    kseq,
		Mode:        mode,
		Scope:       scope,
		ActionID:    cmd.ID,
		Description: cmd.Description,
		Tag:         cmd.Tag,
		ShowInBar:   true,
		Origin:      "options_bar_disabled_test",
	}, cmd)
	trie, _ := bld.Build()
	out := keys.NewTrieSet()
	out.Set(mode, scope, trie)
	return out
}

// TestCollectOptionsForScope_DisabledSuffix proves the AC: a ShowInBar
// leaf bound to a disabled Command renders with the "(disabled)"
// suffix appended to the "description: key" segment.
func TestCollectOptionsForScope_DisabledSuffix(t *testing.T) {
	cmd := &commands.Command{
		ID:                   "demo.bad",
		Description:          "Cancel",
		Tag:                  "Query",
		Handler:              func(commands.ExecCtx) error { return nil },
		DisabledReasonStatic: "driver lacks live cancel",
	}
	ts := buildDisabledOptionsBarTrieSet(t, cmd, "c", types.ModeNormal, types.QUERY_EDITOR)

	got := CollectOptionsForScope(ts, types.ModeNormal, types.QUERY_EDITOR, nil)
	if len(got) != 1 {
		t.Fatalf("got %d entries (%v), want 1", len(got), got)
	}
	want := "Cancel: c (disabled)"
	if got[0] != want {
		t.Errorf("got %q, want %q", got[0], want)
	}
	if !strings.Contains(got[0], "(disabled)") {
		t.Errorf("got %q missing (disabled) suffix", got[0])
	}
}

// TestCollectOptionsForScope_EnabledNoSuffix is the regression guard:
// a leaf bound to an enabled Command renders without the suffix (so
// the suffix is not added accidentally to every entry).
func TestCollectOptionsForScope_EnabledNoSuffix(t *testing.T) {
	cmd := &commands.Command{
		ID:          "demo.ok",
		Description: "Run",
		Tag:         "Query",
		Handler:     func(commands.ExecCtx) error { return nil },
	}
	ts := buildDisabledOptionsBarTrieSet(t, cmd, "r", types.ModeNormal, types.QUERY_EDITOR)

	got := CollectOptionsForScope(ts, types.ModeNormal, types.QUERY_EDITOR, nil)
	if len(got) != 1 {
		t.Fatalf("got %d entries (%v), want 1", len(got), got)
	}
	want := "Run: r"
	if got[0] != want {
		t.Errorf("got %q, want %q (enabled binding must not carry a (disabled) suffix)", got[0], want)
	}
	if strings.Contains(got[0], "(disabled)") {
		t.Errorf("enabled segment %q carries an unexpected (disabled) marker", got[0])
	}
}

// TestCollectOptionsForScope_DisabledHonorsDynamicPredicate confirms
// GetDisabled is consulted at frame-build time (the options bar uses
// the zero ExecCtx, so the predicate must tolerate that ctx).
func TestCollectOptionsForScope_DisabledHonorsDynamicPredicate(t *testing.T) {
	cmd := &commands.Command{
		ID:          "demo.dyn",
		Description: "Refresh",
		Tag:         "Table",
		Handler:     func(commands.ExecCtx) error { return nil },
		GetDisabled: func(commands.ExecCtx) (string, bool) {
			return "not now", true
		},
	}
	ts := buildDisabledOptionsBarTrieSet(t, cmd, "x", types.ModeNormal, types.QUERY_EDITOR)

	got := CollectOptionsForScope(ts, types.ModeNormal, types.QUERY_EDITOR, nil)
	if len(got) != 1 || !strings.HasSuffix(got[0], "(disabled)") {
		t.Errorf("got %v, want one entry ending in (disabled)", got)
	}
}
