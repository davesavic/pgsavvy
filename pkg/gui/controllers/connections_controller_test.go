package controllers_test

import (
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestConnectionsControllerBindingsShape(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})

	// Expect at least: j, k, <CR>, a, plus 5 rail-switch bindings.
	want := map[string]bool{
		"j":       false,
		"k":       false,
		"a":       false,
		"<enter>": false,
		"<tab>":   false,
		"1":       false,
		"2":       false,
		"3":       false,
		"4":       false,
	}
	for _, kb := range bindings {
		if kb.Key.Equals(gocui.NewKeyRune('j')) {
			want["j"] = true
		}
		if kb.Key.Equals(gocui.NewKeyRune('k')) {
			want["k"] = true
		}
		if kb.Key.Equals(gocui.NewKeyRune('a')) {
			want["a"] = true
		}
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEnter)) {
			want["<enter>"] = true
		}
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyTab)) {
			want["<tab>"] = true
		}
		for _, d := range []rune{'1', '2', '3', '4'} {
			if kb.Key.Equals(gocui.NewKeyRune(d)) {
				want[string(d)] = true
			}
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("missing binding for key %q", k)
		}
	}
}

// AC: connections_controller `<CR>` → ConnectHelper.Connect.
func TestConnectionsControllerConfirmCallsConnect(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	profile := &models.Connection{Name: "local", Driver: "pg"}
	b.ConnPicker.sel = profile

	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	var confirm func() error
	for _, kb := range bindings {
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEnter)) {
			confirm = kb.Handler
		}
	}
	if confirm == nil {
		t.Fatal("no <enter> binding")
	}
	if err := confirm(); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(b.Connect.calls) != 1 || b.Connect.calls[0] != profile {
		t.Fatalf("Connect called=%v, want 1 with profile pointer", b.Connect.calls)
	}
}

// Edge: <CR> with no profile under cursor is a no-op (no Connect call).
func TestConnectionsControllerConfirmEmptyRailNoop(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	for _, kb := range bindings {
		if kb.Key.Equals(gocui.NewKeyName(gocui.KeyEnter)) {
			_ = kb.Handler()
		}
	}
	if len(b.Connect.calls) != 0 {
		t.Fatalf("Connect called %d times on empty rail; want 0", len(b.Connect.calls))
	}
}

// AC: connections_controller `a` → ConnectionFormHelper.WalkAddConnection.
func TestConnectionsControllerAddCallsConnectionForm(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
	var add func() error
	for _, kb := range bindings {
		if kb.Key.Equals(gocui.NewKeyRune('a')) {
			add = kb.Handler
		}
	}
	if add == nil {
		t.Fatal("no `a` binding")
	}
	if err := add(); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !b.ConnForm.called {
		t.Fatal("ConnectionForm.WalkAdd not invoked")
	}
}

// Edge from AC list: press `a` on CONNECTIONS when rail is non-empty:
// still allowed.
func TestConnectionsControllerAddAllowedWithSelection(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "x"}
	cur := &fakeCursor{idx: 0, items: []any{b.ConnPicker.sel}}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Key.Equals(gocui.NewKeyRune('a')) {
			_ = kb.Handler()
		}
	}
	if !b.ConnForm.called {
		t.Fatal("WalkAdd MUST run even when the rail has a selected connection (AC)")
	}
}

func TestConnectionsControllerDescriptionsSourceFromTrActions(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag, cur, b.ConnPicker)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Description == "" {
			t.Fatalf("empty Description on binding key=%v (M11i: must source from Tr.Actions.*)", kb.Key)
		}
	}
}
