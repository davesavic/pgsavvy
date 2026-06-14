package keys

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func cmdWithDesc(id, desc string) *commands.Command {
	return &commands.Command{
		ID:          id,
		Description: desc,
		Handler:     func(commands.ExecCtx) error { return nil },
	}
}

// The which-key popup must list the continuations that would
// actually fire, which (mirroring Dispatch's scope→GLOBAL fall-through) is
// the UNION of the focused scope's children and GLOBAL's children for the
// pending prefix. The scope-specific binding wins on a key collision.
func TestChildrenAtMerged_UnionsScopeAndGlobal_ScopeWins(t *testing.T) {
	space := keyOf(' ')
	ts := buildTrieSet(t, []trieEntry{
		// RESULT_GRID leader continuations.
		{types.ModeNormal, types.RESULT_GRID, []Key{space, keyOf('s')}, cmdWithDesc("result.sort", "Sort")},
		{types.ModeNormal, types.RESULT_GRID, []Key{space, keyOf('X')}, cmdWithDesc("result.close", "Close")},
		// GLOBAL leader continuations, including an X that collides.
		{types.ModeNormal, types.GLOBAL, []Key{space, keyOf('1')}, cmdWithDesc("tab.jump1", "Jump 1")},
		{types.ModeNormal, types.GLOBAL, []Key{space, keyOf('q')}, cmdWithDesc("app.quit", "Quit")},
		{types.ModeNormal, types.GLOBAL, []Key{space, keyOf('X')}, cmdWithDesc("global.x", "GlobalX")},
	})

	rows, ok := ts.ChildrenAtMerged(types.ModeNormal, types.RESULT_GRID, []Key{space})
	if !ok {
		t.Fatalf("ChildrenAtMerged: ok=false, want true")
	}

	byKey := map[Key]ChildRow{}
	for _, r := range rows {
		if _, dup := byKey[r.Key]; dup {
			t.Errorf("duplicate key %v in merged rows", r.Key)
		}
		byKey[r.Key] = r
	}
	for _, want := range []Key{keyOf('1'), keyOf('X'), keyOf('q'), keyOf('s')} {
		if _, found := byKey[want]; !found {
			t.Errorf("merged rows missing key %v", want)
		}
	}
	if len(rows) != 4 {
		t.Errorf("len(rows) = %d, want 4 (deduped union)", len(rows))
	}
	// Scope wins the X collision: label is the scope binding's description.
	if got := byKey[keyOf('X')].Label; got != "Close" {
		t.Errorf("X row Label = %q, want %q (scope wins)", got, "Close")
	}
	// Deterministic: sorted by Key.String().
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Key.String() > rows[i].Key.String() {
			t.Errorf("rows not sorted: %q before %q", rows[i-1].Key.String(), rows[i].Key.String())
		}
	}
}

// When the focused scope has no bindings for the prefix at all, the GLOBAL
// continuations must still surface (the bug: scope-only lookup returned
// nothing on RESULT_GRID).
func TestChildrenAtMerged_PrefixOnlyInGlobal_StillFound(t *testing.T) {
	space := keyOf(' ')
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.GLOBAL, []Key{space, keyOf('q')}, cmdWithDesc("app.quit", "Quit")},
	})

	rows, ok := ts.ChildrenAtMerged(types.ModeNormal, types.RESULT_GRID, []Key{space})
	if !ok {
		t.Fatalf("ok=false, want true (global resolves the prefix)")
	}
	if len(rows) != 1 || rows[0].Key != keyOf('q') {
		t.Errorf("rows = %+v, want single q from global", rows)
	}
}

// When the focused scope IS GLOBAL, children must not be collected twice.
func TestChildrenAtMerged_ScopeIsGlobal_NoDoubleCollect(t *testing.T) {
	space := keyOf(' ')
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.GLOBAL, []Key{space, keyOf('q')}, cmdWithDesc("app.quit", "Quit")},
	})

	rows, ok := ts.ChildrenAtMerged(types.ModeNormal, types.GLOBAL, []Key{space})
	if !ok || len(rows) != 1 {
		t.Fatalf("rows=%+v ok=%v, want single q", rows, ok)
	}
}

// Prefix absent from both tries → not found, nil rows.
func TestChildrenAtMerged_NeitherResolves_NotFound(t *testing.T) {
	ts := buildTrieSet(t, []trieEntry{
		{types.ModeNormal, types.GLOBAL, []Key{keyOf('q')}, cmdWithDesc("app.quit", "Quit")},
	})

	rows, ok := ts.ChildrenAtMerged(types.ModeNormal, types.RESULT_GRID, []Key{keyOf('z')})
	if ok {
		t.Errorf("ok=true, want false (z not in any trie)")
	}
	if rows != nil {
		t.Errorf("rows = %+v, want nil", rows)
	}
}
