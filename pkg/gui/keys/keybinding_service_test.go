package keys

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// testRegistry returns a Registry pre-populated with a handful of
// actions every test in this file uses.
func testRegistry(t *testing.T, ids ...string) *commands.Registry {
	t.Helper()
	r := commands.NewRegistry()
	for _, id := range ids {
		if err := r.Register(&commands.Command{
			ID:      id,
			Handler: func(commands.ExecCtx) error { return nil },
		}); err != nil {
			t.Fatalf("register %q: %v", id, err)
		}
	}
	return r
}

// minimalCfg returns a UserConfig with the default leader/localleader
// but no Keybindings. Tests construct keybindings inline.
func minimalCfg() *config.UserConfig {
	return &config.UserConfig{Leader: " ", LocalLeader: ","}
}

func staticKind(m map[types.ContextKey]types.ContextKind) ContextKindLookup {
	return func(k types.ContextKey) types.ContextKind {
		if kind, ok := m[k]; ok {
			return kind
		}
		return types.GLOBAL_CONTEXT
	}
}

func TestBuild_EmptyConfig_EmptyTrieSet(t *testing.T) {
	svc := NewKeybindingService()
	ts, warns, err := svc.Build(nil, minimalCfg(), commands.NewRegistry(), nil)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if ts == nil {
		t.Fatal("TrieSet is nil")
	}
	if ts.Len() != 0 {
		t.Errorf("empty cfg: TrieSet.Len() = %d, want 0", ts.Len())
	}
	if len(warns) != 0 {
		t.Errorf("empty cfg: warns = %v, want none", warns)
	}
}

func TestBuild_NilRegistry_Errors(t *testing.T) {
	svc := NewKeybindingService()
	_, _, err := svc.Build(nil, minimalCfg(), nil, nil)
	if err == nil {
		t.Error("nil registry: want error, got nil")
	}
}

func TestBuild_NilCfg_Errors(t *testing.T) {
	svc := NewKeybindingService()
	_, _, err := svc.Build(nil, nil, commands.NewRegistry(), nil)
	if err == nil {
		t.Error("nil cfg: want error, got nil")
	}
}

func TestBuild_DefaultsOnly_LookupResolves(t *testing.T) {
	reg := testRegistry(t, "app.quit")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{
		{
			Sequence: []Key{{Code: 'q'}},
			Mode:     types.ModeNormal,
			Scope:    types.GLOBAL,
			ActionID: "app.quit",
			Origin:   "ctrl/quit:1",
		},
	}
	ts, warns, err := svc.Build(defaults, minimalCfg(), reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v", warns)
	}

	trie, ok := ts.Get(types.ModeNormal, types.GLOBAL)
	if !ok {
		t.Fatal("no trie for (Normal, GLOBAL)")
	}
	res := trie.Lookup([]Key{{Code: 'q'}})
	if !res.IsLeaf || res.Action == nil || res.Action.ID != "app.quit" {
		t.Errorf("Lookup(q) = %+v", res)
	}
	if res.Source != ShippedDefault {
		t.Errorf("Source = %v, want ShippedDefault", res.Source)
	}
}

