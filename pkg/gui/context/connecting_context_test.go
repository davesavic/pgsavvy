package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

func newTestConnecting(drv types.GuiDriver) *ConnectingContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.CONNECTING,
		ViewName: string(types.CONNECTING),
		Kind:     types.MAIN_CONTEXT,
		Title:    "Connecting",
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewConnectingContext(base, deps)
}

// TestConnectingContext_RendersConnectingState asserts the connecting
// state renders "Connecting to <name>…" into the CONNECTING view.
func TestConnectingContext_RendersConnectingState(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnecting(drv)
	c.SetConnecting("local-pg")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("SetContent calls = %d, want 1", drv.writes)
	}
	if drv.lastView != string(types.CONNECTING) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.CONNECTING))
	}
	if !strings.Contains(drv.lastContent, "Connecting to local-pg") {
		t.Errorf("content missing connecting message: %q", drv.lastContent)
	}
}

// TestConnectingContext_RendersErrorState asserts the error state renders
// the error message plus the retry/back hints.
func TestConnectingContext_RendersErrorState(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnecting(drv)
	c.SetError("connection refused")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	for _, want := range []string{"connection refused", "[r] retry", "[Esc] back"} {
		if !strings.Contains(drv.lastContent, want) {
			t.Errorf("error content missing %q: %q", want, drv.lastContent)
		}
	}
}

// TestConnectingContext_ErrorThenConnectingClearsError asserts SetConnecting
// drops a previously-set error so the error hints don't leak into a
// subsequent connecting render.
func TestConnectingContext_ErrorThenConnectingClearsError(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnecting(drv)
	c.SetError("connection refused")
	c.SetConnecting("local-pg")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, "connection refused") {
		t.Errorf("connecting render still shows stale error: %q", drv.lastContent)
	}
	if !strings.Contains(drv.lastContent, "Connecting to local-pg") {
		t.Errorf("content missing connecting message: %q", drv.lastContent)
	}
}

// TestConnectingContext_NilGuiDriverNoPanic asserts HandleRender is safe
// when no driver is wired (test wiring / partial bootstrap).
func TestConnectingContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestConnecting(nil)
	c.SetConnecting("local-pg")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

// TestConnectingContext_EmptyNameNoPanic asserts an empty profile name
// renders a well-formed body without crashing.
func TestConnectingContext_EmptyNameNoPanic(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnecting(drv)
	c.SetConnecting("")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender empty name: %v", err)
	}
	if !strings.Contains(drv.lastContent, "Connecting to") {
		t.Errorf("content missing connecting prefix: %q", drv.lastContent)
	}
}

// TestConnectingContext_Kind locks the MAIN_CONTEXT kind so the layout
// pass slots it into dims["main"].
func TestConnectingContext_Kind(t *testing.T) {
	c := newTestConnecting(&captureDriver{})
	if got := c.GetKind(); got != types.MAIN_CONTEXT {
		t.Fatalf("GetKind() = %v, want MAIN_CONTEXT", got)
	}
	if got := c.GetKey(); got != types.CONNECTING {
		t.Fatalf("GetKey() = %q, want %q", got, types.CONNECTING)
	}
}
