package context

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeWhichKeyState is a hand-rolled types.WhichKeyState fake; using a
// plain struct keeps the test free of cross-package imports.
type fakeWhichKeyState struct {
	scope   types.ContextKey
	prefix  []types.ChordKey
	visible bool
}

func (f *fakeWhichKeyState) Visible() bool { return f.visible }
func (f *fakeWhichKeyState) Snapshot() (types.ContextKey, []types.ChordKey, bool) {
	cp := append([]types.ChordKey(nil), f.prefix...)
	return f.scope, cp, f.visible
}
func (f *fakeWhichKeyState) Hide() { f.visible = false }

// captureDriver implements types.GuiDriver minimally — only SetContent
// is exercised by WhichKeyContext.HandleRender, and Update runs the fn
// synchronously so the assertion can read the captured payload.
type captureDriver struct {
	stubDriver
	lastView    string
	lastContent string
	writes      int
}

func (c *captureDriver) Update(fn func() error) { _ = fn() }
func (c *captureDriver) SetContent(view, str string) error {
	c.lastView = view
	c.lastContent = str
	c.writes++
	return nil
}

// stubDriver supplies no-op implementations for every other GuiDriver
// method so captureDriver satisfies the interface without re-listing
// them. Embedded in captureDriver.
type stubDriver struct{}

func (stubDriver) Write(string, []byte) (int, error) { return 0, nil }
func (stubDriver) GetViewBuffer(string) string       { return "" }
func (stubDriver) SetView(string, int, int, int, int, byte) (types.View, error) {
	return nil, nil
}

func (stubDriver) SetKeybinding(string, types.Key, types.Modifier, func() error) error {
	return nil
}
func (stubDriver) SetMasterEditor(string, gocui.Editor) error        { return nil }
func (stubDriver) SetViewClickBinding(*types.ViewMouseBinding) error { return nil }
func (stubDriver) UpdateContentOnly(fn func() error)                 { _ = fn() }
func (stubDriver) SetCurrentView(string) (types.View, error)         { return nil, nil }
func (stubDriver) SetViewOnTop(string) (types.View, error)           { return nil, nil }
func (stubDriver) ViewByName(string) (types.View, error)             { return nil, nil }
func (stubDriver) DeleteView(string) error                           { return nil }
func (stubDriver) SetManager(...types.Manager)                       {}
func (stubDriver) SetCaretEnabled(bool)                              {}
func (stubDriver) SetViewCursor(string, int, int) error              { return nil }
func (stubDriver) MainLoop() error                                   { return nil }
func (stubDriver) Close() error                                      { return nil }

// helper: build a fresh context wired to the supplied fakes.
func newTestWhichKey(notifier types.WhichKeyState, rows func(types.ContextKey, []types.ChordKey) []types.ChildRow, drv types.GuiDriver) *WhichKeyContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.WHICH_KEY,
		ViewName: string(types.WHICH_KEY),
		Kind:     types.DISPLAY_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewWhichKeyContext(base, deps, notifier, rows)
}

func TestWhichKeyContext_NilNotifierNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestWhichKey(nil, nil, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times with nil notifier; want 0", drv.writes)
	}
}

func TestWhichKeyContext_NilRowsNoOps(t *testing.T) {
	drv := &captureDriver{}
	notifier := &fakeWhichKeyState{visible: true, scope: types.GLOBAL}
	c := newTestWhichKey(notifier, nil, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times with nil rows; want 0", drv.writes)
	}
}

func TestWhichKeyContext_HiddenNoOps(t *testing.T) {
	drv := &captureDriver{}
	notifier := &fakeWhichKeyState{visible: false}
	rows := func(types.ContextKey, []types.ChordKey) []types.ChildRow {
		t.Fatal("rows callback invoked when notifier reports hidden")
		return nil
	}
	c := newTestWhichKey(notifier, rows, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times when hidden; want 0", drv.writes)
	}
}

func TestWhichKeyContext_EmptyRowsNoOps(t *testing.T) {
	drv := &captureDriver{}
	notifier := &fakeWhichKeyState{visible: true}
	rows := func(types.ContextKey, []types.ChordKey) []types.ChildRow {
		return nil
	}
	c := newTestWhichKey(notifier, rows, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times for empty rows; want 0", drv.writes)
	}
}