func TestBuild_OrphanAction_WarnsAndSkips(t *testing.T) {
	reg := testRegistry(t) // empty registry
	svc := NewKeybindingService()
	defaults := []*ChordBinding{{
		Sequence: []Key{{Code: 'q'}}, Mode: types.ModeNormal, Scope: types.GLOBAL,
		ActionID: "missing.action", Origin: "test",
	}}
	ts, warns, err := svc.Build(defaults, minimalCfg(), reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, w := range warns {
		if w.Code == "orphan_action" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected orphan_action warning, got %v", warns)
	}
	// Binding skipped → no trie entry.
	if ts.Len() != 0 {
		t.Errorf("orphan-only Build: Len = %d, want 0", ts.Len())
	}
}

func TestBuild_LeaderExpansion(t *testing.T) {
	reg := testRegistry(t, "app.quit")
	svc := NewKeybindingService()
	seq, err := SequenceFromShorthand("<leader>q")
	if err != nil {
		t.Fatal(err)
	}
	defaults := []*ChordBinding{{
		Sequence: seq, Mode: types.ModeNormal, Scope: types.GLOBAL,
		ActionID: "app.quit", Origin: "test",
	}}

	cfg := minimalCfg() // Leader = " "
	ts, warns, err := svc.Build(defaults, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, w := range warns {
		if w.Code == "unexpanded_leader" {
			t.Errorf("unexpected unexpanded_leader warning: %+v", w)
		}
	}
	trie, _ := ts.Get(types.ModeNormal, types.GLOBAL)
	// After expansion, the sequence should be [' ', 'q'].
	res := trie.Lookup([]Key{{Code: ' '}, {Code: 'q'}})
	if !res.IsLeaf {
		t.Errorf("Lookup(' q') after leader expansion: %+v", res)
	}
	// The placeholder version must NOT resolve.
	res = trie.Lookup([]Key{{Special: KeyLeader}, {Code: 'q'}})
	if res.Found {
		t.Error("KeyLeader sentinel resolved post-expansion; expected not found")
	}
}

func TestBuild_UserOverlay_FromCfg(t *testing.T) {
	reg := testRegistry(t, "app.quit", "menu.confirm")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{{
		Sequence: []Key{{Code: 'q'}}, Mode: types.ModeNormal, Scope: types.GLOBAL,
		ActionID: "app.quit", Origin: "default",
	}}
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "menu.confirm", OriginFile: "user.yml", OriginLine: 3},
	}
	ts, _, err := svc.Build(defaults, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	trie, _ := ts.Get(types.ModeNormal, types.GLOBAL)
	res := trie.Lookup([]Key{{Code: 'q'}})
	if res.Action == nil || res.Action.ID != "menu.confirm" {
		t.Errorf("Action = %v, want menu.confirm", res.Action)
	}
	if res.Source != UserOverride {
		t.Errorf("Source = %v, want UserOverride", res.Source)
	}
}

func TestBuild_NopUserOverride(t *testing.T) {
	reg := testRegistry(t, "app.quit")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{{
		Sequence: []Key{{Code: 'q'}}, Mode: types.ModeNormal, Scope: types.GLOBAL,
		ActionID: "app.quit", Origin: "default",
	}}
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "<nop>"},
	}
	ts, _, err := svc.Build(defaults, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	trie, _ := ts.Get(types.ModeNormal, types.GLOBAL)
	res := trie.Lookup([]Key{{Code: 'q'}})
	if res.Action != commands.NopCommand {
		t.Errorf("Action = %v, want NopCommand", res.Action)
	}
	if res.Source != UserOverride {
		t.Errorf("Source = %v, want UserOverride", res.Source)
	}
}

func TestBuild_NopOverlayWithoutDefault(t *testing.T) {
	// AC: "<nop> overlay on non-existent default: trie node created with
	// NopSentinel; no shipped layering."
	reg := testRegistry(t)
	svc := NewKeybindingService()
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "<nop>"},
	}
	ts, _, err := svc.Build(nil, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	trie, ok := ts.Get(types.ModeNormal, types.GLOBAL)
	if !ok {
		t.Fatal("no trie for (Normal, GLOBAL)")
	}
	res := trie.Lookup([]Key{{Code: 'q'}})
	if !res.IsLeaf || res.Action != commands.NopCommand {
		t.Errorf("<nop> w/o default: %+v, want IsLeaf=true Action=NopCommand", res)
	}
	if res.Source != UserOverride {
		t.Errorf("Source = %v, want UserOverride", res.Source)
	}
}

