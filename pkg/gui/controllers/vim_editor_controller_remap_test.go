package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// remapHarness wires a full Build → Matcher → VimEditorController stack so
// the cross-mode motion-remap behavior can be exercised end-to-end (the
// same trie/matcher path the live app uses). cfg carries the user's
// keybinding overrides.
type remapHarness struct {
	qec     *context.QueryEditorContext
	matcher *keys.Matcher
	modes   *keys.ModeStore
	reg     *commands.Registry
}

func newRemapHarness(t *testing.T, cfg *config.UserConfig) *remapHarness {
	t.Helper()
	modes := keys.NewModeStore()
	matcher, err := keys.NewMatcher(nil, keys.MatcherConfig{Modes: modes})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	qec := context.NewQueryEditorContext(
		context.NewBaseContext(context.BaseContextOpts{
			Key:      types.QUERY_EDITOR,
			ViewName: string(types.QUERY_EDITOR),
			Kind:     types.MAIN_CONTEXT,
		}),
		types.ContextTreeDeps{},
		modes,
		matcher,
	)
	ctrl := controllers.NewVimEditorController(qec, matcher)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	defaults := ctrl.GetKeybindings(types.KeybindingsOpts{})
	svc := keys.NewKeybindingService(types.QUERY_EDITOR)
	ts, warns, err := svc.Build(defaults, cfg, reg, func(types.ContextKey) types.ContextKind {
		return types.MAIN_CONTEXT
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, w := range warns {
		if w.Code == "orphan_action" || w.Code == "parse_sequence" || w.Code == "parse_mode" {
			t.Fatalf("unexpected build warning: %+v", w)
		}
	}
	matcher.SwapTrieSet(ts)
	return &remapHarness{qec: qec, matcher: matcher, modes: modes, reg: reg}
}

func remapCfg(key string) *config.UserConfig {
	return &config.UserConfig{
		Leader:      " ",
		LocalLeader: ",",
		Keybindings: []config.KeybindingConfig{
			{Mode: "n", Scope: string(types.QUERY_EDITOR), Key: key, Action: commands.MotionLineDown},
		},
	}
}

func (h *remapHarness) press(t *testing.T, r rune) {
	t.Helper()
	if _, err := h.matcher.Dispatch(types.QUERY_EDITOR, keys.Key{Code: r}); err != nil {
		t.Fatalf("Dispatch(%q): %v", r, err)
	}
}

func (h *remapHarness) seed(lines ...string) *editor.Buffer {
	buf := h.qec.Buffer()
	buf.Lines = buf.Lines[:0]
	for _, l := range lines {
		buf.Lines = append(buf.Lines, editor.Line{Runes: []rune(l)})
	}
	return buf
}

// TestVimRemapNormalMovesCursorOneLine: after motion.line_down → n, pressing
// n in Normal moves the cursor down exactly one line.
func TestVimRemapNormalMovesCursorOneLine(t *testing.T) {
	h := newRemapHarness(t, remapCfg("n"))
	buf := h.seed("alpha", "bravo", "charlie")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	h.press(t, 'n')

	if got := buf.CursorPos().Line; got != 1 {
		t.Fatalf("after remapped n: cursor line = %d, want 1", got)
	}
}

// TestVimRemapDeleteComposesSameAsDefault: dn deletes exactly the range dj
// deletes (operator-pending propagation).
func TestVimRemapDeleteComposesSameAsDefault(t *testing.T) {
	// Default behavior: dj on the default trie.
	def := newRemapHarness(t, &config.UserConfig{Leader: " ", LocalLeader: ","})
	dbuf := def.seed("alpha", "bravo", "charlie")
	dbuf.SetCursor(editor.Position{Line: 0, Col: 0})
	def.press(t, 'd')
	def.press(t, 'j')
	want := bufferText(dbuf)

	// Remapped behavior: dn on the j→n trie.
	h := newRemapHarness(t, remapCfg("n"))
	rbuf := h.seed("alpha", "bravo", "charlie")
	rbuf.SetCursor(editor.Position{Line: 0, Col: 0})
	h.press(t, 'd')
	h.press(t, 'n')

	if got := bufferText(rbuf); got != want {
		t.Fatalf("dn result = %q, want dj result %q", got, want)
	}
}

// TestVimRemapCountMovesThreeLines: 3n moves the cursor down exactly 3
// lines (count grammar still reaches the propagated motion).
func TestVimRemapCountMovesThreeLines(t *testing.T) {
	h := newRemapHarness(t, remapCfg("n"))
	buf := h.seed("l0", "l1", "l2", "l3", "l4")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	h.press(t, '3')
	h.press(t, 'n')

	if got := buf.CursorPos().Line; got != 3 {
		t.Fatalf("after 3n: cursor line = %d, want 3", got)
	}
}

// TestVimRemapVisualExtendsSelection: in Visual mode, n extends the
// selection down by one line.
func TestVimRemapVisualExtendsSelection(t *testing.T) {
	h := newRemapHarness(t, remapCfg("n"))
	buf := h.seed("alpha", "bravo", "charlie")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	h.press(t, 'v') // enter visual
	h.press(t, 'n') // remapped line-down extends selection

	sel := buf.Selection
	if sel == nil {
		t.Fatal("no selection after v then remapped n")
	}
	if sel.End.Line != 1 {
		t.Fatalf("visual n: selection end line = %d, want 1", sel.End.Line)
	}
}

// TestVimRemapRepeatReplaysByActionID: after dn, `.` replays the same
// delete by ActionID (LastMotionID == motion.line_down) over an
// equal-length range.
func TestVimRemapRepeatReplaysByActionID(t *testing.T) {
	// Default path: dj then `.` — capture the replayed line-delta.
	def := newRemapHarness(t, &config.UserConfig{Leader: " ", LocalLeader: ","})
	dbuf := def.seed("one", "two", "three", "four", "five")
	dbuf.SetCursor(editor.Position{Line: 0, Col: 0})
	def.press(t, 'd')
	def.press(t, 'j')
	dBeforeReplay := len(dbuf.Lines)
	def.press(t, '.')
	wantDelta := dBeforeReplay - len(dbuf.Lines)

	// Remapped path: dn then `.` — replay must be ActionID-driven and
	// affect the same-length range.
	h := newRemapHarness(t, remapCfg("n"))
	buf := h.seed("one", "two", "three", "four", "five")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	h.press(t, 'd')
	h.press(t, 'n')

	if got := h.qec.Repeat().LastMotionID; got != commands.MotionLineDown {
		t.Fatalf("LastMotionID = %q, want motion.line_down", got)
	}
	beforeReplay := len(buf.Lines)
	h.press(t, '.') // replay by ActionID
	if delta := beforeReplay - len(buf.Lines); delta != wantDelta {
		t.Fatalf("`.` replay removed %d lines, want %d (same as default dj)", delta, wantDelta)
	}
}

// TestVimRemapFreesShippedDefault asserts R3 end-to-end: after j→n the
// original j (and dj) are inert.
func TestVimRemapFreesShippedDefault(t *testing.T) {
	h := newRemapHarness(t, remapCfg("n"))
	buf := h.seed("alpha", "bravo", "charlie")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// j in Normal must NOT move the cursor (no binding → passthrough).
	h.press(t, 'j')
	if got := buf.CursorPos().Line; got != 0 {
		t.Fatalf("freed j still moved cursor to line %d", got)
	}

	// dj must NOT delete (d sets op-pending; j is inert there).
	before := bufferText(buf)
	h.press(t, 'd')
	h.press(t, 'j')
	if got := bufferText(buf); got != before {
		t.Fatalf("freed dj mutated buffer: %q != %q", got, before)
	}
}

// TestVimRemapDigitTargetRejectedKeepsCount asserts R4: motion.line_down → 5
// is rejected, so d5j still parses count=5 (the shipped default and count
// grammar both survive).
func TestVimRemapDigitTargetRejectedKeepsCount(t *testing.T) {
	h := newRemapHarness(t, remapCfg("5"))
	buf := h.seed("l0", "l1", "l2", "l3", "l4", "l5", "l6")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	// d5j: 5 must be a count, not a motion. Deletes 6 lines (0..5 inclusive,
	// linewise dj-with-count semantics). Assert it deletes more than 1 line
	// and the buffer shrank, proving count was applied.
	h.press(t, 'd')
	h.press(t, '5')
	h.press(t, 'j')
	if removed := 7 - len(buf.Lines); removed < 2 {
		t.Fatalf("d5j removed %d lines; count=5 was not parsed (digit shadowed)", removed)
	}
}

// TestVimRemapRegisterTargetRejected asserts R4 for the `"` register prefix:
// after motion.line_down → '"', the register prefix is NOT shadowed by a
// motion leaf. If the remap had been propagated, `"` would resolve to
// motion.line_down (moving the cursor); instead it must enter the
// register-pending state (matcher partial) and leave the cursor put.
func TestVimRemapRegisterTargetRejected(t *testing.T) {
	h := newRemapHarness(t, remapCfg("\""))
	buf := h.seed("alpha", "bravo", "charlie")
	buf.SetCursor(editor.Position{Line: 0, Col: 0})

	h.press(t, '"')

	if got := buf.CursorPos().Line; got != 0 {
		t.Fatalf(`"`+`" was treated as a motion (cursor moved to line %d); register prefix shadowed`, got)
	}
	if !h.matcher.IsPartial() {
		t.Fatal(`"` + `" did not enter register-pending state; register prefix shadowed by rejected remap`)
	}
}

func bufferText(b *editor.Buffer) string {
	out := ""
	for i, l := range b.Lines {
		if i > 0 {
			out += "\n"
		}
		out += string(l.Runes)
	}
	return out
}
