package keys

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// motionMask mirrors the controller's motionModeMask (operator-pending +
// every visual variant). Reconstructed locally so the keys-package test
// stays free of a pkg/gui/controllers import (AD2).
const motionMask = types.ModeOperatorPending |
	types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock

// motionDefaults returns the shipped-default bindings for one motion key
// (Normal + motionMask) under QUERY_EDITOR, matching how the vim
// controller publishes motions.
func motionDefaults(key rune, actionID string) []*ChordBinding {
	return []*ChordBinding{
		{Sequence: []Key{{Code: key}}, Mode: types.ModeNormal, Scope: types.QUERY_EDITOR, ActionID: actionID, Origin: "motion"},
		{Sequence: []Key{{Code: key}}, Mode: motionMask, Scope: types.QUERY_EDITOR, ActionID: actionID, Origin: "motion"},
	}
}

// TestBuild_MotionRemap_PropagatesAcrossShippedMask asserts a Normal-only
// user remap of a motion action lands in every operator-pending / visual
// cell the shipped default covered.
func TestBuild_MotionRemap_PropagatesAcrossShippedMask(t *testing.T) {
	reg := testRegistry(t, commands.MotionLineDown)
	svc := NewKeybindingService(types.QUERY_EDITOR)
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: string(types.QUERY_EDITOR), Key: "n", Action: commands.MotionLineDown},
	}

	ts, warns, err := svc.Build(motionDefaults('j', commands.MotionLineDown), cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, w := range warns {
		if w.Code == "reserved_motion_target" {
			t.Fatalf("unexpected rejection: %+v", w)
		}
	}

	for _, m := range append([]types.Mode{types.ModeNormal}, modeBits(motionMask)...) {
		trie, ok := ts.Get(m, types.QUERY_EDITOR)
		if !ok {
			t.Fatalf("no trie for mode %v", m)
		}
		res := trie.Lookup([]Key{{Code: 'n'}})
		if !res.IsLeaf || res.Action == nil || res.Action.ID != commands.MotionLineDown {
			t.Errorf("mode %v: Lookup(n) = %+v, want leaf motion.line_down", m, res)
		}
	}
}

// TestBuild_MotionRemap_FreesShippedDefault asserts R3: after j→n, the
// j leaf is removed from EVERY shipped-mask cell.
func TestBuild_MotionRemap_FreesShippedDefault(t *testing.T) {
	reg := testRegistry(t, commands.MotionLineDown)
	svc := NewKeybindingService(types.QUERY_EDITOR)
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: string(types.QUERY_EDITOR), Key: "n", Action: commands.MotionLineDown},
	}

	ts, _, err := svc.Build(motionDefaults('j', commands.MotionLineDown), cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, m := range append([]types.Mode{types.ModeNormal}, modeBits(motionMask)...) {
		trie, _ := ts.Get(m, types.QUERY_EDITOR)
		if trie == nil {
			continue
		}
		if res := trie.Lookup([]Key{{Code: 'j'}}); res.Found {
			t.Errorf("mode %v: j still resolves after remap: %+v", m, res)
		}
	}
}

// TestBuild_NormalOnlyAction_DoesNotLeak asserts mask fidelity: a user
// override of an action shipped Normal-only does NOT inject an
// operator-pending leaf.
func TestBuild_NormalOnlyAction_DoesNotLeak(t *testing.T) {
	reg := testRegistry(t, "editor.undo")
	svc := NewKeybindingService(types.QUERY_EDITOR)
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: string(types.QUERY_EDITOR), Key: "U", Action: "editor.undo"},
	}
	// editor.undo ships Normal-only.
	defaults := []*ChordBinding{
		{Sequence: []Key{{Code: 'u'}}, Mode: types.ModeNormal, Scope: types.QUERY_EDITOR, ActionID: "editor.undo", Origin: "hist"},
	}

	ts, _, err := svc.Build(defaults, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if trie, ok := ts.Get(types.ModeOperatorPending, types.QUERY_EDITOR); ok && trie != nil {
		if res := trie.Lookup([]Key{{Code: 'U'}}); res.Found {
			t.Error("Normal-only action leaked into operator-pending cell")
		}
	}
	// Sanity: the Normal binding itself landed.
	ntrie, _ := ts.Get(types.ModeNormal, types.QUERY_EDITOR)
	if res := ntrie.Lookup([]Key{{Code: 'U'}}); !res.IsLeaf {
		t.Error("Normal-only remap did not land in Normal cell")
	}
}

