package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestEscAbortsPendingChordOnNonEditableView pins the Esc-abort behavior.
//
// Non-editable views (here the consolidated SCHEMA_RAIL view
// "schemas-tables") receive no gocui Editor — only per-key SetKeybinding
// shims, and only for keys reachable in the trie. Escape is never a
// trie-reachable key, so without an explicit shim gocui drops Escape on a
// non-editable view: pressing Escape while a leader chord is pending (the
// which-key overlay showing) would never reach the Matcher, so the chord
// would never be aborted and the overlay would ghost on screen.
//
// The fix installs an explicit Esc shim on every non-editable view so the
// Matcher's existing chord-abort path runs.
//
// NOTE (pgsavvy-i42s.4 → .5): the SCHEMAS/TABLES rails are multiplexed into
// the SCHEMA_RAIL container view "schemas-tables"; the Esc-abort shim moved
// with them. .5 republishes the leader binding (<leader>H toggle-show-hidden)
// under SCHEMA_RAIL, so a leader chord trie now exists for the consolidated
// rail. This test restores the full behavioural coverage: feeding the leader
// under SCHEMA_RAIL leaves the Matcher partial (which-key pending), and Esc
// aborts that pending chord.
func TestEscAbortsPendingChordOnNonEditableView(t *testing.T) {
	g, rec := buildTestGui(t)

	view := "schemas-tables" // SCHEMA_RAIL container view (non-editable)
	esc := gocui.NewKeyName(gocui.KeyEsc)

	// Structural: the Esc shim must exist on the consolidated rail view.
	if !rec.HasKeybinding(view, esc, gocui.ModNone) {
		t.Fatalf("no Esc shim registered on view %q — Escape will be dropped by gocui "+
			"and a pending leader chord can never be aborted", view)
	}

	// Behavioural: feed the leader (' ') under SCHEMA_RAIL. <leader>H is bound
	// there, so the leader is a real chord prefix and the Matcher goes partial.
	g.Matcher().Cancel()
	if _, err := g.Matcher().Dispatch(types.SCHEMA_RAIL, keys.Key{Code: ' '}); err != nil {
		t.Fatalf("Dispatch(leader) under SCHEMA_RAIL: %v", err)
	}
	if !g.Matcher().IsPartial() {
		t.Fatalf("Matcher.IsPartial = false after leader under SCHEMA_RAIL; want partial "+
			"(no leader chord published under %s?)", types.SCHEMA_RAIL)
	}

	// Esc through the shim must abort the pending chord (Matcher no longer partial).
	if err := rec.FeedKey(view, esc, gocui.ModNone); err != nil {
		t.Fatalf("feed Esc on %q: %v (no Esc shim => gocui drops the key)", view, err)
	}
	if g.Matcher().IsPartial() {
		t.Fatalf("Matcher.IsPartial = true after Esc; pending leader chord was not aborted")
	}
}