func TestWhichKeyContext_RendersRowsAligned(t *testing.T) {
	drv := &captureDriver{}
	notifier := &fakeWhichKeyState{visible: true, scope: types.GLOBAL, prefix: []types.ChordKey{{Code: 'g'}}}
	rows := func(types.ContextKey, []types.ChordKey) []types.ChildRow {
		return []types.ChildRow{
			{Key: types.ChordKey{Code: 'a'}, Label: "alpha", IsLeaf: true},
			{Key: types.ChordKey{Special: types.KeyEsc}, Label: "escape", IsLeaf: true},
		}
	}
	c := newTestWhichKey(notifier, rows, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if drv.lastView != string(types.WHICH_KEY) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.WHICH_KEY))
	}
	lines := strings.Split(drv.lastContent, "\n")
	// dbsavvy-tro.11: body is padded to whichKeyBodyRows lines so the
	// popup rect is fully covered (defends against cells from views
	// beneath bleeding through the empty rows of the popup).
	if len(lines) != whichKeyBodyRows {
		t.Fatalf("lines = %d, want %d (padded): %q", len(lines), whichKeyBodyRows, drv.lastContent)
	}
	// Widest key is "<esc>" (5 chars). 'a' is padded to 5 chars then "  " separator.
	if !strings.HasPrefix(lines[0], "a    ") {
		t.Errorf("lines[0] = %q; expected 'a' padded to width 5", lines[0])
	}
	if !strings.Contains(lines[0], "alpha") {
		t.Errorf("lines[0] missing label: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "<esc>") {
		t.Errorf("lines[1] = %q; expected to start with <esc>", lines[1])
	}
	// Trailing padding lines must be empty.
	for i := 2; i < len(lines); i++ {
		if lines[i] != "" {
			t.Errorf("lines[%d] = %q; expected empty padding", i, lines[i])
		}
	}
}

func TestWhichKeyContext_TruncatesLongRows(t *testing.T) {
	drv := &captureDriver{}
	notifier := &fakeWhichKeyState{visible: true}
	long := strings.Repeat("x", 200)
	rows := func(types.ContextKey, []types.ChordKey) []types.ChildRow {
		return []types.ChildRow{{Key: types.ChordKey{Code: 'a'}, Label: long, IsLeaf: true}}
	}
	c := newTestWhichKey(notifier, rows, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	// Per-line truncation: every non-empty row must fit within
	// whichKeyMaxRowWidth (the padding added in dbsavvy-tro.11 emits
	// empty trailing newlines, so we measure the first content row).
	firstLine, _, _ := strings.Cut(drv.lastContent, "\n")
	if got := len(firstLine); got > whichKeyMaxRowWidth {
		t.Errorf("first-line length = %d, want <= %d", got, whichKeyMaxRowWidth)
	}
}

func TestWhichKeyContext_AddKeybindingsFnIsDropped(t *testing.T) {
	notifier := &fakeWhichKeyState{}
	c := newTestWhichKey(notifier, nil, nil)
	c.AddKeybindingsFn(func(types.KeybindingsOpts) []*types.ChordBinding {
		return []*types.ChordBinding{{Description: "should-not-appear"}}
	})
	got := c.GetKeybindings(types.KeybindingsOpts{})
	if len(got) != 0 {
		t.Fatalf("DISPLAY_CONTEXT must drop AddKeybindingsFn; got %d bindings", len(got))
	}
}

func TestWhichKeyContext_SatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &WhichKeyContext{}
}

// TestWhichKeyContext_HasRows guards the empty-rows policy seam
// (dbsavvy-tro.4): the orchestrator's layout pass calls HasRows to
// decide whether to dismiss a notifier whose prefix has no trie
// continuations. Nil resolver and empty-resolver must both report
// false; a non-empty resolver must report true.
func TestWhichKeyContext_HasRows(t *testing.T) {
	cases := []struct {
		name string
		rows func(types.ContextKey, []types.ChordKey) []types.ChildRow
		want bool
	}{
		{name: "nil resolver", rows: nil, want: false},
		{name: "empty resolver", rows: func(types.ContextKey, []types.ChordKey) []types.ChildRow { return nil }, want: false},
		{name: "one row", rows: func(types.ContextKey, []types.ChordKey) []types.ChildRow {
			return []types.ChildRow{{Key: types.ChordKey{Code: 'a'}, Label: "alpha"}}
		}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestWhichKey(&fakeWhichKeyState{}, tc.rows, nil)
			if got := c.HasRows(types.GLOBAL, nil); got != tc.want {
				t.Errorf("HasRows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWhichKeyContext_NilDriverNoOps(t *testing.T) {
	// Render must not panic when GuiDriver is nil but notifier+rows are
	// present and visible — writeView guards the nil-driver case.
	notifier := &fakeWhichKeyState{visible: true}
	rows := func(types.ContextKey, []types.ChordKey) []types.ChildRow {
		return []types.ChildRow{{Key: types.ChordKey{Code: 'a'}, Label: "a", IsLeaf: true}}
	}
	c := newTestWhichKey(notifier, rows, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}
