package editor_test

import (
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// bufProvider is a one-field BufferProvider that returns its buf
// pointer verbatim. The test rigs use it to wire VimEditor without
// pulling in pkg/gui/context (which would risk an import cycle in
// downstream test bins).
type bufProvider struct{ buf *editor.Buffer }

func (b *bufProvider) Buffer() *editor.Buffer { return b.buf }

// vimRig bundles the moving parts of a VimEditor test: a *Buffer, a
// live Matcher in a given starting mode, and the VimEditor under test.
type vimRig struct {
	buf     *editor.Buffer
	matcher *keys.Matcher
	ve      *editor.VimEditor
}

func newVimRig(t *testing.T, mode types.Mode) *vimRig {
	t.Helper()
	store := keys.NewModeStore()
	store.Set(types.QUERY_EDITOR, mode)
	m, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &editor.Buffer{}
	ve := editor.NewVimEditor(&bufProvider{buf: buf}, m, types.QUERY_EDITOR)
	return &vimRig{buf: buf, matcher: m, ve: ve}
}

func newViewForVimTest() *gocui.View {
	return gocui.NewView("test", 0, 0, 40, 10, gocui.OutputNormal)
}

// TestVimEditor_InsertModeTypesIntoBuffer drives three printable keys
// through Edit in ModeInsert and asserts the canonical *Buffer holds
// the typed text and the cursor advanced to end-of-buffer. The view's
// content mirrors the buffer.
func TestVimEditor_InsertModeTypesIntoBuffer(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	for _, r := range []rune{'a', 'b', 'c'} {
		if handled := rig.ve.Edit(v, gocui.NewKeyRune(r)); !handled {
			t.Fatalf("Edit(%q) returned false; want true", r)
		}
	}
	if got, want := rig.buf.String(), "abc"; got != want {
		t.Errorf("Buffer.String() = %q, want %q", got, want)
	}
	if got, want := rig.buf.CursorPos(), (editor.Position{Line: 0, Col: 3}); got != want {
		t.Errorf("Cursor = %+v, want %+v", got, want)
	}
	// AC: view content mirrors buffer (Architecture Decision 2).
	if got, want := v.Buffer(), "abc"; got != want {
		t.Errorf("View.Buffer() = %q, want %q", got, want)
	}
	cx, cy := v.Cursor()
	if cx != 3 || cy != 0 {
		t.Errorf("View.Cursor() = (%d, %d), want (3, 0)", cx, cy)
	}
}

// TestVimEditor_InsertModeEnterSplitsLine verifies KeyEnter routes
// through Buffer.Apply (NOT through gocui.DefaultEditor) — the cursor
// lands at column 0 of the new line and Buffer.Lines has two entries.
func TestVimEditor_InsertModeEnterSplitsLine(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	rig.ve.Edit(v, gocui.NewKeyRune('a'))
	rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyEnter))
	rig.ve.Edit(v, gocui.NewKeyRune('b'))
	if got, want := rig.buf.String(), "a\nb"; got != want {
		t.Errorf("Buffer.String() = %q, want %q", got, want)
	}
	if got, want := rig.buf.CursorPos(), (editor.Position{Line: 1, Col: 1}); got != want {
		t.Errorf("Cursor = %+v, want %+v", got, want)
	}
}

// TestVimEditor_BackspaceAtStartIsNoOp pins the AC edge case:
// Backspace at start-of-buffer must not panic and must leave the
// buffer empty.
func TestVimEditor_BackspaceAtStartIsNoOp(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	// Defensive: also ensures handled==true (Insert-mode Passthrough
	// always claims the key even when the buffer rejected the edit —
	// otherwise gocui would fall through to its own editor).
	if handled := rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyBackspace)); !handled {
		t.Fatal("Backspace at start: Edit returned false")
	}
	if got := rig.buf.String(); got != "" {
		t.Errorf("Buffer.String() after no-op Backspace = %q, want \"\"", got)
	}
}

// TestVimEditor_BackspaceJoinsLines verifies Backspace at the start of
// a non-first line merges with the prior line.
func TestVimEditor_BackspaceJoinsLines(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	for _, k := range []gocui.Key{
		gocui.NewKeyRune('a'),
		gocui.NewKeyName(gocui.KeyEnter),
		gocui.NewKeyRune('b'),
	} {
		rig.ve.Edit(v, k)
	}
	// Cursor is at (1, 1). Backspace once → deletes 'b' → (1, 0).
	rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyBackspace))
	// Backspace again → joins onto line 0 → cursor (0, 1).
	rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyBackspace))
	if got, want := rig.buf.String(), "a"; got != want {
		t.Errorf("Buffer.String() = %q, want %q", got, want)
	}
	if got, want := rig.buf.CursorPos(), (editor.Position{Line: 0, Col: 1}); got != want {
		t.Errorf("Cursor = %+v, want %+v", got, want)
	}
}

