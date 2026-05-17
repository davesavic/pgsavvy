package keys

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
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
