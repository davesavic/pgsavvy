package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newTestCellEditor(drv types.GuiDriver) *CellEditorContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      CellEditorKey(),
		ViewName: string(CellEditorKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewCellEditorContext(base, deps)
}

func TestCellEditorContext_InactiveByDefault(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCellEditor(drv)
	if c.Active() {
		t.Fatal("Active() = true on fresh context; want false")
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times while inactive; want 0", drv.writes)
	}
}

func TestCellEditorContext_OpenCapturesSnapshot(t *testing.T) {
	c := newTestCellEditor(nil)
	col := models.ColumnMeta{Name: "email", TypeName: "text"}
	pk := []any{int64(42)}
	c.Open("alice@example.com", col, pk, "alice@example.com")

	if !c.Active() {
		t.Fatal("Active() = false after Open(); want true")
	}
	if got := c.OriginalValue(); got != "alice@example.com" {
		t.Errorf("OriginalValue = %v, want %q", got, "alice@example.com")
	}
	if got := c.Column().Name; got != "email" {
		t.Errorf("Column.Name = %q, want %q", got, "email")
	}
	gotPK := c.PrimaryKey()
	if len(gotPK) != 1 || gotPK[0] != int64(42) {
		t.Errorf("PrimaryKey = %v, want [42]", gotPK)
	}
	if got := c.Buffer(); got != "alice@example.com" {
		t.Errorf("Buffer = %q, want seeded value", got)
	}
}

func TestCellEditorContext_OpenDefensivelyCopiesPrimaryKey(t *testing.T) {
	c := newTestCellEditor(nil)
	pk := []any{int64(1), "a"}
	c.Open(nil, models.ColumnMeta{Name: "x"}, pk, "")
	// Mutating the caller's slice after Open must not bleed into the
	// context — F3's PendingEdit.PrimaryKey carries optimistic-
	// concurrency identity; a torn PK would mis-identify the row.
	pk[0] = int64(999)
	got := c.PrimaryKey()
	if got[0] != int64(1) {
		t.Errorf("PrimaryKey[0] = %v, want 1 (defensive copy must isolate)", got[0])
	}
}

func TestCellEditorContext_PrimaryKeyReturnsDefensiveCopy(t *testing.T) {
	c := newTestCellEditor(nil)
	c.Open(nil, models.ColumnMeta{Name: "x"}, []any{int64(7)}, "")
	first := c.PrimaryKey()
	first[0] = int64(999)
	second := c.PrimaryKey()
	if second[0] != int64(7) {
		t.Errorf("PrimaryKey[0] = %v after caller-mutation; want 7", second[0])
	}
}

func TestCellEditorContext_CloseResetsState(t *testing.T) {
	c := newTestCellEditor(nil)
	c.Open("v", models.ColumnMeta{Name: "x"}, []any{int64(1)}, "v")
	c.Close()
	if c.Active() {
		t.Error("Active() = true after Close; want false")
	}
	if c.OriginalValue() != nil {
		t.Errorf("OriginalValue = %v after Close; want nil", c.OriginalValue())
	}
	if c.Column().Name != "" {
		t.Errorf("Column.Name = %q after Close; want empty", c.Column().Name)
	}
	if len(c.PrimaryKey()) != 0 {
		t.Errorf("PrimaryKey = %v after Close; want empty", c.PrimaryKey())
	}
	if c.Buffer() != "" {
		t.Errorf("Buffer = %q after Close; want empty", c.Buffer())
	}
}

func TestCellEditorContext_HandleRenderWhileActiveWritesBuffer(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCellEditor(drv)
	c.Open("alice", models.ColumnMeta{Name: "name"}, []any{int64(1)}, "alice")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if drv.lastView != string(CellEditorKey()) {
		t.Errorf("view = %q, want %q", drv.lastView, string(CellEditorKey()))
	}
	if !strings.Contains(drv.lastContent, "alice") {
		t.Errorf("body missing buffer; got %q", drv.lastContent)
	}
	if !strings.HasPrefix(drv.lastContent, "> ") {
		t.Errorf("body missing '> ' prefix; got %q", drv.lastContent)
	}
}

func TestCellEditorContext_HandleRenderInactiveNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCellEditor(drv)
	c.Open("v", models.ColumnMeta{Name: "x"}, []any{int64(1)}, "v")
	c.Close()
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times after Close; want 0", drv.writes)
	}
}

func TestCellEditorContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestCellEditor(nil)
	c.Open("v", models.ColumnMeta{Name: "x"}, []any{int64(1)}, "v")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestCellEditorContext_ReadAndClearBuffer(t *testing.T) {
	c := newTestCellEditor(nil)
	c.Open("original", models.ColumnMeta{Name: "x"}, []any{int64(1)}, "original")
	c.SetBuffer("typed")
	got := c.ReadAndClearBuffer()
	if got != "typed" {
		t.Errorf("ReadAndClearBuffer = %q, want %q", got, "typed")
	}
	if c.Buffer() != "" {
		t.Errorf("Buffer = %q after ReadAndClearBuffer; want empty", c.Buffer())
	}
}

func TestCellEditorContext_SatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &CellEditorContext{}
}

