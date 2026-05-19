package editor_test

import (
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
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