// TestBuild_MotionRemap_RejectsDigitTarget asserts R4: remapping a motion
// onto a bare digit is rejected (whole remap skipped + warning), so the
// digit remains free for count grammar and the shipped default survives.
func TestBuild_MotionRemap_RejectsDigitTarget(t *testing.T) {
	reg := testRegistry(t, commands.MotionLineDown)
	svc := NewKeybindingService(types.QUERY_EDITOR)
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: string(types.QUERY_EDITOR), Key: "5", Action: commands.MotionLineDown},
	}

	ts, warns, err := svc.Build(motionDefaults('j', commands.MotionLineDown), cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, w := range warns {
		if w.Code == "reserved_motion_target" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reserved_motion_target warning, got %v", warns)
	}
	// '5' must NOT become a leaf in any motion cell (would break count).
	for _, m := range append([]types.Mode{types.ModeNormal}, modeBits(motionMask)...) {
		trie, _ := ts.Get(m, types.QUERY_EDITOR)
		if trie == nil {
			continue
		}
		if res := trie.Lookup([]Key{{Code: '5'}}); res.Found {
			t.Errorf("mode %v: digit 5 became a binding after rejected remap", m)
		}
	}
	// Shipped default j survives (whole remap rejected).
	ntrie, _ := ts.Get(types.ModeNormal, types.QUERY_EDITOR)
	if res := ntrie.Lookup([]Key{{Code: 'j'}}); !res.IsLeaf {
		t.Error("shipped default j removed despite rejected remap")
	}
}

// TestBuild_MotionRemap_RejectsRegisterTarget asserts R4 for the `"`
// register prefix.
func TestBuild_MotionRemap_RejectsRegisterTarget(t *testing.T) {
	reg := testRegistry(t, commands.MotionLineDown)
	svc := NewKeybindingService(types.QUERY_EDITOR)
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: string(types.QUERY_EDITOR), Key: "\"", Action: commands.MotionLineDown},
	}

	ts, warns, err := svc.Build(motionDefaults('j', commands.MotionLineDown), cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, w := range warns {
		if w.Code == "reserved_motion_target" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected reserved_motion_target warning, got %v", warns)
	}
	for _, m := range append([]types.Mode{types.ModeNormal}, modeBits(motionMask)...) {
		trie, _ := ts.Get(m, types.QUERY_EDITOR)
		if trie == nil {
			continue
		}
		if res := trie.Lookup([]Key{{Code: '"'}}); res.Found {
			t.Errorf("mode %v: register prefix became a binding after rejected remap", m)
		}
	}
}

// TestTrieBuilder_RemoveLeafByAction is the focused unit test for the
// removal primitive: an ActionID-keyed leaf removal across a multi-leaf
// trie, pruning now-empty interior nodes.
func TestTrieBuilder_RemoveLeafByAction(t *testing.T) {
	cmdA := &commands.Command{ID: "a", Handler: func(commands.ExecCtx) error { return nil }}
	cmdB := &commands.Command{ID: "b", Handler: func(commands.ExecCtx) error { return nil }}

	b := NewTrieBuilder()
	// j → a (single key), dj → a (composed), dk → b.
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'j'}}, ActionID: "a"}, cmdA)
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'd'}, {Code: 'j'}}, ActionID: "a"}, cmdA)
	b.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'd'}, {Code: 'k'}}, ActionID: "b"}, cmdB)

	if !b.RemoveLeafByAction("a") {
		t.Fatal("RemoveLeafByAction(a) reported no removal")
	}
	trie, _ := b.Build()

	if res := trie.Lookup([]Key{{Code: 'j'}}); res.Found {
		t.Error("j leaf survived removal")
	}
	if res := trie.Lookup([]Key{{Code: 'd'}, {Code: 'j'}}); res.Found {
		t.Error("dj leaf survived removal")
	}
	// dk (action b) must remain — and the d interior node must survive.
	if res := trie.Lookup([]Key{{Code: 'd'}, {Code: 'k'}}); !res.IsLeaf || res.Action.ID != "b" {
		t.Errorf("dk lost after removing a: %+v", res)
	}
	// Removing a non-existent action is a no-op.
	if b2 := NewTrieBuilder(); b2.RemoveLeafByAction("nope") {
		t.Error("removal of absent action reported true")
	}
}
