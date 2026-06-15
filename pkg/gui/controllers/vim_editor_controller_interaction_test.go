package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// This file holds the cross-feature interaction tests: the
// seam where 6.1 (broadened >=2-char trigger + post-accept suppression),
// 6.2 (alias-on-table-accept, single Replace), and 6.3 (ambiguous-column
// qualify, single Replace) compose. Every test drives the REAL
// VimEditorController + editor.Buffer + SuggestionsContext via the rigs
// defined in vim_editor_controller_insert_test.go (newCompletionRig /
// newQualifyRig / fakeSource / fakeSchemaMeta). No product code is mocked.

// nodeCount returns the buffer's UndoTree node count, treating a nil
// History (the lazy zero state before any Apply) as zero nodes.
func nodeCount(buf *editor.Buffer) int {
	if buf.History == nil {
		return 0
	}
	return buf.History.NodeCount()
}

// TestInteractionBroadenedTriggerThenSuppression proves the 6.1 contract
// end-to-end: a bare >=2-char identifier prefix auto-opens the popup
// (broadened gate), an accept inserts the candidate, and the very next
// NON-DOT keystroke does NOT re-open the popup over the just-inserted text
// — the one-shot suppression flag, not the gate, owes that keystroke.
//
// Uses a SELECT-clause column context ("SELECT us") so the accept is a bare
// insert (no alias/qualify), isolating the trigger+suppression behavior.
func TestInteractionBroadenedTriggerThenSuppression(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "SELECT us", 9, []string{"username"})

	// 2-char bare prefix opens via the broadened auto-trigger gate.
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("AutoTrigger did not open popup on 2-char prefix (broadened gate)")
	}

	// Accept (Enter via the completion seam). SELECT is a column context, so
	// the bare candidate replaces the prefix; no alias/qualify.
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT username" {
		t.Fatalf("after accept = %q; want %q", got, "SELECT username")
	}
	if sugg.IsVisible() {
		t.Fatal("popup still visible immediately after accept")
	}

	// The next non-dot keystroke fires AutoTrigger. The just-inserted
	// "username" is a >=2-char prefix that the broadened gate would otherwise
	// re-open — but the one-shot suppression flag swallows this single
	// keystroke, so the popup stays hidden over the accepted text.
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("popup re-opened on the post-accept keystroke; suppression failed")
	}
}

// TestInteractionDotChainAfterTableAccept proves the 6.1+6.2 dot-chain:
// accepting a TABLE auto-inserts an editable alias (6.2 single Replace),
// arming the post-accept suppression; typing `.` immediately after is an
// explicit `<ident>.` column trigger whose IsIdentDotContext override
// bypasses suppression and opens the COLUMN popup.
//
// This is the load-bearing cross-feature case: the alias insert ("users u")
// means the dot lands after the alias `u`, and the column popup must still
// open there.
func TestInteractionDotChainAfterTableAccept(t *testing.T) {
	// Schema metadata so the post-dot column trigger has candidates: after
	// "users u." the schema source resolves alias u -> users -> {id,name}.
	meta := fakeSchemaMeta{
		cols:   map[string][]models.Column{"public.users": cols("id", "name")},
		warmed: map[string]bool{"public.users": true},
	}
	ctrl, buf, sugg := newQualifyRig(t, "SELECT * FROM us", 16, []string{"users"}, meta)

	// Open + accept the table. 6.2 appends the deduped alias -> "users u".
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("AutoTrigger did not open the table popup")
	}
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("table accept via Enter not consumed")
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users u" {
		t.Fatalf("after table accept = %q; want %q", got, "SELECT * FROM users u")
	}
	if sugg.IsVisible() {
		t.Fatal("popup still visible immediately after table accept")
	}

	// Type `.` after the alias `u`. Suppression is armed, but the dot is an
	// IsIdentDotContext override -> the column popup must open.
	buf.Lines = []editor.Line{{Runes: []rune("SELECT * FROM users u.")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 22})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("`.` after alias did not open the column popup; dot-chain override failed")
	}
}

