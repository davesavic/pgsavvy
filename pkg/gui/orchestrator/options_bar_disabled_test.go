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
	want := "[c] Cancel (disabled)"
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
	want := "[r] Run"
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

// buildMultiBindingTrieSet wires the same Command under multiple
// (mode, scope) pairs so a single predicate can observe what the probe
// actually passes in. Each registered (mode, scope) gets its own trie
// containing one ShowInBar leaf at the supplied sequence.
func buildMultiBindingTrieSet(t *testing.T, cmd *commands.Command, seq string, pairs []struct {
	Mode  types.Mode
	Scope types.ContextKey
},
) *keys.TrieSet {
	t.Helper()
	kseq, err := keys.SequenceFromShorthand(seq)
	if err != nil {
		t.Fatalf("SequenceFromShorthand(%q): %v", seq, err)
	}
	out := keys.NewTrieSet()
	for _, p := range pairs {
		bld := keys.NewTrieBuilder()
		bld.InsertDefault(&keys.ChordBinding{
			Sequence:    kseq,
			Mode:        p.Mode,
			Scope:       p.Scope,
			ActionID:    cmd.ID,
			Description: cmd.Description,
			Tag:         cmd.Tag,
			ShowInBar:   true,
			Origin:      "options_bar_disabled_test",
		}, cmd)
		trie, _ := bld.Build()
		out.Set(p.Mode, p.Scope, trie)
	}
	return out
}

// TestCollectOptionsForScope_DisabledPredicateReceivesModeAndScope
// proves Mode and Scope propagate through the options-bar probe into
// the dynamic GetDisabled predicate. A single Command is bound under
// multiple (mode, scope) pairs; the predicate returns disabled iff the
// observed ExecCtx matches the visual-mode + query-editor combination.
// Probing the same scope in ModeNormal must yield an enabled segment,
// and probing a different scope in ModeVisual must also yield enabled —
// only the exact (ModeVisual, QUERY_EDITOR) combination is disabled.
func TestCollectOptionsForScope_DisabledPredicateReceivesModeAndScope(t *testing.T) {
	cmd := &commands.Command{
		ID:          "demo.probe",
		Description: "Refresh",
		Tag:         "Table",
		Handler:     func(commands.ExecCtx) error { return nil },
		GetDisabled: func(ctx commands.ExecCtx) (string, bool) {
			if ctx.Mode == types.ModeVisual && ctx.Scope == types.QUERY_EDITOR {
				return "visual-only", true
			}
			return "", false
		},
	}
	ts := buildMultiBindingTrieSet(t, cmd, "x", []struct {
		Mode  types.Mode
		Scope types.ContextKey
	}{
		{types.ModeVisual, types.QUERY_EDITOR},
		{types.ModeNormal, types.QUERY_EDITOR},
		{types.ModeVisual, types.TABLES},
	})

	// Probe 1: ModeVisual + QUERY_EDITOR — predicate must observe both
	// and flag the segment disabled.
	gotVisual := CollectOptionsForScope(ts, types.ModeVisual, types.QUERY_EDITOR, nil)
	if len(gotVisual) != 1 {
		t.Fatalf("ModeVisual/QUERY_EDITOR: got %d entries (%v), want 1", len(gotVisual), gotVisual)
	}
	if !strings.HasSuffix(gotVisual[0], "(disabled)") {
		t.Errorf("ModeVisual/QUERY_EDITOR: got %q, want segment ending in (disabled) — predicate did not see Mode=Visual+Scope=QUERY_EDITOR", gotVisual[0])
	}

	// Probe 2: ModeNormal + QUERY_EDITOR — same scope, different mode.
	// Predicate must observe ModeNormal and leave the segment enabled.
	gotNormal := CollectOptionsForScope(ts, types.ModeNormal, types.QUERY_EDITOR, nil)
	if len(gotNormal) != 1 {
		t.Fatalf("ModeNormal/QUERY_EDITOR: got %d entries (%v), want 1", len(gotNormal), gotNormal)
	}
	if strings.Contains(gotNormal[0], "(disabled)") {
		t.Errorf("ModeNormal/QUERY_EDITOR: got %q, want segment WITHOUT (disabled) — predicate did not see Mode=Normal", gotNormal[0])
	}

	// Probe 3: ModeVisual + TABLES — same mode, different scope.
	// Predicate must observe Scope=TABLES and leave the segment enabled.
	gotOtherScope := CollectOptionsForScope(ts, types.ModeVisual, types.TABLES, nil)
	if len(gotOtherScope) != 1 {
		t.Fatalf("ModeVisual/TABLES: got %d entries (%v), want 1", len(gotOtherScope), gotOtherScope)
	}
	if strings.Contains(gotOtherScope[0], "(disabled)") {
		t.Errorf("ModeVisual/TABLES: got %q, want segment WITHOUT (disabled) — predicate did not see Scope=TABLES", gotOtherScope[0])
	}
}
