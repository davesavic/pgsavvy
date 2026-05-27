package controllers_test

import (
	"errors"
	"strings"
	"testing"
	"time"

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

// AC: connections_controller `<CR>` → ConnectHelper.Connect.
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
	if len(b.Connect.calls) != 1 || b.Connect.calls[0] != profile {
		t.Fatalf("Connect called=%v, want 1 with profile pointer", b.Connect.calls)
	}
}

// Edge: <CR> with no profile under cursor is a no-op (no Connect call).
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
	if len(b.Connect.calls) != 0 {
		t.Fatalf("Connect called %d times on empty rail; want 0", len(b.Connect.calls))
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

// AC dbsavvy-a07: a Connect error MUST NOT propagate up (which would
// crash gocui's MainLoop). Instead the controller surfaces a toast and
// returns nil.
func TestConnectionsControllerConfirmConvertsConnectErrToToast(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	b.ConnPicker.sel = &models.Connection{Name: "p", Driver: "pg"}
	b.Connect.err = errors.New("session: interactive password prompt not supported in TUI mode; configure password_command, keyring, or pgpass (hint: password for localhost)")

	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("confirm propagated Connect error (would crash MainLoop): %v", err)
			}
		}
	}
	// Post-dbsavvy-fow.1 the toast routes through the keyed "connect"
	// slot: a "Connecting…" emit then an error replacement, both under
	// connectToastKey. The last update carries the surfaced error text.
	if len(b.Toast.updates) != 2 {
		t.Fatalf("Toast.ShowOrUpdate calls = %d; want 2 (connecting + error)", len(b.Toast.updates))
	}
	last := b.Toast.updates[len(b.Toast.updates)-1]
	if last.Key != "connect" {
		t.Fatalf("error toast key = %q; want %q", last.Key, "connect")
	}
	if !strings.Contains(last.Msg, "interactive password prompt not supported") {
		t.Fatalf("toast text = %q; want it to surface the prompt-not-supported error", last.Msg)
	}
}

// AC dbsavvy-e9i: "already connected" MUST be a toast (or no-op),
// never a crash. We assert the toast text is the friendlier short
// rewrite (not the raw "data: …" string).
func TestConnectionsControllerConfirmAlreadyConnectedIsToastNotCrash(t *testing.T) {
	b := newBag()
	cur := &fakeCursor{}
	b.ConnPicker.sel = &models.Connection{Name: "p", Driver: "pg"}
	b.Connect.err = errors.New("data: already connected (call Disconnect first)")

	ctrl := controllers.NewConnectionsController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.ThreadingDeps, cur, b.ConnPicker)
	reg := commands.NewRegistry()
	ctrl.ListControllerTrait.RegisterActions(reg)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isSpecial(kb, types.KeyEnter) {
			if err := invokeAction(reg, kb); err != nil {
				t.Fatalf("confirm propagated 'already connected' (would crash): %v", err)
			}
		}
	}
	// Keyed "connect" slot: connecting emit then the friendly error
	// rewrite replacement (dbsavvy-fow.1).
	if len(b.Toast.updates) != 2 {
		t.Fatalf("Toast.ShowOrUpdate calls = %d; want 2 (connecting + error)", len(b.Toast.updates))
	}
	last := b.Toast.updates[len(b.Toast.updates)-1]
	if last.Key != "connect" {
		t.Fatalf("error toast key = %q; want %q", last.Key, "connect")
	}
	if last.Msg != "already connected" {
		t.Fatalf("toast text = %q; want the friendly 'already connected'", last.Msg)
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

// AC dbsavvy-fow.1: activating a connection dispatches Connect via
// OnWorker (off the UI thread) rather than running it inline on the
// dispatch path.
func TestConnectionsControllerConfirmDispatchesViaOnWorker(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "local", Driver: "pg"}

	confirmConnections(t, b)

	if b.WorkerCalls != 1 {
		t.Fatalf("OnWorker calls = %d; want 1 (connect must run off the UI thread)", b.WorkerCalls)
	}
	if len(b.Connect.calls) != 1 {
		t.Fatalf("Connect calls = %d; want 1", len(b.Connect.calls))
	}
}

// AC dbsavvy-fow.1: the ctx handed to Connect carries a ~10s timeout
// (non-zero Deadline) covering dial + Ping + version().
func TestConnectionsControllerConfirmConnectCtxHasDeadline(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "local", Driver: "pg"}

	confirmConnections(t, b)

	if !b.Connect.hasDL {
		t.Fatal("Connect ctx had no Deadline; want a ~10s timeout (single-sourced)")
	}
	until := time.Until(b.Connect.deadline)
	if until <= 0 || until > 11*time.Second {
		t.Fatalf("Connect ctx deadline in %v; want a positive value <= ~10s", until)
	}
}

// AC dbsavvy-fow.1: a successful connect clears the keyed "Connecting…"
// toast (emitting connecting, then the empty clear, both under the
// "connect" key).
func TestConnectionsControllerConfirmSuccessClearsConnectingToast(t *testing.T) {
	b := newBag()
	b.ConnPicker.sel = &models.Connection{Name: "local", Driver: "pg"}

	confirmConnections(t, b)

	if len(b.Toast.updates) != 2 {
		t.Fatalf("Toast.ShowOrUpdate calls = %d; want 2 (connecting + clear)", len(b.Toast.updates))
	}
	first := b.Toast.updates[0]
	if first.Key != "connect" || first.Msg == "" {
		t.Fatalf("first update = %+v; want a non-empty connecting toast keyed 'connect'", first)
	}
	if !strings.Contains(first.Msg, "local") {
		t.Fatalf("connecting toast %q; want it to name the connection", first.Msg)
	}
	clear := b.Toast.updates[1]
	if clear.Key != "connect" || clear.Msg != "" {
		t.Fatalf("clear update = %+v; want an empty-message clear keyed 'connect'", clear)
	}
}