// TestInteractionAliasInsertSingleUndoNode proves the 6.2 alias insert is a
// SINGLE undo node: one EditKindReplace, asserted both by the resulting text
// AND by the buffer's UndoTree node count (exactly one node added by the
// accept). A multi-edit accept would leave >1 node.
func TestInteractionAliasInsertSingleUndoNode(t *testing.T) {
	ctrl, _, buf, _, _ := newCompletionRig(t, "SELECT * FROM us", 16, []string{"users"})

	before := nodeCount(buf)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM users u" {
		t.Fatalf("after accept = %q; want %q", got, "SELECT * FROM users u")
	}
	if added := nodeCount(buf) - before; added != 1 {
		t.Fatalf("alias accept added %d undo nodes; want exactly 1 (single EditKindReplace)", added)
	}

	// One undo reverts the WHOLE insertion back to the typed prefix.
	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT * FROM us" {
		t.Fatalf("after single undo = %q; want %q", got, "SELECT * FROM us")
	}
}

// TestInteractionQualifyInsertSingleUndoNode proves the 6.3 qualify insert is
// likewise a SINGLE undo node: the "<alias>.<column>" replacement is one
// EditKindReplace (asserted by NodeCount delta) and one undo reverts it whole.
func TestInteractionQualifyInsertSingleUndoNode(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.users":    cols("id", "name"),
			"public.accounts": cols("id", "name"),
		},
		warmed: map[string]bool{"public.users": true, "public.accounts": true},
	}
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM users u JOIN accounts a", 9, []string{"name"}, meta)

	before := nodeCount(buf)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	if !ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter}) {
		t.Fatal("accept via Enter not consumed")
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT u.name FROM users u JOIN accounts a" {
		t.Fatalf("after accept = %q; want qualified", got)
	}
	if added := nodeCount(buf) - before; added != 1 {
		t.Fatalf("qualify accept added %d undo nodes; want exactly 1 (single EditKindReplace)", added)
	}

	if err := buf.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := string(buf.Lines[0].Runes); got != "SELECT na FROM users u JOIN accounts a" {
		t.Fatalf("after single undo = %q; want %q", got, "SELECT na FROM users u JOIN accounts a")
	}
}

// TestInteractionQuotedIdentThreshold documents+pins the >=2-char trigger
// threshold for a QUOTED identifier prefix. identifierPrefixAt's rune scan
// stops at the opening `"`, so the prefix is the run AFTER the quote:
//   - `"Co`  -> prefix "Co" (2 runes) -> AutoTrigger opens.
//   - `"C`   -> prefix "C"  (1 rune)  -> AutoTrigger stays closed.
//
// Driven with a bare quoted prefix (no governing SELECT clause) so the
// >=2-char gate — not a clause-keyword/column context — is the deciding
// factor. (`SELECT "C` is a column context and opens regardless; that is a
// distinct path covered by the editor-package TestAutoTriggerFromContext
// table — see TestAutoTriggerFromContext_Cases.)
func TestInteractionQuotedIdentThreshold(t *testing.T) {
	t.Run("two runes opens", func(t *testing.T) {
		ctrl, _, buf, sugg, _ := newCompletionRig(t, `"Co`, 3, []string{"Country"})
		ctrl.AutoTrigger(buf, buf.CursorPos())
		if !sugg.IsVisible() {
			t.Error(`AutoTrigger did not open on "Co (2-rune quoted prefix); want visible`)
		}
	})
	t.Run("one rune stays closed", func(t *testing.T) {
		ctrl, _, buf, sugg, _ := newCompletionRig(t, `"C`, 2, []string{"Country"})
		ctrl.AutoTrigger(buf, buf.CursorPos())
		if sugg.IsVisible() {
			t.Error(`AutoTrigger opened on "C (1-rune quoted prefix); want hidden`)
		}
	})
}

// TestInteractionQualifyAliaslessFirstOwner pins the 6.3 [exec note]
// behavior that was documented in code but previously UNTESTED:
// composeColumnQualifier uses the first in-scope owner WITH a non-empty
// alias, NOT strictly the first owner. An aliasless owning table cannot
// supply a qualifier, so it is skipped.
//
// owners = [accounts (NO alias), users (alias u)] both own "name":
// accounts is first in FROM order but aliasless, so the qualifier falls
// through to users -> "u.name".
func TestInteractionQualifyAliaslessFirstOwner(t *testing.T) {
	meta := fakeSchemaMeta{
		cols: map[string][]models.Column{
			"public.accounts": cols("id", "name"),
			"public.users":    cols("id", "name"),
		},
		warmed: map[string]bool{"public.accounts": true, "public.users": true},
	}
	// "FROM accounts JOIN users u" — accounts has no alias, users has alias u.
	ctrl, buf, _ := newQualifyRig(t, "SELECT na FROM accounts JOIN users u", 9, []string{"name"}, meta)
	ctrl.RefilterOrTrigger(buf, buf.CursorPos())
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "SELECT u.name FROM accounts JOIN users u" {
		t.Fatalf("aliasless-first-owner accept = %q; want %q (first ALIASED owner wins)", got, "SELECT u.name FROM accounts JOIN users u")
	}
}

