package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"
)

func reverseOpenEntries() []controllers.ReverseEntry {
	return []controllers.ReverseEntry{{
		FK:       models.ForeignKey{Schema: "app", Table: "orders", Columns: []string{"user_id"}, RefColumns: []string{"id"}},
		PKValues: []any{int64(7)}, Reltuples: 12,
	}}
}

func reverseOpenFixture(t *testing.T) (*Gui, *guicontext.FKReversePickerContext) {
	t.Helper()
	tree := gui.NewContextTree()
	pushRootCh(t, tree)
	picker := guicontext.NewFKReversePickerContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key: types.FK_REVERSE_PICKER, ViewName: string(types.FK_REVERSE_PICKER), Kind: types.TEMPORARY_POPUP,
	}), guicontext.Deps{})
	g := &Gui{tree: tree, registry: &guicontext.ContextTree{FKReversePicker: picker}, controllers: &controllers.Controllers{}}
	g.controllers.FKReversePicker = controllers.NewFKReversePickerController(nil, controllers.CoreDeps{}, controllers.FKReversePickerDeps{
		Context: picker, Tree: tree,
	})
	return g, picker
}

func TestReview_reverse_fk_wired(t *testing.T) {
	t.Run("open_reopen_close", func(t *testing.T) {
		g, picker := reverseOpenFixture(t)
		controller := g.controllers.FKReversePicker
		g.controllers.FKReversePicker = nil
		open := g.wireEditDeps().OpenFKReversePicker
		g.controllers.FKReversePicker = controller
		pushes := 0
		g.tree.RegisterSwapHook(func() { pushes++ })
		for range 2 {
			if !open(reverseOpenEntries(), nil, 5, 3) {
				t.Fatal("wired callback rejected valid entries")
			}
			if g.tree.Current() != picker || len(g.tree.Stack()) != 2 || pushes != 1 {
				t.Fatalf("current=%v depth=%d pushes=%d; want picker,2,1", g.tree.Current().GetKey(), len(g.tree.Stack()), pushes)
			}
		}
		if err := controller.Close(commands.ExecCtx{}); err != nil {
			t.Fatal(err)
		}
		if len(g.tree.Stack()) != 1 || g.tree.Current() == picker {
			t.Fatal("close did not restore root")
		}
		if !open(reverseOpenEntries(), nil, 0, 0) || len(g.tree.Stack()) != 2 || g.tree.Current() != picker {
			t.Fatal("reopen after close did not install exactly one picker")
		}
	})
	for _, missing := range []string{"empty_entries", "controller", "context"} {
		t.Run(missing, func(t *testing.T) {
			g, _ := reverseOpenFixture(t)
			entries := reverseOpenEntries()
			switch missing {
			case "empty_entries":
				entries = nil
			case "controller":
				g.controllers.FKReversePicker = nil
			case "context":
				g.controllers.FKReversePicker = controllers.NewFKReversePickerController(nil, controllers.CoreDeps{}, controllers.FKReversePickerDeps{})
			}
			if g.wireEditDeps().OpenFKReversePicker(entries, nil, 0, 0) || len(g.tree.Stack()) != 1 {
				t.Fatal("failed preparation must return false without pushing")
			}
		})
	}
}

type reverseOpenFocusLeaf struct {
	guicontext.BaseContext
	err   error
	calls int
}

func (c *reverseOpenFocusLeaf) HandleFocus(types.OnFocusOpts) error {
	c.calls++
	return c.err
}

type reverseOpenDisplacedPopup struct {
	guicontext.BaseContext
	picker *guicontext.FKReversePickerContext
	leaf   *reverseOpenFocusLeaf
}

func (c *reverseOpenDisplacedPopup) HandleFocusLost(types.OnFocusLostOpts) error {
	// Push invokes this hook after Open prepares the real leaves, but before
	// picker focus. Inject failure here without introducing a production seam.
	c.picker.SetLeaves(c.leaf)
	return nil
}

func TestReview_reverse_fk_push_failure(t *testing.T) {
	g, picker := reverseOpenFixture(t)
	leaf := &reverseOpenFocusLeaf{err: errors.New("synthetic focus failure")}
	displaced := &reverseOpenDisplacedPopup{
		BaseContext: guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.PROMPT, Kind: types.TEMPORARY_POPUP}),
		picker:      picker, leaf: leaf,
	}
	if err := g.tree.Push(displaced); err != nil {
		t.Fatal(err)
	}
	if g.wireEditDeps().OpenFKReversePicker(reverseOpenEntries(), nil, 0, 0) {
		t.Fatal("wired callback reported success despite Push focus error")
	}
	if leaf.calls != 1 {
		t.Fatalf("focus calls=%d; want one failing Push", leaf.calls)
	}
	// ContextTree retains its entry on focus failure; the callback must not
	// change that contract or multiply the entry on subsequent preparation.
	if !g.wireEditDeps().OpenFKReversePicker(reverseOpenEntries(), nil, 0, 0) || len(g.tree.Stack()) != 2 {
		t.Fatal("retry must not duplicate the retained picker")
	}
}

func TestReview_reverse_fk_native(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_reverse_fk_native_process$", "-test.v")
	cmd.Env = append(os.Environ(), "PGSAVVY_REVERSE_FK_PROBE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native probe: %v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestReview_reverse_fk_native_process(t *testing.T) {
	if os.Getenv("PGSAVVY_REVERSE_FK_PROBE") != "1" {
		return
	}
	ng, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 130, Height: 42})
	if err != nil {
		t.Fatal(err)
	}
	fs := afero.NewMemMapFs()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	g := NewGui(Deps{Common: c, Store: common.NewAppStateStore(fs, "/state.yml", common.DefaultClock()), ConnectionsProvider: func() []models.Connection { return nil }})
	if err := g.UseDriverForTest(newGocuiDriver(ng)); err != nil {
		ng.Close()
		t.Fatal(err)
	}
	defer func() {
		if err := g.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	ng.SetManagerFunc(func(*gocui.Gui) error {
		if !g.wireEditDeps().OpenFKReversePicker(reverseOpenEntries(), nil, 0, 0) {
			return errors.New("native callback rejected entries")
		}
		if err := g.RunLayout(130, 42); err != nil {
			return err
		}
		name := g.registry.FKReversePicker.GetViewName()
		v, err := ng.View(name)
		if err != nil {
			return fmt.Errorf("picker not displayed: %w", err)
		}
		if ng.CurrentView() != v || !strings.Contains(v.Buffer(), "app.orders(user_id)") || !strings.Contains(v.Buffer(), "~12 rows") {
			return fmt.Errorf("picker focus/body mismatch: %q", v.Buffer())
		}
		t.Logf("native focus=%s body=%q", name, v.Buffer())
		return gocui.ErrQuit
	})
	if err := ng.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatal(err)
	}
}
