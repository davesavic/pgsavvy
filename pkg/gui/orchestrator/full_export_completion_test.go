package orchestrator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/tasks"
)

func TestReview_full_export_native_publication(t *testing.T) {
	for _, mode := range []string{"publication", "rbm_stop", "editor_quit", "manager_error", "cancel_delivery", "callback_canceled", "burst_retire"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReview_full_export_native_process$", "-test.v")
			cmd.Env = append(os.Environ(), "PGSAVVY_FULL_EXPORT_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native publication: %v (deadline=%v)\n%s", err, ctx.Err(), out)
			}
			t.Logf("%s", out)
		})
	}
}

func TestReview_full_export_native_process(t *testing.T) {
	mode := os.Getenv("PGSAVVY_FULL_EXPORT_PROBE")
	if mode == "" {
		return
	}
	native, err := gocui.NewGui(gocui.NewGuiOpts{Headless: true, Width: 130, Height: 42})
	if err != nil {
		t.Fatal(err)
	}
	d := newGocuiDriver(native)
	defer func() { _ = d.Close() }()
	entered := false
	terminalErr := gocui.ErrQuit
	if mode == "manager_error" {
		terminalErr = errors.New("synthetic layout failure")
	}
	if mode == "callback_canceled" {
		terminalErr = context.Canceled
	}
	var retirementStart time.Time
	var burstSettled atomic.Int32
	d.SetManager(gocui.ManagerFunc(func(g *gocui.Gui) error {
		if entered {
			return nil
		}
		entered = true
		retirementStart = time.Now()
		for range 20 {
			g.UpdateContentOnly(func(*gocui.Gui) error { return nil })
		}
		if mode == "burst_retire" {
			for range 512 {
				d.publish(context.Background(), func() error {
					t.Error("retired burst callback executed")
					return nil
				}, true, func(err error) {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("retired burst outcome=%v", err)
					}
					burstSettled.Add(1)
				})
			}
			return terminalErr
		}
		if mode == "callback_canceled" {
			d.publish(context.Background(), func() error { return context.Canceled }, true, nil)
			return nil
		}
		if mode == "cancel_delivery" {
			ctx, cancel := context.WithCancel(context.Background())
			d.publish(ctx, func() error { cancel(); return ctx.Err() }, true, func(err error) {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled delivery outcome=%v", err)
				}
			})
			d.UpdateContentOnly(func() error { return gocui.ErrQuit })
			return nil
		}
		d.UpdateContentOnly(func() error {
			t.Error("retired publication executed")
			return nil
		})
		if mode == "rbm_stop" {
			app := &Gui{driver: d}
			published := make(chan struct{})
			workerDone := make(chan struct{})
			result := make(chan error, 1)
			mgr := tasks.New(func(fn func(gocui.Task) error) {
				go func() { defer close(workerDone); _ = fn(nil) }()
			}, nil, tasks.WithUIDispatch(func(ctx context.Context, fn func() error, settled func(error)) {
				app.PublishUIContent(ctx, fn, settled)
				close(published)
			}))
			if err := mgr.NewQueryTask("native", func(context.Context) (drivers.RowStream, error) {
				return &nativeExportStream{}, nil
			}, func([]models.Row) { t.Error("stopped final batch appended") }, 0, nil); err != nil {
				return err
			}
			mgr.ReadToEndWithCompletion(context.Background(), func(err error) { result <- err })
			select {
			case <-published:
			case <-time.After(time.Second):
				return errors.New("RBM blocked on saturated native enqueue")
			}
			mgr.Stop() // MainLoop cannot drain native userEvents during this wait.
			<-workerDone
			if err := <-result; !errors.Is(err, context.Canceled) {
				return errors.New("stopped full export reported false success")
			}
		}
		if time.Since(retirementStart) >= time.Second {
			t.Error("saturated native publication blocked MainLoop")
		}
		if mode == "editor_quit" {
			view, err := g.SetView("editor", 0, 0, 60, 10, 0)
			if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
				return err
			}
			matcher, err := keys.NewMatcher(nil, keys.MatcherConfig{})
			if err != nil {
				return err
			}
			defer matcher.Cancel()
			ed := NewMasterEditor(g, matcher, types.COMMAND_LINE, WithEmergencyQuit(func() error { return gocui.ErrQuit }))
			if err := d.SetMasterEditor("editor", ed); err != nil {
				return err
			}
			key := gocui.NewKeyRune('c')
			ed.Edit(view, gocui.NewKey(key.KeyName(), key.Str(), gocui.ModCtrl))
			return nil
		}
		return terminalErr
	}))
	if err := d.MainLoop(); !errors.Is(err, terminalErr) {
		t.Fatalf("MainLoop: %v", err)
	}
	if time.Since(retirementStart) >= time.Second {
		t.Fatal("native publisher/worker retirement exceeded one second")
	}
	select {
	case <-d.publisherDone:
	default:
		t.Fatal("MainLoop returned before publisher joined")
	}
	if mode == "burst_retire" && burstSettled.Load() != 512 {
		t.Fatalf("burst settled=%d; want exactly 512", burstSettled.Load())
	}
	d.publicationMu.Lock()
	pending, accepted := len(d.publications), len(d.accepted)
	d.publicationMu.Unlock()
	if pending != 0 || accepted != 0 {
		t.Fatalf("retired publication retention: pending=%d accepted=%d", pending, accepted)
	}
	t.Log("saturated native queue: submission returned; retired publication did not execute; MainLoop quit")
}

type nativeExportStream struct{ read bool }

func (s *nativeExportStream) Next(context.Context) (models.Row, bool, error) {
	if s.read {
		return models.Row{}, false, nil
	}
	s.read = true
	return models.Row{Values: []any{"final"}}, true, nil
}

func (s *nativeExportStream) Close() error                 { return nil }
func (s *nativeExportStream) Columns() []models.ColumnMeta { return nil }
func (s *nativeExportStream) QueryID() models.QueryID      { return models.QueryID{} }
func (s *nativeExportStream) RowsAffected() int64          { return 0 }
