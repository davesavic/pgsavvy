package context

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// railRow is a minimal test row carrying just a searchable name.
type railRow struct{ name string }

func railName(v any) string { return v.(*railRow).name }

// newRailCtx builds a SideListContext with the given row names, a name
// accessor wired, and the cursor at 0.
func newRailCtx(t *testing.T, names ...string) *SideListContext {
	t.Helper()
	base := NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT})
	c := NewSideListContext(base, Deps{})
	c.SetRailNameAccessor(railName)
	items := make([]any, len(names))
	for i, n := range names {
		items[i] = &railRow{name: n}
	}
	c.SetItems(items)
	return &c
}

// slice returns the matched substring out of the row's name for assertion.
func matchText(c *SideListContext, m RailMatch) string {
	return railName(c.Items()[m.RowIndex])[m.ByteStart:m.ByteEnd]
}

func TestRailSearch_SubstringOverRows(t *testing.T) {
	c := newRailCtx(t, "audit", "users", "user_roles")
	c.SetSearch("user")

	got := c.Matches()
	if len(got) != 2 {
		t.Fatalf("matches = %d, want 2", len(got))
	}
	wantRows := []int{1, 2}
	for i, m := range got {
		if m.RowIndex != wantRows[i] {
			t.Errorf("match[%d].RowIndex = %d, want %d", i, m.RowIndex, wantRows[i])
		}
		if matchText(c, m) != "user" {
			t.Errorf("match[%d] text = %q, want %q", i, matchText(c, m), "user")
		}
	}
}

func TestRailSearch_SmartCase(t *testing.T) {
	// lowercase query => case-insensitive: matches "User" too.
	c := newRailCtx(t, "Users", "audit")
	c.SetSearch("user")
	if got := len(c.Matches()); got != 1 {
		t.Fatalf("lowercase query matches = %d, want 1 (case-insensitive)", got)
	}
	if txt := matchText(c, c.Matches()[0]); txt != "User" {
		t.Errorf("matched text = %q, want %q", txt, "User")
	}

	// uppercase in query => case-sensitive: "user" no longer matches "User".
	c2 := newRailCtx(t, "Users", "audit")
	c2.SetSearch("User")
	if got := len(c2.Matches()); got != 1 {
		t.Fatalf("case-sensitive query matches = %d, want 1", got)
	}

	c3 := newRailCtx(t, "users")
	c3.SetSearch("User") // uppercase => case-sensitive, no match against lowercase
	if got := len(c3.Matches()); got != 0 {
		t.Fatalf("case-sensitive query matches = %d, want 0", got)
	}
	if !c3.SearchActive() {
		t.Error("zero-match query should still be active")
	}
}

func TestRailSearch_MultibyteCaseFold(t *testing.T) {
	// "aÉb": 'É' (2 bytes) lowercases to 'é' (2 bytes). Lowercase query
	// "é" => case-insensitive, must match the uppercase rune with offsets
	// into the ORIGINAL name.
	c := newRailCtx(t, "aÉb")
	c.SetSearch("é")
	got := c.Matches()
	if len(got) != 1 {
		t.Fatalf("matches = %d, want 1", len(got))
	}
	if txt := matchText(c, got[0]); txt != "É" {
		t.Errorf("matched text = %q, want %q (original uppercase rune)", txt, "É")
	}
}

func TestRailSearch_MultibyteOffsets(t *testing.T) {
	// "héllo" — 'é' is 2 bytes; search "llo" must slice the original.
	c := newRailCtx(t, "héllo")
	c.SetSearch("llo")
	got := c.Matches()
	if len(got) != 1 {
		t.Fatalf("matches = %d, want 1", len(got))
	}
	if txt := matchText(c, got[0]); txt != "llo" {
		t.Errorf("matched text = %q, want %q", txt, "llo")
	}
}

