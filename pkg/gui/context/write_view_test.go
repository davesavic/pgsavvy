package context

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestWriteView_ToleratesUnknownView reproduces the commit-dialog crash:
// a popup replacement can temporarily remove a render target. A benign
// "view gone" must not reach the fatal Update error path; the next layout
// recreates and repaints the view.
func TestWriteView_ToleratesUnknownView(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	deps := types.ContextTreeDeps{GuiDriver: drv}

	writeView(deps, func() error { return gocui.ErrUnknownView })

	if errs := drv.UpdateErrors(); len(errs) != 0 {
		t.Fatalf("writeView leaked %d error(s) to the Update queue (gocui MainLoop would exit fatally): %v", len(errs), errs)
	}
}

// TestWriteView_PropagatesRealErrors guards the fix from over-swallowing:
// any error that is NOT ErrUnknownView must still reach the Update queue so
// genuine failures are not silently hidden.
func TestWriteView_PropagatesRealErrors(t *testing.T) {
	drv := testfake.NewRecorderGuiDriver()
	deps := types.ContextTreeDeps{GuiDriver: drv}

	sentinel := errors.New("boom")
	writeView(deps, func() error { return sentinel })

	errs := drv.UpdateErrors()
	if len(errs) != 1 || !errors.Is(errs[0], sentinel) {
		t.Fatalf("writeView should propagate non-ErrUnknownView errors, got: %v", errs)
	}
}

type renderCountingDriver struct {
	*testfake.RecorderGuiDriver
	full, content int
}

func (d *renderCountingDriver) Update(fn func() error) {
	d.full++
	d.RecorderGuiDriver.Update(fn)
}

func (d *renderCountingDriver) UpdateContentOnly(fn func() error) {
	d.content++
	d.RecorderGuiDriver.UpdateContentOnly(fn)
}

func TestReview_render_write(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"missing", gocui.ErrUnknownView},
		{"wrapped_missing", fmt.Errorf("view: %w", gocui.ErrUnknownView)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &renderCountingDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
			calls := 0
			writeView(types.ContextTreeDeps{GuiDriver: d}, func() error {
				calls++
				return tc.err
			})
			if calls != 1 || d.full != 0 || d.content != 0 {
				t.Fatalf("render calls=%d full=%d content=%d; want 1,0,0", calls, d.full, d.content)
			}
			if errs := d.UpdateErrors(); len(errs) != 0 {
				t.Fatalf("benign render error escaped: %v", errs)
			}
		})
	}
	t.Run("nil_driver", func(t *testing.T) {
		writeView(types.ContextTreeDeps{}, func() error {
			t.Fatal("nil driver executed render")
			return nil
		})
	})
	// Retain the fatal-error delivery contract independently of successful paints.
	t.Run("real_error", func(t *testing.T) {
		d := &renderCountingDriver{RecorderGuiDriver: testfake.NewRecorderGuiDriver()}
		err := errors.New("render failed")
		writeView(types.ContextTreeDeps{GuiDriver: d}, func() error { return err })
		if errs := d.UpdateErrors(); len(errs) != 1 || !errors.Is(errs[0], err) {
			t.Fatalf("fatal error delivery = %v", errs)
		}
	})
}
