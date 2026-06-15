package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/status"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// newTestConnectionManager wires a ConnectionManagerContext to the supplied
// driver, decoration hook, and row-suffix hook. Nil hooks leave the deps
// fields empty so the renderer exercises its fallback paths.
func newTestConnectionManager(
	drv types.GuiDriver,
	hook func(*models.Connection) (string, string, string),
	suffix func(*models.Connection) string,
) *ConnectionManagerContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CONNECTION_MANAGER,
		ViewName: string(types.CONNECTION_MANAGER),
		Kind:     types.MAIN_CONTEXT,
		Title:    "Connection Manager",
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	if hook != nil {
		deps.PerRowDecorationHook = hook
	}
	if suffix != nil {
		deps.RowSuffix = suffix
	}
	return NewConnectionManagerContext(base, deps)
}

// TestConnectionManagerContext_RendersRowsWithHostDbAndMarker asserts the list
// mode renders one row per connection with the active marker (icon) + the
// parsed host/db suffix (AC1).
func TestConnectionManagerContext_RendersRowsWithHostDbAndMarker(t *testing.T) {
	drv := &captureDriver{}
	hook := func(c *models.Connection) (string, string, string) {
		if c.Name == "beta" {
			return "●", c.Name, ""
		}
		return "", c.Name, ""
	}
	suffix := func(c *models.Connection) string {
		if c.Name == "beta" {
			return "db.example.com/app"
		}
		return "localhost/dev"
	}
	c := newTestConnectionManager(drv, hook, suffix)
	c.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered %d lines, want 2; body=%q", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor row alpha = %q, want '> ' prefix", lines[0])
	}
	if !strings.Contains(lines[0], "localhost/dev") {
		t.Errorf("alpha row missing host/db suffix: %q", lines[0])
	}
	if !strings.Contains(lines[1], "●") {
		t.Errorf("active row beta missing marker: %q", lines[1])
	}
	if !strings.Contains(lines[1], "db.example.com/app") {
		t.Errorf("beta row missing host/db suffix: %q", lines[1])
	}
}

// TestConnectionManagerContext_RendersRowColour asserts that when the
// decoration hook returns a recognised colour name, the row's label is wrapped
// in the matching ANSI foreground SGR + reset so the connection renders tinted.
// Rows whose colour is empty stay bare.
func TestConnectionManagerContext_RendersRowColour(t *testing.T) {
	drv := &captureDriver{}
	hook := func(c *models.Connection) (string, string, string) {
		if c.Name == "alpha" {
			return "", c.Name, "red"
		}
		return "", c.Name, ""
	}
	c := newTestConnectionManager(drv, hook, nil)
	c.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	lines := strings.Split(strings.TrimRight(drv.lastContent, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered %d lines, want 2; body=%q", len(lines), drv.lastContent)
	}
	if !strings.Contains(lines[0], "\x1b[31m") {
		t.Errorf("red row missing ANSI red SGR: %q", lines[0])
	}
	if !strings.Contains(lines[0], "\x1b[0m") {
		t.Errorf("red row missing ANSI reset: %q", lines[0])
	}
	if !strings.Contains(lines[0], "\x1b[31malpha") {
		t.Errorf("red SGR must precede the label: %q", lines[0])
	}
	if strings.Contains(lines[1], "\x1b[") {
		t.Errorf("bare row must carry no ANSI escape: %q", lines[1])
	}
}

// TestConnectionManagerContext_EmptyStateShowsAdd asserts zero connections
// render the '[a] add' empty-state body (AC4).
func TestConnectionManagerContext_EmptyStateShowsAdd(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.SetItems(nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "[a] add") {
		t.Errorf("empty-state body = %q, want '[a] add'", drv.lastContent)
	}
}

// TestConnectionManagerContext_CursorMovesWithSetCursor asserts the cursor
// marker tracks SetCursor, backing j/k nav (AC2).
func TestConnectionManagerContext_CursorMovesWithSetCursor(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
		&models.Connection{Name: "gamma"},
	})
	c.SetCursor(2)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	lines := strings.Split(strings.TrimRight(drv.lastContent, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3", len(lines))
	}
	if !strings.HasPrefix(lines[2], "> ") {
		t.Errorf("cursor=2: gamma line = %q, want '> ' prefix", lines[2])
	}
	if strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor=2: alpha must not carry the marker: %q", lines[0])
	}
}

