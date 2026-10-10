package orchestrator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

func TestBoundNavigationNativeStaleHistoryIsNonfatal(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestBoundNavigationNativeProcess$", "-test.count=1")
	command.Env = append(os.Environ(), "PGSAVVY_BOUND_NAV_NATIVE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native stale history: %v\n%s", err, output)
	}
}

func TestBoundNavigationNativeProcess(t *testing.T) {
	if os.Getenv("PGSAVVY_BOUND_NAV_NATIVE") == "" {
		return
	}
	native, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 130, Height: 42})
	if err != nil {
		t.Fatal(err)
	}
	inner := &navigationSession{fkGuardSession: &fkGuardSession{fwd: []models.ForeignKey{{Name: "parent_fk", Columns: []string{"parent_id"}, RefSchema: "app", RefTable: "parents", RefColumns: []string{"id"}}}}, queryEntered: make(chan models.Query, 2)}
	g, _ := navigationFixture(t, inner)
	driver := newGocuiDriver(native)
	defer func() { _ = driver.Close() }()
	g.driver = driver
	native.SetManagerFunc(func(*gocui.Gui) error { return nil })
	if err := g.runBoundForwardFK(context.Background(), navigationOrigin{g.resultTabsH.Active()}, 0, 1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	queued := false
	for time.Now().Before(deadline) {
		driver.publicationMu.Lock()
		queued = len(driver.accepted) > 0
		driver.publicationMu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !queued {
		t.Fatal("FK metadata did not queue its history continuation")
	}
	ordinary := make(chan struct{})
	g.queryState.queryRunner.RunQueryAsync(context.Background(), models.Query{SQL: "newer user query"}, func(handle *session.RunHandle, err error) {
		if err != nil {
			t.Error(err)
		}
		if handle != nil {
			_ = handle.Rows().Close()
		}
		close(ordinary)
	})
	asyncAwait(t, ordinary)
	continued := false
	g.PublishUIContent(context.Background(), func() error { continued = true; return gocui.ErrQuit }, func(error) {})
	err = driver.MainLoop()
	if err != nil && !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("stale FK retired MainLoop: %v", err)
	}
	if !continued {
		t.Fatal("stale FK history rejection prevented subsequent native UI events")
	}
	select {
	case q := <-inner.queryEntered:
		if q.SQL != "newer user query" {
			t.Fatal("stale FK executed instead of the newer query")
		}
	default:
		t.Fatal("newer user query did not execute")
	}
	select {
	case <-inner.queryEntered:
		t.Fatal("stale FK history acknowledgment dispatched a parent SELECT")
	default:
	}
}
