package controllers_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestConnectionsControllerBindingsShape(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})

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
		if isRune(kb, 'j') {
			want["j"] = true
		}
		if isRune(kb, 'k') {
			want["k"] = true
		}
		if isRune(kb, 'a') {
			want["a"] = true
		}
		if isSpecial(kb, types.KeyEnter) {
			want["<enter>"] = true
		}
		if isSpecial(kb, types.KeyTab) {
			want["<tab>"] = true
		}
		for _, d := range []rune{'1', '2', '3', '4'} {
			if isRune(kb, d) {
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

// AC epic dbsavvy-e53.5: connections_controller `<CR>` hands the selected
// profile to OnBeginConnecting (the orchestrator's CONNECTING seam, which
// starts the dial). The controller no longer dials directly.
func TestConnectionsControllerConfirmCallsConnect(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	profile := &models.Connection{Name: "local", Driver: "pg"}
	b.ConnPicker.sel = profile

	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("confirm: %v", err)
			}
		}
	}
	if got := b.BeginConnectingNames; len(got) != 1 || got[0] != "local" {
		t.Fatalf("OnBeginConnecting calls = %v; want exactly [\"local\"]", got)
	}
}

// Edge: <CR> with no profile under cursor is a no-op (no OnBeginConnecting).
func TestConnectionsControllerConfirmEmptyRailNoop(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			_ = invokeAction(reg, kb)
		}
	}
	if len(b.BeginConnectingNames) != 0 {
		t.Fatalf("OnBeginConnecting called %d times on empty rail; want 0", len(b.BeginConnectingNames))
	}
}

// AC: connections_controller `a` → ConnectionFormHelper.WalkAddConnection.
func TestConnectionsControllerAddCallsConnectionForm(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'a') {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("add: %v", err)
			}
		}
	}
	if !b.ConnForm.called {
		t.Fatal("ConnectionForm.WalkAdd not invoked")
	}
}

// Edge: `a` on CONNECTIONS with non-empty rail still allowed.
func TestConnectionsControllerAddAllowedWithSelection(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "x"}
	cur := &fakeCursor{idx: 0, items: []any{b.ConnPicker.sel}}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'a') {
			_ = invokeAction(reg, kb)
		}
	}
	if !b.ConnForm.called {
		t.Fatal("WalkAdd MUST run even when the rail has a selected connection (AC)")
	}
}

// AC dbsavvy-a07 / epic dbsavvy-e53: <CR> MUST NOT propagate an error (which
// would crash gocui's MainLoop) and MUST NOT route through the old keyed
// "connect" toast slot — error feedback now lives on the CONNECTING screen
// (the connectInvoker routes it to Connecting.SetError; see the orchestrator
// adapters test). The controller's only job is to hand the profile to
// OnBeginConnecting; the dial + error routing live in the orchestrator.
func TestConnectionsControllerConfirmConnectErrIsSwallowedNoToast(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	b.ConnPicker.sel = &models.Connection{Name: "p", Driver: "pg"}

	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("confirm propagated an error (would crash MainLoop): %v", err)
			}
		}
	}
	// CONNECTING screen seeded with the profile name.
	if got := b.BeginConnectingNames; len(got) != 1 || got[0] != "p" {
		t.Fatalf("OnBeginConnecting calls = %v; want exactly [\"p\"]", got)
	}
	// No connect-keyed toast: error feedback no longer rides the toast lane.
	for _, u := range b.Toast.updates {
		if u.Key == "connect" {
			t.Fatalf("unexpected connect-keyed toast %+v; error feedback must use the CONNECTING screen", u)
		}
	}
}

// AC dbsavvy-e9i / epic dbsavvy-e53: re-activating an already-open profile
// MUST NOT crash. The friendly "already connected" rewrite now surfaces via
// the CONNECTING screen's error state (asserted in the orchestrator adapters
// test); here we assert the controller swallows, seeds CONNECTING, and emits
// no connect-keyed toast.
func TestConnectionsControllerConfirmAlreadyConnectedNoCrash(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	b.ConnPicker.sel = &models.Connection{Name: "p", Driver: "pg"}

	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("confirm propagated an error (would crash): %v", err)
			}
		}
	}
	if got := b.BeginConnectingNames; len(got) != 1 || got[0] != "p" {
		t.Fatalf("OnBeginConnecting calls = %v; want exactly [\"p\"]", got)
	}
	for _, u := range b.Toast.updates {
		if u.Key == "connect" {
			t.Fatalf("unexpected connect-keyed toast %+v; already-connected must use the CONNECTING screen", u)
		}
	}
}

func TestConnectionsControllerDescriptionsSourceFromTrActions(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Description == "" {
			t.Fatalf("empty Description on binding sequence=%v (M11i: must source from Tr.Actions.*)", kb.Sequence)
		}
	}
}

// confirmConnections drives the CONNECTIONS <CR> handler once.
func confirmConnections(t *testing.T, b *bag) {
	t.Helper()
	cur := &fakeCursor{}
	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("confirm: %v", err)
			}
		}
	}
}

// AC epic dbsavvy-e53.5: activating a connection no longer dials directly
// from the controller — the dial (and its worker dispatch) moved into the
// orchestrator's startAttempt behind OnBeginConnecting. The controller must
// neither call Connect nor dispatch a worker on the <CR> path.
func TestConnectionsControllerConfirmDoesNotDialDirectly(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "local", Driver: "pg"}

	confirmConnections(t, b)

	if b.WorkerCalls != 0 {
		t.Fatalf("OnWorker calls = %d; want 0 (the dial moved into the orchestrator)", b.WorkerCalls)
	}
	if len(b.Connect.calls) != 0 {
		t.Fatalf("Connect calls = %d; want 0 (controller hands off to OnBeginConnecting)", len(b.Connect.calls))
	}
	if got := b.BeginConnectingNames; len(got) != 1 || got[0] != "local" {
		t.Fatalf("OnBeginConnecting calls = %v; want exactly [\"local\"]", got)
	}
}

// AC epic dbsavvy-e53: a successful confirm seeds the CONNECTING screen
// (with the profile name) and emits NO connect-keyed toast. The actual push
// of Schemas/Tables (auto-removing CONNECTING) lives in the orchestrator
// layer; from the controller's seam we observe only the OnBeginConnecting
// hand-off and the absence of any toast-lane feedback.
func TestConnectionsControllerConfirmSuccessPushesConnectingNoToast(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "local", Driver: "pg"}

	confirmConnections(t, b)

	if got := b.BeginConnectingNames; len(got) != 1 || got[0] != "local" {
		t.Fatalf("OnBeginConnecting calls = %v; want exactly [\"local\"] (CONNECTING seeded + named)", got)
	}
	for _, u := range b.Toast.updates {
		if u.Key == "connect" {
			t.Fatalf("unexpected connect-keyed toast %+v; the CONNECTING screen replaces the toast lifecycle", u)
		}
	}
}