// TestInteractionStaleAnchorAcceptUnderBroadenedGate proves the edge path:
// when the prefix the popup was filtering is edited out from under it, an
// accept under the broadened gate aborts cleanly without corrupting the
// buffer (the resolved identStart..cur range no longer matches the popup's
// anchor). Mirrors TestCompletionStaleAnchorAbortsReplace but pins it in the
// cross-feature suite as a required-coverage negative path.
func TestInteractionStaleAnchorAcceptUnderBroadenedGate(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "us", 2, []string{"users"})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible after broadened-gate trigger")
	}
	// User deletes the whole "us" prefix before accepting.
	buf.Lines = []editor.Line{{Runes: []rune("")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
	_ = ctrl.CompletionKey(keys.Key{Special: keys.KeyEnter})
	if got := string(buf.Lines[0].Runes); got != "" {
		t.Fatalf("buffer corrupted by stale-anchor accept = %q; want empty (abort)", got)
	}
	if sugg.IsVisible() {
		t.Error("popup not dismissed after stale-anchor accept")
	}
}

// TestInteractionBackspaceWithinPrefixRefilters proves+documents the
// refilter edge path under the broadened gate. Two distinct rules:
//   - The >=2-char threshold gates only the OPENING of a hidden popup
//     (AutoTrigger's open-gate branch). Once the popup is VISIBLE, AutoTrigger
//     refilters in place and bypasses the width gate entirely — so backspacing
//     within (or even below) the threshold while still matching keeps the
//     popup open. Dismissal is driven by the candidate set going EMPTY, not by
//     the prefix dropping below 2 chars. (Mirrors TestAutoTriggerRefiltersWhileVisible
//     and TestAutoTriggerBackspaceRefiltersToEmptyDismisses.)
func TestInteractionBackspaceWithinPrefixRefilters(t *testing.T) {
	ctrl, _, buf, sugg, _ := newCompletionRig(t, "use", 3, []string{"users", "usage"})
	// "use" -> only "users" matches (3-char prefix). Popup opens via gate.
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup not visible at 3-char prefix")
	}
	if n := len(sugg.Suggestions()); n != 1 {
		t.Fatalf("candidate count at \"use\" = %d; want 1 (users)", n)
	}

	// Backspace to "us" (still >=2 chars): refilters in place, popup stays
	// open, both candidates match again.
	buf.Lines = []editor.Line{{Runes: []rune("us")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 2})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("popup dismissed at 2-char prefix; want still visible (refilter)")
	}
	if n := len(sugg.Suggestions()); n != 2 {
		t.Fatalf("candidate count after backspace to \"us\" = %d; want 2", n)
	}

	// Backspace to "u" (1 char, BELOW the open threshold): because the popup
	// is already visible, the refilter branch bypasses the width gate. "u"
	// still prefix-matches both candidates, so the popup stays open and
	// refilters — it does NOT dismiss merely on dropping below 2 chars. The
	// width gate governs OPENING, not staying open.
	buf.Lines = []editor.Line{{Runes: []rune("u")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 1})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if !sugg.IsVisible() {
		t.Fatal("visible popup dismissed at 1-char matching prefix; want still visible (refilter bypasses width gate)")
	}
	if n := len(sugg.Suggestions()); n != 2 {
		t.Fatalf("candidate count at \"u\" = %d; want 2 (both still match)", n)
	}

	// Backspace/edit to a NON-matching prefix ("z"): refilter yields an empty
	// candidate set, which IS the dismissal trigger.
	buf.Lines = []editor.Line{{Runes: []rune("z")}}
	buf.SetCursor(editor.Position{Line: 0, Col: 1})
	ctrl.AutoTrigger(buf, buf.CursorPos())
	if sugg.IsVisible() {
		t.Error("popup still visible after refilter to empty candidate set; want dismissed")
	}
}