func TestBuild_RevertByRemovingUserBinding(t *testing.T) {
	// Per AC: "revert by removing user binding" — build twice, second
	// time without the user override. The default must reappear.
	reg := testRegistry(t, "app.quit")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{{
		Sequence: []Key{{Code: 'q'}}, Mode: types.ModeNormal, Scope: types.GLOBAL,
		ActionID: "app.quit", Origin: "default",
	}}

	cfgWithNop := minimalCfg()
	cfgWithNop.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "q", Action: "<nop>"},
	}
	ts1, _, err := svc.Build(defaults, cfgWithNop, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	trie1, _ := ts1.Get(types.ModeNormal, types.GLOBAL)
	if trie1.Lookup([]Key{{Code: 'q'}}).Action != commands.NopCommand {
		t.Error("first build: q not nop")
	}

	// Rebuild with empty user config (user removed the override).
	cfgEmpty := minimalCfg()
	ts2, _, err := svc.Build(defaults, cfgEmpty, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	trie2, _ := ts2.Get(types.ModeNormal, types.GLOBAL)
	res := trie2.Lookup([]Key{{Code: 'q'}})
	if res.Action == nil || res.Action.ID != "app.quit" {
		t.Errorf("after revert: Action = %v, want app.quit", res.Action)
	}
	if res.Source != ShippedDefault {
		t.Errorf("after revert: Source = %v, want ShippedDefault", res.Source)
	}
}

func TestBuild_ScopeAllExpansion(t *testing.T) {
	reg := testRegistry(t, "list.up")
	svc := NewKeybindingService()
	kindOf := staticKind(map[types.ContextKey]types.ContextKind{
		types.SCHEMAS: types.SIDE_CONTEXT,
		types.TABLES:  types.SIDE_CONTEXT,
		types.COLUMNS: types.SIDE_CONTEXT,
		types.MENU:    types.PERSISTENT_POPUP, // popup → excluded
	})
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "all", Key: "k", Action: "list.up"},
	}
	ts, warns, err := svc.Build(nil, cfg, reg, kindOf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, w := range warns {
		if w.Code != "" && w.Code != "orphan_action" {
			t.Logf("warn: %+v", w)
		}
	}

	for _, ctx := range []types.ContextKey{types.SCHEMAS, types.TABLES, types.COLUMNS} {
		trie, ok := ts.Get(types.ModeNormal, ctx)
		if !ok {
			t.Errorf("no trie for %s", ctx)
			continue
		}
		if !trie.Lookup([]Key{{Code: 'k'}}).IsLeaf {
			t.Errorf("Lookup(k) on %s: not a leaf", ctx)
		}
	}
	// MENU is a popup → should NOT have an entry.
	if _, ok := ts.Get(types.ModeNormal, types.MENU); ok {
		t.Error("MENU (popup) should NOT be in scope:all expansion")
	}
}

func TestBuild_ModeNV_TwoBindings(t *testing.T) {
	reg := testRegistry(t, "list.up")
	svc := NewKeybindingService()
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n,v", Scope: "global", Key: "k", Action: "list.up"},
	}
	ts, _, err := svc.Build(nil, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, m := range []types.Mode{types.ModeNormal, types.ModeVisual} {
		trie, ok := ts.Get(m, types.GLOBAL)
		if !ok {
			t.Errorf("no trie for mode %v", m)
			continue
		}
		if !trie.Lookup([]Key{{Code: 'k'}}).IsLeaf {
			t.Errorf("Lookup(k) in mode %v: not a leaf", m)
		}
	}
}