// TestConnectionManagerContext_ConnectingMode renders the connecting / error
// body from the shared ConnectingState (AC3), mirroring ConnectingContext.
func TestConnectionManagerContext_ConnectingMode(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.SetItems([]any{&models.Connection{Name: "alpha"}})

	c.ConnectingState().SetConnectingStaged("alpha", nil)
	c.SetMode(ModeConnecting)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender connecting: %v", err)
	}
	if !strings.Contains(drv.lastContent, "Connecting to alpha") {
		t.Errorf("connecting body = %q, want connecting message", drv.lastContent)
	}

	c.ConnectingState().SetError("connection refused")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender error: %v", err)
	}
	for _, want := range []string{"connection refused", "[r] retry", "[Esc] back"} {
		if !strings.Contains(drv.lastContent, want) {
			t.Errorf("error body missing %q: %q", want, drv.lastContent)
		}
	}
}

// TestConnectionManagerContext_ConnectingGlyphFromSpinnerFrame asserts body()
// resolves the Active-stage glyph from the injected SpinnerFrame accessor and
// renders it via BodyGlyph (T3 AD5/AD6a). The seeded Active first stage must
// draw status.SpinnerGlyph(frame) for the live frame value.
func TestConnectionManagerContext_ConnectingGlyphFromSpinnerFrame(t *testing.T) {
	drv := &captureDriver{}
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CONNECTION_MANAGER,
		ViewName: string(types.CONNECTION_MANAGER),
		Kind:     types.MAIN_CONTEXT,
		Title:    "Connection Manager",
	})
	const frame = int64(3)
	deps := types.ContextTreeDeps{GuiDriver: drv, SpinnerFrame: func() int64 { return frame }}
	c := NewConnectionManagerContext(base, deps)

	c.ConnectingState().SetConnectingStaged("alpha", []Stage{
		{ID: StageAuth, Label: "Authenticated", Status: StageActive},
	})
	c.SetMode(ModeConnecting)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	wantGlyph := string(status.SpinnerGlyph(frame))
	wantLine := wantGlyph + " Authenticated"
	if !strings.Contains(drv.lastContent, wantLine) {
		t.Errorf("Active row = %q; want it to contain %q (glyph from SpinnerFrame)", drv.lastContent, wantLine)
	}
}

// TestConnectionManagerContext_ConnectingGlyphNilAccessor asserts a nil
// SpinnerFrame accessor (test fixtures / partial bootstrap) falls back to the
// frame-0 glyph without panicking (T3 AD5/AD6a).
func TestConnectionManagerContext_ConnectingGlyphNilAccessor(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil) // SpinnerFrame left nil

	c.ConnectingState().SetConnectingStaged("alpha", []Stage{
		{ID: StageAuth, Label: "Authenticated", Status: StageActive},
	})
	c.SetMode(ModeConnecting)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	wantLine := string(status.SpinnerGlyph(0)) + " Authenticated"
	if !strings.Contains(drv.lastContent, wantLine) {
		t.Errorf("Active row = %q; want frame-0 glyph fallback line %q", drv.lastContent, wantLine)
	}
}

// TestConnectionManagerContext_HandleFocusRunsOnShowInListMode asserts that
// regaining focus in list mode (a fresh re-open, or returning from a CONFIRM
// delete popup) runs the populate closure so the rows refresh (AC2 cursor-on-
// show wiring; AC3 re-open lands on the list).
func TestConnectionManagerContext_HandleFocusRunsOnShowInListMode(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	shown := 0
	c.SetOnShow(func() { shown++ })
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if c.Mode() != ModeList {
		t.Errorf("mode after focus = %v, want ModeList", c.Mode())
	}
	if shown != 1 {
		t.Errorf("onShow fired %d times, want 1", shown)
	}
}

// TestConnectionManagerContext_HandleFocusPreservesConnectingMode reproduces
// the SSH-connect bug: the SSH passphrase PROMPT popup pops
// mid-connect and returns focus to the modal in ModeConnecting. HandleFocus
// must NOT reset the mode — if it does, a subsequent dial error written to the
// ConnectingState sink is swallowed because body() renders the row list
// instead of the error. Mirrors HandleFocusPreservesFormMode.
func TestConnectionManagerContext_HandleFocusPreservesConnectingMode(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.SetItems([]any{&models.Connection{Name: "alpha"}})
	c.ConnectingState().SetConnectingStaged("alpha", nil)
	c.SetMode(ModeConnecting)
	shown := 0
	c.SetOnShow(func() { shown++ })

	// SSH passphrase PROMPT popup pops, returning focus to the modal.
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if c.Mode() != ModeConnecting {
		t.Fatalf("mode after popup return = %v, want ModeConnecting", c.Mode())
	}
	if shown != 0 {
		t.Errorf("onShow fired %d times, want 0 (connecting mode should skip refresh)", shown)
	}

	// The dial then fails; the error must render in the modal, not be swallowed.
	c.ConnectingState().SetError("ssh: handshake failed")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "ssh: handshake failed") {
		t.Fatalf("error body not rendered after popup return: %q", drv.lastContent)
	}
}

