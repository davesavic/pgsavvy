package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gdamore/tcell/v3"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

type nativeRenderDriver struct {
	*gocuiDriver
	full, content int
}

func (d *nativeRenderDriver) Update(fn func() error) {
	d.full++
	d.gocuiDriver.Update(fn)
}

func (d *nativeRenderDriver) UpdateContentOnly(fn func() error) {
	d.content++
	d.gocuiDriver.UpdateContentOnly(fn)
}

// Subprocesses isolate gocui's global Screen and make a blocked MainLoop a
// bounded test failure, including when the baseline leaves queued goroutines.
func TestReview_render_native(t *testing.T) {
	for _, mode := range []string{"idle", "saturated"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_render_native_process$", "-test.v")
			cmd.Env = append(os.Environ(), "PGSAVVY_RENDER_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native %s probe: %v (deadline=%v)\n%s", mode, err, ctx.Err(), out)
			}
			t.Logf("%s", out)
		})
	}
}

func TestReview_render_native_process(t *testing.T) {
	mode := os.Getenv("PGSAVVY_RENDER_PROBE")
	if mode == "" {
		return
	}
	g, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 130, Height: 42})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	// tcell initialization asynchronously posts a resize in addition to
	// SetManager's event. Drain that real startup event before measuring the
	// one requested layout; otherwise arrival timing masquerades as feedback.
	if ev := <-gocui.Screen.EventQ(); ev == nil {
		t.Fatal("missing native startup resize")
	} else if _, ok := ev.(*tcell.EventResize); !ok {
		t.Fatalf("unexpected native startup event: %T", ev)
	}
	d := &nativeRenderDriver{gocuiDriver: newGocuiDriver(g)}
	tree := guicontext.NewContextTree(types.ContextTreeDeps{GuiDriver: d})
	tree.ConnectionManager.SetItems([]any{models.Connection{Name: "synthetic offline"}})
	rows := make([]any, 30)
	for i := range rows {
		rows[i] = models.Schema{Name: fmt.Sprintf("schema_%02d", i)}
	}
	tree.Schemas.SetItems(rows)
	tree.Schemas.SetCursor(29)
	layouts := 0
	entered := make(chan struct{})
	g.SetManagerFunc(func(g *gocui.Gui) error {
		layouts++
		if layouts > 8 {
			return fmt.Errorf("idle render feedback: %d layouts", layouts)
		}
		for _, name := range []string{tree.ConnectionManager.GetViewName(), tree.SchemaRail.GetViewName()} {
			if _, err := g.SetView(name, 0, 0, 60, 10, 0); err != nil && !errors.Is(err, gocui.ErrUnknownView) {
				return err
			}
		}
		if mode == "saturated" {
			// MainLoop is inside Layout and cannot drain this 20-slot native
			// queue. A content-only substitution deadlocks on the next send.
			for range 20 {
				g.UpdateContentOnly(func(*gocui.Gui) error { return nil })
			}
		}
		for range 32 {
			if err := tree.ConnectionManager.HandleRender(); err != nil {
				return err
			}
			if err := tree.SchemaRail.HandleRender(); err != nil {
				return err
			}
		}
		if mode == "saturated" {
			if d.full != 0 || d.content != 0 {
				return fmt.Errorf("render scheduled full=%d content=%d into saturated native queue", d.full, d.content)
			}
			return gocui.ErrQuit
		}
		if layouts == 1 {
			close(entered)
		}
		return nil
	})
	if mode == "idle" {
		go func() {
			<-entered
			// The observation begins only once native Layout actually renders.
			time.Sleep(200 * time.Millisecond)
			g.UpdateContentOnly(func(*gocui.Gui) error { return gocui.ErrQuit })
		}()
	}
	if err := g.MainLoop(); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("MainLoop: %v", err)
	}
	if layouts != 1 || d.full != 0 || d.content != 0 {
		t.Fatalf("layouts=%d render full=%d content=%d; want 1,0,0", layouts, d.full, d.content)
	}
	v, err := g.View(tree.SchemaRail.GetViewName())
	if err != nil {
		t.Fatal(err)
	}
	if _, oy := v.Origin(); oy == 0 {
		t.Fatal("native rail cursor did not scroll into view")
	}
	t.Logf("native %s: layouts=%d; 32 connection-manager and rail renders; zero render enqueues", mode, layouts)
}