func TestBuild_CollisionWarning_LastWins(t *testing.T) {
	reg := testRegistry(t, "a.one", "a.two")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{
		{Sequence: []Key{{Code: 't'}, {Code: 'r'}}, Mode: types.ModeNormal, Scope: types.TABLES, ActionID: "a.one", Origin: "first"},
		{Sequence: []Key{{Code: 't'}, {Code: 'r'}}, Mode: types.ModeNormal, Scope: types.TABLES, ActionID: "a.two", Origin: "second"},
	}
	ts, warns, err := svc.Build(defaults, minimalCfg(), reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	foundCollision := false
	for _, w := range warns {
		if w.Code == "collision" {
			foundCollision = true
		}
	}
	if !foundCollision {
		t.Errorf("expected collision warning, got %v", warns)
	}
	trie, _ := ts.Get(types.ModeNormal, types.TABLES)
	res := trie.Lookup([]Key{{Code: 't'}, {Code: 'r'}})
	if res.Action == nil || res.Action.ID != "a.two" {
		t.Errorf("last-wins: Action = %v, want a.two", res.Action)
	}
}

func TestBuild_AmbiguousPrefix_GAndGG(t *testing.T) {
	reg := testRegistry(t, "g.alone", "g.g")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{
		{Sequence: []Key{{Code: 'g'}}, Mode: types.ModeNormal, Scope: types.GLOBAL, ActionID: "g.alone", Origin: "g.go"},
		{Sequence: []Key{{Code: 'g'}, {Code: 'g'}}, Mode: types.ModeNormal, Scope: types.GLOBAL, ActionID: "g.g", Origin: "gg.go"},
	}
	_, warns, err := svc.Build(defaults, minimalCfg(), reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	foundAmb := false
	for _, w := range warns {
		if w.Code == "ambiguous_prefix" {
			foundAmb = true
		}
	}
	if !foundAmb {
		t.Errorf("expected ambiguous_prefix warning, got %v", warns)
	}
}

func TestBuild_BadKeySequence_Warns(t *testing.T) {
	reg := testRegistry(t, "app.quit")
	svc := NewKeybindingService()
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<unterminated", Action: "app.quit"},
	}
	_, warns, err := svc.Build(nil, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	found := false
	for _, w := range warns {
		if w.Code == "parse_sequence" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected parse_sequence warning, got %v", warns)
	}
}

func TestBuild_CustomCommandSource(t *testing.T) {
	// Per epic: "only the glyph machinery (★) ships here" — the binding
	// must land in the trie with Source=CustomCmd so the cheatsheet can
	// paint the ★ glyph. Dispatch is wired in E11.
	reg := testRegistry(t)
	svc := NewKeybindingService()
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "z", Command: "echo hi", Description: "say hi"},
	}
	ts, warns, err := svc.Build(nil, cfg, reg, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, w := range warns {
		if w.Code == "orphan_action" {
			t.Errorf("unexpected orphan_action for command: shorthand: %+v", w)
		}
	}
	trie, ok := ts.Get(types.ModeNormal, types.GLOBAL)
	if !ok {
		t.Fatal("no trie for (Normal, GLOBAL)")
	}
	res := trie.Lookup([]Key{{Code: 'z'}})
	if !res.IsLeaf {
		t.Fatalf("Lookup(z): %+v", res)
	}
	if res.Source != CustomCmd {
		t.Errorf("Source = %v, want CustomCmd", res.Source)
	}
	if res.Action == nil || res.Action.ID != "command:echo hi" {
		t.Errorf("Action = %v, want ID=command:echo hi", res.Action)
	}
}

func TestBuild_Walk_VisitsBindings(t *testing.T) {
	reg := testRegistry(t, "a.one")
	svc := NewKeybindingService()
	defaults := []*ChordBinding{{
		Sequence: []Key{{Code: 'a'}}, Mode: types.ModeNormal, Scope: types.GLOBAL,
		ActionID: "a.one", Origin: "t",
	}}
	ts, _, err := svc.Build(defaults, minimalCfg(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	ts.Walk(func(key TrieSetKey, trie *ChordTrie) {
		trie.Walk(func(seq []Key, leaf LookupResult) {
			seen++
		})
	})
	if seen != 1 {
		t.Errorf("walked %d leaves, want 1", seen)
	}
}

// allContextKeyKinds is an INDEPENDENT classification of every
// types.AllContextKeys() entry, transcribed from the contextSpecs()
// declarations in pkg/gui/context/setup.go. It deliberately does NOT
// reach into pkg/gui/context (which pkg/gui/keys must not import); it is
// the oracle the completeness guard checks production against. Keep it in
// sync with setup.go if a context's kind changes.
func allContextKeyKinds() map[types.ContextKey]types.ContextKind {
	return map[types.ContextKey]types.ContextKind{
		types.SCHEMAS:            types.SIDE_CONTEXT,
		types.TABLES:             types.SIDE_CONTEXT,
		types.SCHEMA_RAIL:        types.SIDE_CONTEXT,
		types.COLUMNS:            types.STUB,
		types.INDEXES:            types.STUB,
		types.QUERY_EDITOR:       types.MAIN_CONTEXT,
		types.TABLE_DATA_EDITOR:  types.STUB,
		types.RESULT_GRID:        types.STUB,
		types.PLAN:               types.STUB,
		types.MENU:               types.TEMPORARY_POPUP,
		types.CONFIRMATION:       types.TEMPORARY_POPUP,
		types.PROMPT:             types.TEMPORARY_POPUP,
		types.SELECTION:          types.TEMPORARY_POPUP,
		types.SUGGESTIONS:        types.TEMPORARY_POPUP,
		types.COMMAND_LINE:       types.TEMPORARY_POPUP,
		types.SEARCH_LINE:        types.TEMPORARY_POPUP,
		types.HISTORY:            types.TEMPORARY_POPUP,
		types.SAVED_QUERY:        types.PERSISTENT_POPUP,
		types.WHICH_KEY:          types.DISPLAY_CONTEXT,
		types.GLOBAL:             types.GLOBAL_CONTEXT,
		types.LIMIT:              types.DISPLAY_CONTEXT,
		types.CHEATSHEET:         types.DISPLAY_CONTEXT,
		types.HIDE_OVERLAY:       types.TEMPORARY_POPUP,
		types.EXPORT_MENU:        types.TEMPORARY_POPUP,
		types.FIRST_RUN_TIP:      types.PERSISTENT_POPUP,
		types.TABLE_INSPECT:      types.TEMPORARY_POPUP,
		types.CELL_EDITOR:        types.TEMPORARY_POPUP,
		types.COMMIT_DIALOG:      types.TEMPORARY_POPUP,
		types.CONFLICT_DIALOG:    types.TEMPORARY_POPUP,
		types.FK_REVERSE_PICKER:  types.TEMPORARY_POPUP,
		types.CONNECTION_MANAGER: types.MAIN_CONTEXT,
		types.RELATIONSHIP_PANEL: types.DISPLAY_CONTEXT,
	}
}

// scopeAllExpansion builds a service over `known`, expands a single
// `scope: all` Normal-mode binding for key "k", and returns the set of
// scopes that actually received a leaf for it.
func scopeAllExpansion(t *testing.T, known []types.ContextKey, kindOf ContextKindLookup) map[types.ContextKey]struct{} {
	t.Helper()
	reg := testRegistry(t, "list.up")
	svc := NewKeybindingService(known...)
	cfg := minimalCfg()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "all", Key: "k", Action: "list.up"},
	}
	ts, _, err := svc.Build(nil, cfg, reg, kindOf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := map[types.ContextKey]struct{}{}
	ts.Walk(func(key TrieSetKey, trie *ChordTrie) {
		if key.Mode != types.ModeNormal {
			return
		}
		if trie.Lookup([]Key{{Code: 'k'}}).IsLeaf {
			got[key.Scope] = struct{}{}
		}
	})
	return got
}

// TestAllKnownContextsCompleteness is the binding guard: scope:all must
// reach EXACTLY the non-popup contexts (kindOf ∈ nonPopupKinds minus
// overlayExclusions) plus GLOBAL. The expected set is derived from
// types.AllContextKeys() via an independent oracle (allContextKeyKinds),
// NOT from the list injected into production, so it fails loudly if any
// non-popup context is ever absent from the expansion.
func TestAllKnownContextsCompleteness(t *testing.T) {
	kinds := allContextKeyKinds()
	kindOf := staticKind(kinds)

	// Independent oracle: same policy production applies, computed here
	// in the test's own code over types.AllContextKeys().
	expected := map[types.ContextKey]struct{}{types.GLOBAL: {}}
	for _, k := range types.AllContextKeys() {
		if _, overlay := overlayExclusions[k]; overlay {
			continue
		}
		if _, ok := nonPopupKinds[kinds[k]]; ok {
			expected[k] = struct{}{}
		}
	}

	// Deliberate explicit fixture input (NOT the no-arg fallback).
	got := scopeAllExpansion(t, types.AllContextKeys(), kindOf)

	if !sameSet(got, expected) {
		t.Errorf("scope:all expansion mismatch\n got: %v\nwant: %v", keysOf(got), keysOf(expected))
	}

	// Targeted membership assertions (the CONNECTION_MANAGER + CHEATSHEET gap).
	for _, must := range []types.ContextKey{types.CONNECTION_MANAGER, types.CHEATSHEET} {
		if _, ok := got[must]; !ok {
			t.Errorf("scope:all must reach %s", must)
		}
	}
	for _, mustNot := range []types.ContextKey{types.WHICH_KEY, types.LIMIT, types.HIDE_OVERLAY, types.RELATIONSHIP_PANEL} {
		if _, ok := got[mustNot]; ok {
			t.Errorf("scope:all must NOT reach overlay %s", mustNot)
		}
	}

	// No PERSISTENT/TEMPORARY popup may appear in the expansion.
	for sc := range got {
		switch kinds[sc] {
		case types.PERSISTENT_POPUP, types.TEMPORARY_POPUP:
			t.Errorf("scope:all reached popup %s (kind %v)", sc, kinds[sc])
		}
	}
}

// TestAllKnownContextsDelta asserts the migration's membership delta:
// AFTER == BEFORE + {CONNECTION_MANAGER, CHEATSHEET} − {WHICH_KEY, LIMIT}.
// BEFORE is the pre-change hand-list ∩ real kinds (+GLOBAL); AFTER is the
// live full-set expansion. The delta property is what the constructor
// injection buys.
func TestAllKnownContextsDelta(t *testing.T) {
	kindOf := staticKind(allContextKeyKinds())

	// BEFORE: the legacy hand-list, intersected with real kinds (the old
	// behaviour had no overlayExclusions, so WHICH_KEY/LIMIT were IN).
	handList := []types.ContextKey{
		types.SCHEMAS, types.TABLES, types.COLUMNS, types.INDEXES,
		types.QUERY_EDITOR, types.TABLE_DATA_EDITOR, types.RESULT_GRID,
		types.PLAN, types.MENU, types.CONFIRMATION, types.PROMPT,
		types.SUGGESTIONS, types.COMMAND_LINE, types.HISTORY,
		types.WHICH_KEY, types.LIMIT,
	}
	kinds := allContextKeyKinds()
	before := map[types.ContextKey]struct{}{types.GLOBAL: {}}
	for _, k := range handList {
		if _, ok := nonPopupKinds[kinds[k]]; ok {
			before[k] = struct{}{}
		}
	}

	after := scopeAllExpansion(t, types.AllContextKeys(), kindOf)

	// Compute expected AFTER from BEFORE via the stated delta.
	wantAfter := map[types.ContextKey]struct{}{}
	for k := range before {
		wantAfter[k] = struct{}{}
	}
	wantAfter[types.CONNECTION_MANAGER] = struct{}{}
	wantAfter[types.CHEATSHEET] = struct{}{}
	// SCHEMA_RAIL is the consolidated side-rail container shipped by
	// pgsavvy-i42s.4; it is a new SIDE_CONTEXT member of the scope:all
	// expansion (the SCHEMAS/TABLES leaves remain in AllContextKeys and the
	// kind map, so they also still appear).
	wantAfter[types.SCHEMA_RAIL] = struct{}{}
	delete(wantAfter, types.WHICH_KEY)
	delete(wantAfter, types.LIMIT)

	if !sameSet(after, wantAfter) {
		t.Errorf("AFTER != BEFORE + {CONNECTION_MANAGER, CHEATSHEET} - {WHICH_KEY, LIMIT}\n after: %v\n want:  %v",
			keysOf(after), keysOf(wantAfter))
	}
}

// TestBuildWarnings_IdenticalBeforeAfter pins that the change does not
// alter Build's diagnostics for a fixed config: orphan / collision /
// ambiguous codes and counts are independent of the injected known set.
func TestBuildWarnings_IdenticalBeforeAfter(t *testing.T) {
	reg := testRegistry(t, "a.one", "a.two")
	defaults := []*ChordBinding{
		// orphan (unknown action)
		{Sequence: []Key{{Code: 'z'}}, Mode: types.ModeNormal, Scope: types.GLOBAL, ActionID: "a.unknown", Origin: "o"},
		// collision (same scope/seq)
		{Sequence: []Key{{Code: 't'}, {Code: 'r'}}, Mode: types.ModeNormal, Scope: types.TABLES, ActionID: "a.one", Origin: "c1"},
		{Sequence: []Key{{Code: 't'}, {Code: 'r'}}, Mode: types.ModeNormal, Scope: types.TABLES, ActionID: "a.two", Origin: "c2"},
		// ambiguous prefix
		{Sequence: []Key{{Code: 'g'}}, Mode: types.ModeNormal, Scope: types.GLOBAL, ActionID: "a.one", Origin: "g1"},
		{Sequence: []Key{{Code: 'g'}, {Code: 'g'}}, Mode: types.ModeNormal, Scope: types.GLOBAL, ActionID: "a.two", Origin: "g2"},
	}
	codeCounts := func(known []types.ContextKey) map[string]int {
		svc := NewKeybindingService(known...)
		_, warns, err := svc.Build(defaults, minimalCfg(), reg, staticKind(allContextKeyKinds()))
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		out := map[string]int{}
		for _, w := range warns {
			out[w.Code]++
		}
		return out
	}

	// "before-like" small injected set vs. the full live set: warning
	// codes/counts must be identical for this fixed config.
	small := codeCounts([]types.ContextKey{types.SCHEMAS, types.TABLES, types.GLOBAL})
	full := codeCounts(types.AllContextKeys())

	if len(small) != len(full) {
		t.Fatalf("warning code-set differs: %v vs %v", small, full)
	}
	for code, n := range full {
		if small[code] != n {
			t.Errorf("warning %q count differs: small=%d full=%d", code, small[code], n)
		}
	}
	for _, code := range []string{"orphan_action", "collision", "ambiguous_prefix"} {
		if full[code] == 0 {
			t.Errorf("expected warning %q to be present", code)
		}
	}
}

func sameSet(a, b map[types.ContextKey]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func keysOf(m map[types.ContextKey]struct{}) []types.ContextKey {
	out := make([]types.ContextKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