// TestConnectionManagerContext_HandleFocusPreservesFormMode asserts that when a
// child popup (PROMPT) pops and returns focus to the modal in ModeForm, the
// mode is preserved and onShow is NOT called (the form is still active).
func TestConnectionManagerContext_HandleFocusPreservesFormMode(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.OpenAddForm(nil, nil)
	if c.Mode() != ModeForm {
		t.Fatalf("precondition: mode = %v, want ModeForm", c.Mode())
	}
	shown := 0
	c.SetOnShow(func() { shown++ })
	if err := c.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus: %v", err)
	}
	if c.Mode() != ModeForm {
		t.Errorf("mode after focus = %v, want ModeForm", c.Mode())
	}
	if shown != 0 {
		t.Errorf("onShow fired %d times, want 0 (form mode should skip refresh)", shown)
	}
}

// TestConnectionManagerContext_NilGuiDriverNoPanic asserts HandleRender is
// safe when no driver is wired (test wiring / partial bootstrap).
func TestConnectionManagerContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestConnectionManager(nil, nil, nil)
	c.SetItems([]any{&models.Connection{Name: "alpha"}})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender nil driver: %v", err)
	}
}

// TestConnectionManagerContext_Kind locks the MAIN_CONTEXT kind so the layout
// pass slots it into dims["main"].
func TestConnectionManagerContext_Kind(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	if got := c.GetKind(); got != types.MAIN_CONTEXT {
		t.Fatalf("GetKind() = %v, want MAIN_CONTEXT", got)
	}
	if got := c.GetKey(); got != types.CONNECTION_MANAGER {
		t.Fatalf("GetKey() = %q, want %q", got, types.CONNECTION_MANAGER)
	}
}

// TestConnectionManagerContext_OptionsBarFilterHidesFieldEditInListMode locks
// the field-edit fix plus the rebind: in ModeList the status bar
// must NOT advertise the now-unbound ConnectionManagerFieldEdit, while the
// list-appropriate actions stay visible. After the `e`→`i` rebind the single
// edit action is ConnectionManagerEdit, so ModeForm advertises it (the `i`
// "Edit connection/field" binding) and hides the dead ConnectionManagerFieldEdit.
func TestConnectionManagerContext_OptionsBarFilterHidesFieldEditInListMode(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	if c.Mode() != ModeList {
		t.Fatalf("precondition: mode = %v, want ModeList", c.Mode())
	}

	listFilter := c.OptionsBarFilter()
	if listFilter == nil {
		t.Fatal("ModeList OptionsBarFilter() = nil, want a predicate that hides form-only actions")
	}
	if listFilter(commands.ConnectionManagerFieldEdit) {
		t.Error("ModeList shows ConnectionManagerFieldEdit, want it hidden")
	}
	for _, id := range []string{
		commands.ConnectionManagerConfirm,
		commands.ConnectionManagerClose,
		commands.ConnectionManagerAdd,
		commands.ConnectionManagerEdit,
		commands.ConnectionManagerDelete,
	} {
		if !listFilter(id) {
			t.Errorf("ModeList hides %q, want it visible", id)
		}
	}

	c.OpenAddForm(nil, nil)
	if c.Mode() != ModeForm {
		t.Fatalf("precondition: mode = %v, want ModeForm", c.Mode())
	}
	formFilter := c.OptionsBarFilter()
	if formFilter == nil || !formFilter(commands.ConnectionManagerEdit) {
		t.Error("ModeForm hides ConnectionManagerEdit, want it visible (the `i` edit binding)")
	}
	if formFilter != nil && formFilter(commands.ConnectionManagerFieldEdit) {
		t.Error("ModeForm shows ConnectionManagerFieldEdit, want it hidden (now unbound)")
	}
}