// TestVimEditor_NormalModePassthroughDropped: with no binding for 'x'
// in ModeNormal, Matcher returns FellThrough and VimEditor must NOT
// mutate the Buffer.
func TestVimEditor_NormalModePassthroughDropped(t *testing.T) {
	rig := newVimRig(t, types.ModeNormal)
	v := newViewForVimTest()
	if handled := rig.ve.Edit(v, gocui.NewKeyRune('x')); handled {
		t.Errorf("Edit('x') in Normal returned true; want false (FellThrough)")
	}
	if got := rig.buf.String(); got != "" {
		t.Errorf("Buffer.String() = %q, want \"\" (Normal-mode passthrough must not mutate)", got)
	}
}

// TestVimEditor_ModeSwitchDoesNotRewriteView: switching the underlying
// mode from Insert to Normal between Edit calls must not cause a fresh
// SetContent — VimEditor only mirrors the buffer when a Passthrough
// actually mutated it.
func TestVimEditor_ModeSwitchDoesNotRewriteView(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	rig.ve.Edit(v, gocui.NewKeyRune('a'))
	// Manually scribble distinct cells into the view; if a follow-up
	// Edit needlessly resyncs we'll see the scribble erased.
	v.SetContent("SCRIBBLE")
	v.SetCursor(8, 0)
	// Flip to Normal and feed an unbound printable. Expect Passthrough
	// in Normal → return false → no view rewrite.
	store := keys.NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeNormal)
	// Re-construct the rig's matcher tied to the same store so
	// CurrentMode reflects the new mode (we cannot mutate the rig's
	// matcher store directly without exposing it; build a fresh editor
	// over the same buffer).
	m, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	ve2 := editor.NewVimEditor(&bufProvider{buf: rig.buf}, m, types.QUERY_EDITOR)
	_ = ve2.Edit(v, gocui.NewKeyRune('q'))
	if got, want := v.Buffer(), "SCRIBBLE"; got != want {
		t.Errorf("view.Buffer() = %q, want %q (Normal-mode passthrough must not rewrite view)", got, want)
	}
}

// TestVimEditor_AutoCompleterFiresOnPrintableInsert pins the C5 seam:
// every printable insert-mode rune invokes the wired auto-completer
// callback with the post-insert buffer + cursor. The callback is
// responsible for the config/popup-visible gate; VimEditor itself
// fires unconditionally so the callback can decide.
func TestVimEditor_AutoCompleterFiresOnPrintableInsert(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	var calls int
	var lastCol int
	rig.ve.SetAutoCompleter(func(_ *editor.Buffer, pos editor.Position) {
		calls++
		lastCol = pos.Col
	})
	for _, r := range []rune{'a', 'b', 'c'} {
		rig.ve.Edit(v, gocui.NewKeyRune(r))
	}
	if calls != 3 {
		t.Errorf("autoCompleter calls = %d; want 3 (one per printable rune)", calls)
	}
	if lastCol != 3 {
		t.Errorf("last cursor col = %d; want 3 (post-insert position)", lastCol)
	}
}

// TestVimEditor_AutoCompleterNotFiredOnEnter pins the rule that Enter is
// never an auto-trigger candidate (Enter ending a statement is not a SQL
// completion context). Backspace was carved out of this rule —
// see TestVimEditor_AutoCompleterFiresOnBackspace.
func TestVimEditor_AutoCompleterNotFiredOnEnter(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	var calls int
	rig.ve.SetAutoCompleter(func(_ *editor.Buffer, _ editor.Position) {
		calls++
	})
	rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyEnter))
	if calls != 0 {
		t.Errorf("autoCompleter calls = %d; want 0 (Enter must not trigger)", calls)
	}
}

// TestVimEditor_AutoCompleterFiresOnBackspace pins the
// Backspace refilter hook: deleting a rune fires the callback with the
// post-delete buffer + cursor so a backspace within an active completion
// re-narrows the popup. The callback (controller.AutoTrigger) owns the
// popup-visible gate; VimEditor fires unconditionally on a successful
// delete. A Backspace at start-of-buffer is a no-op and must NOT fire.
func TestVimEditor_AutoCompleterFiresOnBackspace(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	var calls int
	var lastCol int
	rig.ve.SetAutoCompleter(func(_ *editor.Buffer, pos editor.Position) {
		calls++
		lastCol = pos.Col
	})
	// Type "ab" (2 fires), then Backspace once (1 fire, post-delete col 1).
	rig.ve.Edit(v, gocui.NewKeyRune('a'))
	rig.ve.Edit(v, gocui.NewKeyRune('b'))
	rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyBackspace))
	if calls != 3 {
		t.Fatalf("autoCompleter calls = %d; want 3 (2 runes + 1 backspace)", calls)
	}
	if lastCol != 1 {
		t.Errorf("post-backspace cursor col = %d; want 1", lastCol)
	}
}

