package ui

import (
	"fmt"
	"testing"
)

// TestOnActiveTabSetFiresOnEveryActivation: the relationship panel must repaint
// whenever the active tab changes — including the OPEN path (a jump-opened
// child tab) and the jump-list <c-o>/<c-i> SwitchToTabByID path, neither of
// which fires onActiveChanged (that callback is scoped to user Jump/Cycle focus
// reconciliation). onActiveTabSet is the broader hook that fires on every
// active-tab change so the panel can follow.
func TestOnActiveTabSetFiresOnEveryActivation(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	var fired int
	h.SetOnActiveTabSet(func() { fired++ })

	// OPEN: opening a tab makes it active -> fires.
	_ = h.openTab("a", nil)
	if fired != 1 {
		t.Fatalf("after open a: fired=%d, want 1", fired)
	}
	_ = h.openTab("b", nil)
	if fired != 2 {
		t.Fatalf("after open b: fired=%d, want 2", fired)
	}

	// JUMP (<leader>1): slot 0 -> a. Different active tab -> fires.
	fired = 0
	h.Jump(1)
	if fired != 1 {
		t.Fatalf("after Jump to a: fired=%d, want 1", fired)
	}

	// CYCLE (gt): a -> b. Fires.
	fired = 0
	h.Cycle(1)
	if fired != 1 {
		t.Fatalf("after Cycle: fired=%d, want 1", fired)
	}

	// SwitchToTabByID (<c-o>/<c-i> jump-list nav): switch back to a's id.
	a := h.Tabs()[0]
	fired = 0
	if got := h.SwitchToTabByID(fmt.Sprintf("%d", a.ID())); got == nil {
		t.Fatal("SwitchToTabByID returned nil for an open tab")
	}
	if fired != 1 {
		t.Fatalf("after SwitchToTabByID: fired=%d, want 1", fired)
	}
}

// TestOnActiveTabSetSilentWhenUnchanged: a Jump/Cycle/SwitchToTabByID that lands
// on the already-active tab does not fire (no activation change).
func TestOnActiveTabSetSilentWhenUnchanged(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	var fired int
	h.SetOnActiveTabSet(func() { fired++ })
	_ = h.openTab("a", nil) // a is now active
	fired = 0

	// Switch to the already-active tab: no change -> no fire.
	a := h.Tabs()[0]
	_ = h.SwitchToTabByID(fmt.Sprintf("%d", a.ID()))
	if fired != 0 {
		t.Fatalf("SwitchToTabByID to already-active tab fired=%d, want 0", fired)
	}
}
