package controllers_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// AC: bare `q` quits cleanly (returns gocui.ErrQuit).
func TestQuitControllerQReturnsErrQuit(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, 'q') {
			if err := kb.Handler(); !errors.Is(err, gocui.ErrQuit) {
				t.Fatalf("q handler returned %v, want gocui.ErrQuit", err)
			}
		}
	}
}

// AC: `:q` invokes Quit via the colon-armed OneshotArmer with suffixes
// map containing q→Quit, scope=GLOBAL.
func TestQuitControllerColonArmsOneShotWithQSuffix(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, ':') {
			if err := kb.Handler(); err != nil {
				t.Fatalf("colon: %v", err)
			}
		}
	}
	if len(b.OneShot.calls) != 1 {
		t.Fatalf("OneShot.Arm calls = %d, want 1", len(b.OneShot.calls))
	}
	got := b.OneShot.calls[0]
	if got.Prefix != ":" {
		t.Fatalf("Arm.prefix = %q, want \":\"", got.Prefix)
	}
	if got.Scope != string(types.GLOBAL) {
		t.Fatalf("Arm.scope = %q, want %q", got.Scope, string(types.GLOBAL))
	}
	qSuffix, ok := got.Suffixes['q']
	if !ok {
		t.Fatalf("Arm.suffixes missing 'q'; got %v", got.Suffixes)
	}
	// Invoking the q suffix MUST return gocui.ErrQuit.
	if err := qSuffix(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("q suffix returned %v, want ErrQuit", err)
	}
}

// AC: `?` opens MENU via MenuPushHelper.
func TestQuitControllerQuestionMarkOpensMenu(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, '?') {
			if err := kb.Handler(); err != nil {
				t.Fatalf("?: %v", err)
			}
		}
	}
	if b.Menu.pushed != 1 {
		t.Fatalf("Menu.PushMenu called %d times, want 1", b.Menu.pushed)
	}
}

// Edge from AC list: ":" then a non-"q" key closes the colon-prefix
// without action. The OneshotArmer interface's contract (T7b) handles
// this — here we verify that the controller arms with a suffix map
// containing ONLY q, so any other key is implicitly an "unknown
// suffix" handled by the dispatcher.
func TestQuitControllerColonArmsOnlyQSuffix(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, ':') {
			_ = kb.Handler()
		}
	}
	if len(b.OneShot.calls) != 1 {
		t.Fatalf("expected one Arm call")
	}
	suffixes := b.OneShot.calls[0].Suffixes
	if len(suffixes) != 1 {
		t.Fatalf("colon arm should declare exactly 1 suffix (q); got %d entries: %v", len(suffixes), suffixes)
	}
	if _, ok := suffixes['q']; !ok {
		t.Fatalf("colon arm must declare q→Quit suffix")
	}
}
