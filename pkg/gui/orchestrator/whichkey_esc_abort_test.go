package orchestrator_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestEscAbortsPendingChordOnNonEditableView pins the Esc-abort behavior.
//
// Non-editable views (list rails like "schemas"/"tables") receive no
// gocui Editor — only per-key SetKeybinding shims, and only for keys
// reachable in the trie. Escape is never a trie-reachable key, so before
// the fix no Esc shim was installed on those views: pressing Escape while
// a leader chord was pending (the which-key overlay showing) was dropped
// by gocui, never reached the Matcher, and so never cleared m.pending nor
// hid the overlay. The overlay ghosted on screen.
//
// The fix installs an explicit Esc shim on every non-editable view so the
// Matcher's existing chord-abort path runs.
func TestEscAbortsPendingChordOnNonEditableView(t *testing.T) {
	g, rec := buildTestGui(t)

	view := string(types.SCHEMAS) // non-editable list rail
	space := gocui.NewKeyRune(' ')
	esc := gocui.NewKeyName(gocui.KeyEsc)

	// Structural: the Esc shim must exist on the non-editable view.
	if !rec.HasKeybinding(view, esc, gocui.ModNone) {
		t.Fatalf("no Esc shim registered on view %q — Escape will be dropped by gocui "+
			"and a pending leader chord can never be aborted", view)
	}

	// Behavioural: feed the leader, confirm the Matcher buffers it, then
	// feed Esc and confirm the chord is aborted (pending cleared, which-key
	// hidden).
	if err := rec.FeedKey(view, space, gocui.ModNone); err != nil {
		t.Fatalf("feed Space on %q: %v", view, err)
	}
	if !g.Matcher().IsPartial() {
		t.Fatal("Matcher not partial after leader key — leader was not buffered")
	}

	if err := rec.FeedKey(view, esc, gocui.ModNone); err != nil {
		t.Fatalf("feed Esc on %q: %v (no Esc shim => gocui drops the key)", view, err)
	}
	if g.Matcher().IsPartial() {
		t.Error("Matcher still partial after Esc — pending leader chord was not aborted")
	}

	notifier := g.Registry().WhichKey.Notifier()
	if _, _, visible := notifier.Snapshot(); visible {
		t.Error("which-key notifier still visible after Esc — overlay would ghost")
	}
}