// TestVimEditor_AutoCompleterNotFiredOnNoOpBackspace pins the edge case:
// Backspace at start-of-buffer deletes nothing, so the callback must not
// fire (otherwise an empty-buffer backspace would spuriously re-evaluate
// the trigger gate).
func TestVimEditor_AutoCompleterNotFiredOnNoOpBackspace(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	var calls int
	rig.ve.SetAutoCompleter(func(_ *editor.Buffer, _ editor.Position) {
		calls++
	})
	rig.ve.Edit(v, gocui.NewKeyName(gocui.KeyBackspace))
	if calls != 0 {
		t.Errorf("autoCompleter calls = %d; want 0 (no-op backspace must not fire)", calls)
	}
}

// TestVimEditor_AutoCompleterNotFiredInNormalMode confirms the callback
// only runs from the Insert-mode insertKey path. Passthrough / fell-
// through in Normal mode is short-circuited before insertKey runs, so
// the callback must remain quiet.
func TestVimEditor_AutoCompleterNotFiredInNormalMode(t *testing.T) {
	rig := newVimRig(t, types.ModeNormal)
	v := newViewForVimTest()
	var calls int
	rig.ve.SetAutoCompleter(func(_ *editor.Buffer, _ editor.Position) {
		calls++
	})
	rig.ve.Edit(v, gocui.NewKeyRune('a'))
	if calls != 0 {
		t.Errorf("autoCompleter calls = %d; want 0 (Normal-mode key must not trigger)", calls)
	}
}

// TestVimEditor_AutoCompleter_CallbackCanGate demonstrates the
// popup-already-visible / config-disabled pattern Z1 will use: the
// callback inspects external state and chooses whether to act. The
// VimEditor seam does not enforce the gate — it invokes the callback
// on every printable rune and trusts the closure.
func TestVimEditor_AutoCompleter_CallbackCanGate(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	v := newViewForVimTest()
	popupVisible := true
	var triggers int
	rig.ve.SetAutoCompleter(func(buf *editor.Buffer, pos editor.Position) {
		// Simulate the Z1 closure: skip when popup already shown or
		// the context isn't trigger-worthy.
		if popupVisible {
			return
		}
		if !editor.AutoTriggerFromContext(buf, pos) {
			return
		}
		triggers++
	})
	// Type "SELECT * FROM " — last char is a space which is the
	// trigger boundary. With popup visible the callback exits early.
	for _, r := range "SELECT * FROM " {
		rig.ve.Edit(v, gocui.NewKeyRune(r))
	}
	if triggers != 0 {
		t.Errorf("triggers = %d; want 0 (popup-visible gate blocks)", triggers)
	}
	// Drop the popup and type one more space — buffer now ends in
	// "FROM  " which still matches reKeywordTable (`\s+$`).
	popupVisible = false
	rig.ve.Edit(v, gocui.NewKeyRune(' '))
	if triggers != 1 {
		t.Errorf("triggers = %d; want 1 (after popup hidden + matching context)", triggers)
	}
}

// TestVimEditor_FeedChordViaRecorder confirms VimEditor satisfies the
// chordDispatcher contract (testfake.RecorderGuiDriver.FeedChord) just
// like masterEditor does. This is the acceptance criterion that wires
// testfake into the editor tests.
func TestVimEditor_FeedChordViaRecorder(t *testing.T) {
	rig := newVimRig(t, types.ModeInsert)
	rec := testfake.NewRecorderGuiDriver()
	if err := rec.SetMasterEditor(string(types.QUERY_EDITOR), rig.ve); err != nil {
		t.Fatalf("SetMasterEditor: %v", err)
	}
	// Insert-mode Passthrough — three printable runes.
	res, err := rec.FeedChord(string(types.QUERY_EDITOR), []keys.Key{
		{Code: 'a'}, {Code: 'b'}, {Code: 'c'},
	})
	if err != nil {
		t.Fatalf("FeedChord: %v", err)
	}
	if res != keys.Passthrough {
		t.Fatalf("FeedChord res = %v, want Passthrough", res)
	}
	if got, want := rig.buf.String(), "abc"; got != want {
		t.Errorf("Buffer.String() = %q, want %q", got, want)
	}
}
