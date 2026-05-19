package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// newTestConnections wires a ConnectionsContext to the supplied driver
// and decoration hook. Mirrors the helper patterns used by sibling
// context tests (selection / whichkey). A nil hook leaves the deps
// field empty so the renderer exercises its fallback path.
func newTestConnections(drv types.GuiDriver, hook func(*models.Connection) (string, string, string)) *ConnectionsContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CONNECTIONS,
		ViewName: string(types.CONNECTIONS),
		Kind:     types.SIDE_CONTEXT,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	if hook != nil {
		deps.PerRowDecorationHook = hook
	}
	return NewConnectionsContext(base, deps)
}

// TestConnectionsContext_CursorMarkerMovesWithCursor guards dbsavvy-sig:
// the cursor row must render with a visible "> " marker; non-cursor
// rows get "  " padding so columns line up and j/k visibly moves the
// indicator. The marker is text — it persists regardless of whether
// the rail is focused (focus is communicated separately via the rail
// frame colour).
func TestConnectionsContext_CursorMarkerMovesWithCursor(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnections(drv, nil)
	c.SetItems([]any{
		&models.Connection{Name: "alpha"},
		&models.Connection{Name: "beta"},
		&models.Connection{Name: "gamma"},
	})
	// Default cursor is 0 — "alpha" should carry "> ".
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes == 0 {
		t.Fatal("HandleRender wrote nothing; expected rows to be rendered")
	}
	body := drv.lastContent
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3; body=%q", len(lines), body)
	}
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor=0: alpha line = %q, want '> ' prefix", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  ") || strings.HasPrefix(lines[1], "> ") {
		t.Errorf("cursor=0: beta line = %q, want '  ' prefix (not '> ')", lines[1])
	}
	if !strings.HasPrefix(lines[2], "  ") || strings.HasPrefix(lines[2], "> ") {
		t.Errorf("cursor=0: gamma line = %q, want '  ' prefix (not '> ')", lines[2])
	}

	// Move cursor to row 2 ("gamma") — marker must follow.
	c.SetCursor(2)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (cursor=2): %v", err)
	}
	body = drv.lastContent
	lines = strings.Split(strings.TrimRight(body, "\n"), "\n")
	if strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor=2: alpha line = %q, must not have '> ' prefix", lines[0])
	}
	if !strings.HasPrefix(lines[2], "> ") {
		t.Errorf("cursor=2: gamma line = %q, want '> ' prefix", lines[2])
	}
}

// TestConnectionsContext_RowLabelUsesProfileName guards dbsavvy-2ox:
// the rail label must be Profile.Name, not Profile.Label. A profile
// with name='local-pg' and label='localhost' (e.g. DSN-host derived)
// must render as 'local-pg' so two profiles sharing a host stay
// distinguishable.
func TestConnectionsContext_RowLabelUsesProfileName(t *testing.T) {
	drv := &captureDriver{}
	// Wire the production decoration hook indirectly by providing a
	// stand-in that returns Name (mirroring presentation.NewPerRowDecorationHook
	// post-fix). This keeps the test independent of the presentation
	// package while still asserting the contract the renderer relies on.
	hook := func(conn *models.Connection) (string, string, string) {
		if conn == nil {
			return "", "", ""
		}
		return conn.Icon, conn.Name, conn.Color
	}
	c := newTestConnections(drv, hook)
	c.SetItems([]any{
		&models.Connection{Name: "local-pg", Label: "localhost"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	if !strings.Contains(body, "local-pg") {
		t.Errorf("body = %q, must contain 'local-pg'", body)
	}
	// Two profiles with same Label but different Names must be distinct.
	c.SetItems([]any{
		&models.Connection{Name: "local-pg", Label: "localhost"},
		&models.Connection{Name: "prod-pg", Label: "localhost"},
	})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender (two-row): %v", err)
	}
	body = drv.lastContent
	if !strings.Contains(body, "local-pg") || !strings.Contains(body, "prod-pg") {
		t.Errorf("body = %q, must contain both 'local-pg' and 'prod-pg'", body)
	}
}
