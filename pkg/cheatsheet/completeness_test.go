package cheatsheet

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// futureEmptyModes are the modes intentionally left unbound in this
// epic; future epics may add bindings to them but TODAY any binding in
// these modes is a bug (per AC D13 — "future modes empty" invariant).
var futureEmptyModes = []types.Mode{
	types.ModeVisual,
	types.ModeVisualLine,
	types.ModeVisualBlock,
	types.ModeOperatorPending,
	types.ModeReplace,
}

// scopesFromTrieSet returns every distinct ContextKey present in
// trieSet, plus GLOBAL. The "current scope" partition test runs Generate
// once per scope to confirm every Walk leaf surfaces in either
// CurrentScope (when scope matches) or Global (when scope is GLOBAL).
// We can't hard-code the list because `scope: all` bindings fan out
// across every non-popup context (LIMIT, LOG, WHICH_KEY, …) — driving
// the probe-list from the live trie keeps the test correct as the fan
// set evolves.
func scopesFromTrieSet(trieSet *keys.TrieSet) []types.ContextKey {
	seen := map[types.ContextKey]struct{}{types.GLOBAL: {}}
	trieSet.Walk(func(k keys.TrieSetKey, _ *keys.ChordTrie) {
		seen[k.Scope] = struct{}{}
	})
	out := make([]types.ContextKey, 0, len(seen))
	for sc := range seen {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// rowKey is the canonical equality key used to compare Walk-leaves and
// Generate-rows. (Mode, Scope, key-sequence-string).
type rowKey struct {
	Mode  types.Mode
	Scope types.ContextKey
	Key   string
}

func (r rowKey) String() string {
	return fmt.Sprintf("(mode=%s, scope=%s, seq=%q)", r.Mode, r.Scope, r.Key)
}

// buildProductionTrieSet assembles the production keybinding pipeline:
//
//   - real ContextTree + AttachControllers (HelperBag is mostly nil; the
//     null-picker fallbacks inside AttachControllers cover the picker
//     fields, and helpers like Toast/Confirm/Refresh are never invoked
//     by static GetKeybindings calls);
//   - bundle.RegisterActions on a fresh commands.Registry;
//   - the four extra commands the orchestrator registers directly
//     (HelpCheatsheet + the 3 COMMAND_LINE commands) so Build does not
//     skip those bindings as orphans;
//   - the default ContextKindLookup mirrors gui.go: it inspects every
//     context in the tree and reports its GetKind, falling back to
//     GLOBAL_CONTEXT for unknown keys.
//
// This is the SAME path the live wireWithDriver uses (orchestrator/gui.go
// lines ~210–365); the test must exercise it without mocking.
func buildProductionTrieSet(t *testing.T) (*keys.TrieSet, *commands.Registry, []keys.Warning) {
	t.Helper()

	tree := context.NewContextTree(types.ContextTreeDeps{})
	bundle := controllers.AttachControllers(tree, nil, controllers.HelperBag{})

	reg := commands.NewRegistry()
	bundle.RegisterActions(reg)

	// Orchestrator-side direct registrations (gui.go ~306, ~344–346).
	_ = reg.Register(&commands.Command{
		ID:          commands.HelpCheatsheet,
		Description: "Show cheatsheet",
		Tag:         "Help",
		Handler:     commands.NopSentinel,
	})
	_ = reg.Register(keys.CommandOpenCommand(keys.CommandLineCommandDeps{}))
	_ = reg.Register(keys.CommandCancelCommand(keys.CommandLineCommandDeps{}))
	_ = reg.Register(keys.CommandSubmitCommand(keys.CommandLineCommandDeps{}))

	kindOf := func(k types.ContextKey) types.ContextKind {
		for _, ctx := range tree.Flatten() {
			if ctx != nil && ctx.GetKey() == k {
				return ctx.GetKind()
			}
		}
		return types.GLOBAL_CONTEXT
	}

	defaults := controllers.AllDefaultBindings(bundle)
	svc := keys.NewKeybindingService()
	trieSet, warnings, err := svc.Build(defaults, &config.UserConfig{}, reg, kindOf)
	if err != nil {
		t.Fatalf("Build: unexpected error: %v", err)
	}
	if trieSet == nil {
		t.Fatalf("Build: returned nil TrieSet")
	}
	return trieSet, reg, warnings
}

// collectWalkLeaves enumerates every leaf in trieSet keyed by
// (Mode, Scope, key-label). Every leaf — including `<nop>` — is
// included because rowFromLeaf in generator.go emits a Row for any
// non-nil Action, and NopCommand has a non-nil Action pointer.
//
// The key string is built with the SAME leader-aware label helper
// Generate uses (keyLabel), so the (Walk ⇔ Generate) set-equality
// invariant holds across the dbsavvy-tro.9 reverse-mapping change —
// post-expanded runes ` `/`,` become `<leader>`/`<localleader>` on
// both sides.
func collectWalkLeaves(trieSet *keys.TrieSet) map[rowKey]struct{} {
	out := map[rowKey]struct{}{}
	leader := trieSet.Leader
	if leader == 0 {
		leader = ' '
	}
	localLeader := trieSet.LocalLeader
	if localLeader == 0 {
		localLeader = ','
	}
	trieSet.Walk(func(k keys.TrieSetKey, trie *keys.ChordTrie) {
		trie.Walk(func(seq []keys.Key, _ keys.LookupResult) {
			out[rowKey{Mode: k.Mode, Scope: k.Scope, Key: keyLabel(seq, leader, localLeader)}] = struct{}{}
		})
	})
	return out
}

// collectGenerateRows runs Generate once per probe scope and unions the
// resulting Rows into a single (Mode, Scope, key-sequence-string) set.
//
// Generate partitions output as CurrentScope (Scope == in.Scope) and
// Global (Scope == GLOBAL). To recover the per-(Mode, Scope) set without
// double-counting, we attribute every CurrentScope row to in.Scope and
// every Global row to types.GLOBAL — but ONLY when in.Scope == GLOBAL,
// otherwise non-GLOBAL probe scopes would re-emit the same Global rows
// (which would still be correct as set semantics but wastes work).
func collectGenerateRows(trieSet *keys.TrieSet, probeScopes []types.ContextKey) map[rowKey]struct{} {
	out := map[rowKey]struct{}{}
	for _, sc := range probeScopes {
		genOut := Generate(GenerateInput{Trie: trieSet, Scope: sc, Tr: nil})
		for _, mv := range genOut.CurrentScope {
			for _, sect := range mv.Sections {
				for _, row := range sect.Rows {
					out[rowKey{Mode: mv.Mode, Scope: sc, Key: row.Key}] = struct{}{}
				}
			}
		}
		// Global rows are independent of in.Scope; attribute them to GLOBAL.
		for _, mv := range genOut.Global {
			for _, sect := range mv.Sections {
				for _, row := range sect.Rows {
					out[rowKey{Mode: mv.Mode, Scope: types.GLOBAL, Key: row.Key}] = struct{}{}
				}
			}
		}
	}
	return out
}

// symmetricDiff returns the two-sided diff between a and b: items in a
// missing from b, and items in b missing from a. Returned slices are
// sorted for stable diagnostic output.
func symmetricDiff(a, b map[rowKey]struct{}) (missingFromB, missingFromA []rowKey) {
	for k := range a {
		if _, ok := b[k]; !ok {
			missingFromB = append(missingFromB, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			missingFromA = append(missingFromA, k)
		}
	}
	sortKeys := func(xs []rowKey) {
		sort.Slice(xs, func(i, j int) bool {
			if xs[i].Mode != xs[j].Mode {
				return xs[i].Mode < xs[j].Mode
			}
			if xs[i].Scope != xs[j].Scope {
				return xs[i].Scope < xs[j].Scope
			}
			return xs[i].Key < xs[j].Key
		})
	}
	sortKeys(missingFromB)
	sortKeys(missingFromA)
	return
}

// futureModesEmptyInTrie returns a non-empty error string if any leaf
// exists in trieSet under a mode listed in futureEmptyModes. The error
// names every offending (Mode, Scope, key-sequence) so the assertion is
// actionable.
func futureModesEmptyInTrie(trieSet *keys.TrieSet) error {
	var offenders []string
	forbidden := map[types.Mode]struct{}{}
	for _, m := range futureEmptyModes {
		forbidden[m] = struct{}{}
	}
	trieSet.Walk(func(k keys.TrieSetKey, trie *keys.ChordTrie) {
		if _, bad := forbidden[k.Mode]; !bad {
			return
		}
		trie.Walk(func(seq []keys.Key, _ keys.LookupResult) {
			offenders = append(offenders, fmt.Sprintf("%s/%s/%q", modeLabel(k.Mode), k.Scope, keys.SequenceString(seq)))
		})
	})
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf("future-empty modes carry bindings: %s", strings.Join(offenders, ", "))
}

// futureModesEmptyInGenerate returns a non-empty error string if any
// Generate ModeView (across CurrentScope or Global, across every probe
// scope) is keyed by a mode in futureEmptyModes.
func futureModesEmptyInGenerate(trieSet *keys.TrieSet, probeScopes []types.ContextKey) error {
	forbidden := map[types.Mode]struct{}{}
	for _, m := range futureEmptyModes {
		forbidden[m] = struct{}{}
	}
	var offenders []string
	for _, sc := range probeScopes {
		out := Generate(GenerateInput{Trie: trieSet, Scope: sc})
		for _, mv := range out.CurrentScope {
			if _, bad := forbidden[mv.Mode]; bad {
				offenders = append(offenders, fmt.Sprintf("CurrentScope(probe=%s) %s", sc, modeLabel(mv.Mode)))
			}
		}
		for _, mv := range out.Global {
			if _, bad := forbidden[mv.Mode]; bad {
				offenders = append(offenders, fmt.Sprintf("Global(probe=%s) %s", sc, modeLabel(mv.Mode)))
			}
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf("future-empty modes appear in Generate output: %s", strings.Join(offenders, ", "))
}

// TestCheatsheetCompleteness asserts the (Walk ⇔ Generate) invariant
// for every populated (Mode, Scope) pair in the production trie, and
// asserts the "future modes empty" invariant for the modes this epic
// leaves unbound. See AC dlp.11 for the full contract.
func TestCheatsheetCompleteness(t *testing.T) {
	t.Parallel()
	start := time.Now()
	defer func() {
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Logf("WARN: test wall-clock %s exceeds 500ms budget", d)
		}
	}()

	trieSet, reg, _ := buildProductionTrieSet(t)
	scopesToProbe := scopesFromTrieSet(trieSet)

	// (1) Every Walk leaf has a Generate row, and vice versa.
	walkSet := collectWalkLeaves(trieSet)
	if len(walkSet) == 0 {
		t.Fatalf("production trie has zero leaves — refusing to assert vacuous truth")
	}
	genSet := collectGenerateRows(trieSet, scopesToProbe)
	missingFromGen, missingFromWalk := symmetricDiff(walkSet, genSet)
	if len(missingFromGen) > 0 || len(missingFromWalk) > 0 {
		var sb strings.Builder
		if len(missingFromGen) > 0 {
			sb.WriteString("\n  in Walk but missing from Generate:")
			for _, k := range missingFromGen {
				sb.WriteString("\n    " + k.String())
			}
		}
		if len(missingFromWalk) > 0 {
			sb.WriteString("\n  in Generate but missing from Walk:")
			for _, k := range missingFromWalk {
				sb.WriteString("\n    " + k.String())
			}
		}
		t.Fatalf("Walk-set ↔ Generate-set mismatch:%s", sb.String())
	}

	// (2) Every leaf's resolved Action.ID is in the production Registry,
	//     EXCEPT the <nop> sentinel (which is not Registry-registered)
	//     and CustomCmd "command:<shell>" stubs (also not Registry-registered).
	//     The production Build is what actually wires these; we just verify
	//     the post-Build trie is consistent with the registry.
	var unknownActions []string
	trieSet.Walk(func(_ keys.TrieSetKey, trie *keys.ChordTrie) {
		trie.Walk(func(seq []keys.Key, leaf keys.LookupResult) {
			if leaf.Action == nil {
				unknownActions = append(unknownActions, fmt.Sprintf("nil-Action leaf at %q", keys.SequenceString(seq)))
				return
			}
			if leaf.Action.ID == "<nop>" {
				return
			}
			if leaf.Source == types.CustomCmd {
				return
			}
			if _, ok := reg.Get(leaf.Action.ID); !ok {
				unknownActions = append(unknownActions, fmt.Sprintf("%q at %q", leaf.Action.ID, keys.SequenceString(seq)))
			}
		})
	})
	if len(unknownActions) > 0 {
		sort.Strings(unknownActions)
		t.Fatalf("trie contains leaves whose Action.ID is not in the Registry:\n  %s", strings.Join(unknownActions, "\n  "))
	}

	// (3) Future modes empty — both in the trie AND in Generate output.
	if err := futureModesEmptyInTrie(trieSet); err != nil {
		t.Fatalf("future-modes-empty invariant violated in TrieSet: %v", err)
	}
	if err := futureModesEmptyInGenerate(trieSet, scopesToProbe); err != nil {
		t.Fatalf("future-modes-empty invariant violated in Generate: %v", err)
	}

	// --- Negative-fixture sub-tests verify the assertion paths above
	// actually fire when the invariants are broken.

	t.Run("OrphanActionSkipped", func(t *testing.T) {
		// A binding referencing an unknown ActionID must be DROPPED by
		// Build with an orphan_action warning, leaving the resulting trie
		// empty for that key. Walk-set and Generate-set are both empty →
		// equality still holds.
		reg := commands.NewRegistry()
		seq, err := keys.SequenceFromShorthand("zz")
		if err != nil {
			t.Fatalf("SequenceFromShorthand: %v", err)
		}
		bindings := []*keys.ChordBinding{{
			Sequence: seq,
			Mode:     types.ModeNormal,
			Scope:    types.TABLES,
			ActionID: "does.not.exist",
			Origin:   "completeness_test.go",
		}}
		svc := keys.NewKeybindingService()
		trieSet, warnings, err := svc.Build(bindings, &config.UserConfig{}, reg, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		var orphanSeen bool
		for _, w := range warnings {
			if w.Code == "orphan_action" {
				orphanSeen = true
				break
			}
		}
		if !orphanSeen {
			t.Fatalf("expected an orphan_action warning, got %+v", warnings)
		}
		walkSet := collectWalkLeaves(trieSet)
		genSet := collectGenerateRows(trieSet, []types.ContextKey{types.TABLES, types.GLOBAL})
		if len(walkSet) != 0 {
			t.Fatalf("orphan binding should not appear in trie, got %d leaves", len(walkSet))
		}
		if len(genSet) != 0 {
			t.Fatalf("orphan binding should not appear in Generate, got %d rows", len(genSet))
		}
		miss1, miss2 := symmetricDiff(walkSet, genSet)
		if len(miss1) != 0 || len(miss2) != 0 {
			t.Fatalf("completeness diff non-empty for empty inputs: %v / %v", miss1, miss2)
		}
	})

	t.Run("AssertionFiresOnGenerateGap", func(t *testing.T) {
		// Take the production trie, run Generate, then drop one row from
		// the result before comparing. The completeness diff helper MUST
		// surface the dropped row as missing-from-Generate.
		trieSet, _, _ := buildProductionTrieSet(t)
		probe := scopesFromTrieSet(trieSet)
		walkSet := collectWalkLeaves(trieSet)
		genSet := collectGenerateRows(trieSet, probe)

		// Pick a victim from walkSet — any one will do, but choose
		// deterministically by sorting the keys.
		victims := make([]rowKey, 0, len(walkSet))
		for k := range walkSet {
			victims = append(victims, k)
		}
		sort.Slice(victims, func(i, j int) bool {
			if victims[i].Mode != victims[j].Mode {
				return victims[i].Mode < victims[j].Mode
			}
			if victims[i].Scope != victims[j].Scope {
				return victims[i].Scope < victims[j].Scope
			}
			return victims[i].Key < victims[j].Key
		})
		if len(victims) == 0 {
			t.Fatalf("no leaves to drop")
		}
		victim := victims[0]

		// Doctor genSet by removing the victim.
		delete(genSet, victim)
		missingFromGen, missingFromWalk := symmetricDiff(walkSet, genSet)
		if len(missingFromGen) != 1 || missingFromGen[0] != victim {
			t.Fatalf("expected exactly the victim %s in missing-from-Generate, got %v", victim, missingFromGen)
		}
		if len(missingFromWalk) != 0 {
			t.Fatalf("expected no missing-from-Walk entries, got %v", missingFromWalk)
		}
	})

	t.Run("FutureModeAccidentallyBound", func(t *testing.T) {
		// Synthesise a single ChordBinding in ModeVisual; the
		// future-modes-empty helper MUST flag it.
		reg := commands.NewRegistry()
		_ = reg.Register(&commands.Command{
			ID:      "test.vis",
			Handler: commands.NopSentinel,
		})
		seq, err := keys.SequenceFromShorthand("v")
		if err != nil {
			t.Fatalf("SequenceFromShorthand: %v", err)
		}
		bindings := []*keys.ChordBinding{{
			Sequence: seq,
			Mode:     types.ModeVisual,
			Scope:    types.TABLES,
			ActionID: "test.vis",
			Origin:   "completeness_test.go",
		}}
		svc := keys.NewKeybindingService()
		trieSet, _, err := svc.Build(bindings, &config.UserConfig{}, reg, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		err = futureModesEmptyInTrie(trieSet)
		if err == nil {
			t.Fatalf("expected an error from futureModesEmptyInTrie")
		}
		if !strings.Contains(err.Error(), "Visual") {
			t.Fatalf("expected error to mention Visual, got: %v", err)
		}
		// And the Generate-side helper must also catch it.
		err = futureModesEmptyInGenerate(trieSet, []types.ContextKey{types.TABLES, types.GLOBAL})
		if err == nil {
			t.Fatalf("expected an error from futureModesEmptyInGenerate")
		}
		if !strings.Contains(err.Error(), "Visual") {
			t.Fatalf("expected Generate-side error to mention Visual, got: %v", err)
		}
	})
}