func TestRailSearch_VisibleExcludesHiddenRows(t *testing.T) {
	c := newRailCtx(t, "user_a", "user_b", "user_c")
	// Hide index 1.
	c.SetRailVisible(func(i int) bool { return i != 1 })
	c.SetSearch("user")

	for _, m := range c.Matches() {
		if m.RowIndex == 1 {
			t.Fatalf("hidden row 1 appeared in matches")
		}
	}
	if len(c.Matches()) != 2 {
		t.Fatalf("matches = %d, want 2 (hidden row excluded)", len(c.Matches()))
	}
	// SetSearch landed on first visible match (index 0).
	if c.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0", c.Cursor())
	}
	// Stepping never lands on the hidden row.
	c.NextMatch()
	if c.Cursor() == 1 {
		t.Errorf("NextMatch landed on hidden row 1")
	}
	c.PrevMatch()
	if c.Cursor() == 1 {
		t.Errorf("PrevMatch landed on hidden row 1")
	}
}

func TestRailSearch_LandsCursorOnFirstMatchAtOrAfter(t *testing.T) {
	c := newRailCtx(t, "users", "audit", "user_roles")
	c.SetCursor(2) // cursor at index 2
	c.SetSearch("user")
	// matches at rows 0 and 2; first at/after cursor 2 is row 2.
	if c.Cursor() != 2 {
		t.Errorf("cursor = %d, want 2", c.Cursor())
	}

	// Cursor at 0: first match at/after 0 is row 0.
	c2 := newRailCtx(t, "users", "audit", "user_roles")
	c2.SetSearch("user")
	if c2.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0", c2.Cursor())
	}
}

func TestRailSearch_EmptyQueryClearsCursorUnmoved(t *testing.T) {
	c := newRailCtx(t, "users", "audit", "user_roles")
	c.SetCursor(1)
	c.SetSearch("")
	if c.SearchActive() {
		t.Error("empty query should not be active")
	}
	if c.Cursor() != 1 {
		t.Errorf("cursor = %d, want 1 (unmoved)", c.Cursor())
	}
	if len(c.Matches()) != 0 {
		t.Errorf("matches = %d, want 0", len(c.Matches()))
	}
}

func TestRailSearch_NextPrevWrap(t *testing.T) {
	// Named scenario: [audit, users, user_roles], cursor 0, search "user"
	// => cursor on index 1, total 2; NextMatch twice wraps back to "users".
	c := newRailCtx(t, "audit", "users", "user_roles")
	c.SetSearch("user")
	if c.Cursor() != 1 {
		t.Fatalf("after SetSearch cursor = %d, want 1", c.Cursor())
	}
	_, _, total, _ := c.SearchStatus()
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	c.NextMatch() // -> index 2 (user_roles)
	if c.Cursor() != 2 {
		t.Errorf("after 1 Next cursor = %d, want 2", c.Cursor())
	}
	c.NextMatch() // wrap -> index 1 (users)
	if c.Cursor() != 1 {
		t.Errorf("after wrap cursor = %d, want 1", c.Cursor())
	}

	// PrevMatch wraps the other way.
	c.PrevMatch() // -> index 2
	if c.Cursor() != 2 {
		t.Errorf("after Prev wrap cursor = %d, want 2", c.Cursor())
	}
}

func TestRailSearch_NextPrevNoOpWhenZeroMatches(t *testing.T) {
	c := newRailCtx(t, "users", "audit")
	c.SetCursor(1)
	c.SetSearch("zzz") // no matches
	if c.Cursor() != 1 {
		t.Fatalf("zero-match SetSearch moved cursor to %d, want 1", c.Cursor())
	}
	c.NextMatch()
	if c.Cursor() != 1 {
		t.Errorf("NextMatch at zero matches moved cursor to %d", c.Cursor())
	}
	c.PrevMatch()
	if c.Cursor() != 1 {
		t.Errorf("PrevMatch at zero matches moved cursor to %d", c.Cursor())
	}
}

