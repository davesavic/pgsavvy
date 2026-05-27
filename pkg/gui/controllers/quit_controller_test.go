package controllers_test

import (
	"errors"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// AC: <c-c> dispatches commands.AppQuit which returns gocui.ErrQuit.
func TestQuitControllerCtrlCReturnsErrQuit(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag.CoreDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	found := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if len(kb.Sequence) == 1 && kb.Sequence[0].Code == 'c' && kb.Sequence[0].Mod == types.ChordModCtrl {
			found = true
			if kb.ActionID != commands.AppQuit {
				t.Fatalf("<c-c> ActionID = %q, want %q", kb.ActionID, commands.AppQuit)
			}
			if err := invokeAction(reg, kb); !errors.Is(err, gocui.ErrQuit) {
				t.Fatalf("AppQuit handler returned %v, want gocui.ErrQuit", err)
			}
		}
	}
	if !found {
		t.Fatal("QuitController did not publish a <c-c> binding")
	}
}

// AC: ? binding dispatches commands.HelpCheatsheet (registered as a
// stub by the orchestrator; controller only publishes the binding).
func TestQuitControllerQuestionMarkPublishesHelpCheatsheet(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag.CoreDeps)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(kb, '?') {
			if kb.ActionID != commands.HelpCheatsheet {
				t.Fatalf("? ActionID = %q, want %q", kb.ActionID, commands.HelpCheatsheet)
			}
			return
		}
	}
	t.Fatal("QuitController did not publish a ? binding")
}

// RegisterActions populates commands.AppQuit.
func TestQuitControllerRegisterActionsPopulatesAppQuit(t *testing.T) {
	b := newBag()
	ctrl := controllers.NewQuitController(nil, b.HelperBag.CoreDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, ok := reg.Get(commands.AppQuit)
	if !ok || cmd == nil {
		t.Fatalf("commands.AppQuit not registered after RegisterActions")
	}
	if err := cmd.Handler(commands.ExecCtx{}); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("AppQuit handler returned %v, want gocui.ErrQuit", err)
	}
}