// TestCellEditorContext_CursorXYNoView pins the no-view (unit-test) caret
// contract: inactive → ok=false so the layout skips placing a caret;
// active → x = prefix width (2) + the buffer rune count (cursor assumed at
// end), y = 0. The horizontally-scrolled view path is exercised in the
// tmux integration check (no real gocui view in unit tests).
func TestCellEditorContext_CursorXYNoView(t *testing.T) {
	c := newTestCellEditor(nil)
	if _, _, ok := c.CursorXY(); ok {
		t.Fatal("CursorXY ok=true while inactive; want false")
	}
	c.Open("plan", models.ColumnMeta{Name: "data"}, []any{int64(1)}, "plan")
	x, y, ok := c.CursorXY()
	if !ok {
		t.Fatal("CursorXY ok=false while active; want true")
	}
	if want := len(cellEditorPrefix) + len("plan"); x != want {
		t.Errorf("CursorXY x = %d, want %d", x, want)
	}
	if y != 0 {
		t.Errorf("CursorXY y = %d, want 0", y)
	}
}

// TestWindowRunes covers the horizontal-scroll window: clamping a start
// past the end, a zero window (no measurable view width) returning the
// remainder, normal mid-string windows, and multibyte rune boundaries
// (the slice must not split a rune).
func TestWindowRunes(t *testing.T) {
	cases := []struct {
		name       string
		s          string
		start, win int
		want       string
	}{
		{"zero win returns remainder", "abcdef", 2, 0, "cdef"},
		{"window clamps to end", "abcdef", 3, 10, "def"},
		{"mid window", "abcdef", 1, 3, "bcd"},
		{"start past end", "abc", 9, 4, ""},
		{"negative start clamps to 0", "abc", -2, 2, "ab"},
		{"multibyte boundary", "héllo", 0, 3, "hél"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowRunes(tc.s, tc.start, tc.win); got != tc.want {
				t.Errorf("windowRunes(%q,%d,%d) = %q, want %q", tc.s, tc.start, tc.win, got, tc.want)
			}
		})
	}
}

// caretCaptureDriver embeds captureDriver (which records SetContent) and
// additionally records the last SetCaretEnabled call so the focus hooks'
// caret toggle is observable. The embedded stubDriver's SetCaretEnabled is
// a no-op; overriding it here makes it assertable.
type caretCaptureDriver struct {
	captureDriver
	caretEnabled bool
	caretSet     bool
}

func (c *caretCaptureDriver) SetCaretEnabled(enabled bool) {
	c.caretEnabled = enabled
	c.caretSet = true
}

func TestCellEditorHandleFocusSetsInsertModeAndCaret(t *testing.T) {
	drv := &caretCaptureDriver{}
	c := newTestCellEditor(drv)
	modes := newFakeModeStore()
	c.SetModes(modes)

	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}

	if got, ok := modes.set[types.CELL_EDITOR]; !ok || got != types.ModeInsert {
		t.Errorf("mode for CELL_EDITOR = %v (set=%v); want ModeInsert", got, ok)
	}
	if got := modes.set[types.CELL_EDITOR]; got == types.ModeCommand {
		t.Errorf("mode = ModeCommand; want ModeInsert (deliberate divergence from PROMPT)")
	}
	if !drv.caretSet || !drv.caretEnabled {
		t.Errorf("caret enabled = %v (set=%v); want true", drv.caretEnabled, drv.caretSet)
	}
}

func TestCellEditorHandleFocusLostResets(t *testing.T) {
	drv := &caretCaptureDriver{}
	c := newTestCellEditor(drv)
	modes := newFakeModeStore()
	c.SetModes(modes)

	// Focus first so there's a mode to reset and a caret that was on.
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	// Seed a buffer so we can prove HandleFocusLost clears it.
	c.SetBuffer("leftover")

	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost: %v", err)
	}

	if len(modes.resets) != 1 || modes.resets[0] != types.CELL_EDITOR {
		t.Errorf("resets = %v; want [CELL_EDITOR]", modes.resets)
	}
	if _, ok := modes.set[types.CELL_EDITOR]; ok {
		t.Errorf("CELL_EDITOR mode still set after HandleFocusLost; want cleared")
	}
	if !drv.caretSet || drv.caretEnabled {
		t.Errorf("caret enabled = %v (set=%v); want false", drv.caretEnabled, drv.caretSet)
	}
	if c.Buffer() != "" {
		t.Errorf("Buffer = %q after HandleFocusLost; want empty (buf cleared)", c.Buffer())
	}
}

func TestCellEditorFocusHooksNilSafe(t *testing.T) {
	// No SetModes (modes nil) and nil GuiDriver: focus hooks must not panic.
	c := newTestCellEditor(nil)
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus with nil modes/driver: %v", err)
	}
	if err := c.HandleFocusLost(types.OnFocusLostOpts{}); err != nil {
		t.Fatalf("HandleFocusLost with nil modes/driver: %v", err)
	}
}

func TestCellEditorKey_Stable(t *testing.T) {
	// Z1 will replace cellEditorKey with types.CELL_EDITOR. Until
	// then the value MUST remain "cell_editor" so the orchestrator's
	// view-name registration (Z1) can find the popup.
	if got := string(CellEditorKey()); got != "cell_editor" {
		t.Errorf("CellEditorKey = %q, want %q", got, "cell_editor")
	}
}