func TestRailSearch_Status(t *testing.T) {
	c := newRailCtx(t, "users", "user_roles")

	// inactive
	q, cur, total, active := c.SearchStatus()
	if q != "" || cur != 0 || total != 0 || active {
		t.Errorf("inactive status = (%q,%d,%d,%v), want empty", q, cur, total, active)
	}
	if c.SearchActive() {
		t.Error("SearchActive should be false before search")
	}

	// active with matches: 1-based cur
	c.SetSearch("user")
	q, cur, total, active = c.SearchStatus()
	if q != "user" || cur != 1 || total != 2 || !active {
		t.Errorf("active status = (%q,%d,%d,%v), want (user,1,2,true)", q, cur, total, active)
	}
	if !c.SearchActive() {
		t.Error("SearchActive should be true")
	}

	// zero-match active: cur 0, active true
	c2 := newRailCtx(t, "users")
	c2.SetSearch("zzz")
	q, cur, total, active = c2.SearchStatus()
	if q != "zzz" || cur != 0 || total != 0 || !active {
		t.Errorf("zero-match status = (%q,%d,%d,%v), want (zzz,0,0,true)", q, cur, total, active)
	}
}

func TestRailSearch_SetItemsZeroesState(t *testing.T) {
	c := newRailCtx(t, "users", "user_roles")
	c.SetSearch("user")
	if !c.SearchActive() {
		t.Fatal("precondition: search should be active")
	}
	c.SetItems([]any{&railRow{name: "fresh"}})
	if c.SearchActive() {
		t.Error("SetItems must zero search state")
	}
	if len(c.Matches()) != 0 {
		t.Errorf("matches = %d, want 0 after SetItems", len(c.Matches()))
	}
}

func TestRailSearch_ExternalSemanticsUnaffected(t *testing.T) {
	c := newRailCtx(t, "audit", "users", "user_roles")
	// Without search: cursor 0, selected item is row 0.
	if c.Cursor() != 0 {
		t.Fatalf("cursor = %d, want 0", c.Cursor())
	}
	if railName(c.SelectedItem()) != "audit" {
		t.Fatalf("selected = %q, want audit", railName(c.SelectedItem()))
	}
	if len(c.Items()) != 3 {
		t.Fatalf("items = %d, want 3", len(c.Items()))
	}

	// Search only moves the cursor; Items unchanged, SelectedItem follows cursor.
	c.SetSearch("user")
	if len(c.Items()) != 3 {
		t.Errorf("items changed after search: %d", len(c.Items()))
	}
	if railName(c.SelectedItem()) != "users" {
		t.Errorf("selected = %q, want users", railName(c.SelectedItem()))
	}
}

func TestRailSearch_EmptyItems(t *testing.T) {
	c := newRailCtx(t) // no rows
	c.SetSearch("user")
	if len(c.Matches()) != 0 {
		t.Errorf("matches = %d, want 0", len(c.Matches()))
	}
	if c.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0", c.Cursor())
	}
	// Next/Prev are safe no-ops.
	c.NextMatch()
	c.PrevMatch()
}

func TestRailSearch_DuplicateSubstringInOneName(t *testing.T) {
	// "user_user" contains two non-overlapping "user" spans on one row.
	c := newRailCtx(t, "user_user")
	c.SetSearch("user")
	got := c.Matches()
	if len(got) != 2 {
		t.Fatalf("matches = %d, want 2", len(got))
	}
	for _, m := range got {
		if m.RowIndex != 0 {
			t.Errorf("RowIndex = %d, want 0", m.RowIndex)
		}
		if matchText(c, m) != "user" {
			t.Errorf("matched text = %q, want user", matchText(c, m))
		}
	}
	// Non-overlapping: second span starts at/after first span's end.
	if got[1].ByteStart < got[0].ByteEnd {
		t.Errorf("spans overlap: %v then %v", got[0], got[1])
	}
}

func TestRailSearch_NilNameAccessorYieldsNoMatches(t *testing.T) {
	base := NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT})
	c := NewSideListContext(base, Deps{})
	c.SetItems([]any{&railRow{name: "users"}})
	c.SetSearch("user") // nameOf nil => zero matches, no panic
	if len(c.Matches()) != 0 {
		t.Errorf("matches = %d, want 0 with nil accessor", len(c.Matches()))
	}
}
