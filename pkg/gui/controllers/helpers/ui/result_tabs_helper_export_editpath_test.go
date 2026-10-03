package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/popup"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/tasks"
)

type fullExportStream struct {
	gate     <-chan struct{}
	index    int
	nextErr  bool
	closeErr bool
	total    int
	column   string
}

func (s *fullExportStream) Columns() []models.ColumnMeta {
	name := s.column
	if name == "" {
		name = "id"
	}
	return []models.ColumnMeta{{Name: name}}
}

func (s *fullExportStream) Next(ctx context.Context) (models.Row, bool, error) {
	select {
	case <-s.gate:
	case <-ctx.Done():
		return models.Row{}, false, ctx.Err()
	}
	if s.nextErr && s.index == 1 {
		return models.Row{}, false, errors.New("synthetic Next failure")
	}
	total := s.total
	if total == 0 {
		total = 201
	}
	if s.index == total {
		return models.Row{}, false, nil
	}
	s.index++
	return models.Row{Values: []any{s.index}}, true, nil
}

func (s *fullExportStream) Close() error {
	if s.closeErr {
		return errors.New("synthetic Close failure")
	}
	return nil
}

func (s *fullExportStream) QueryID() models.QueryID { return models.QueryID{} }
func (s *fullExportStream) RowsAffected() int64     { return 0 }

type fullExportRunner struct {
	*tasks.ResultBufferManager
	requested chan struct{}
}

func (r *fullExportRunner) ReadToEnd(then func()) {
	r.ResultBufferManager.ReadToEnd(then)
	close(r.requested)
}

func (r *fullExportRunner) ReadToEndWithCompletion(ctx context.Context, completed func(error)) {
	r.ResultBufferManager.ReadToEndWithCompletion(ctx, completed)
	close(r.requested)
}

func TestReview_full_export_helper(t *testing.T) {
	for _, mode := range []string{"success", "next_error", "close_error", "cancel", "reopen_cancel", "replace", "dispose", "before_worker_replace", "overwrite_replace"} {
		t.Run(mode, func(t *testing.T) {
			streamGate := make(chan struct{})
			workerGate := make(chan struct{})
			streamDone := make(chan struct{})
			var streamWorkers sync.WaitGroup
			var launches atomic.Int32
			exportDone := make(chan struct{})
			ui := make(chan func() error, 16)
			runner := &fullExportRunner{requested: make(chan struct{})}
			runner.ResultBufferManager = tasks.New(func(fn func(gocui.Task) error) {
				first := launches.Add(1) == 1
				streamWorkers.Go(func() {
					_ = fn(nil)
					if first {
						close(streamDone)
					}
				})
			}, func(fn func() error) { ui <- fn })
			toast := &fakeToaster{}
			confirm := &fakeConfirmer{}
			h := NewResultTabsHelper(ResultTabsHelperDeps{
				StreamFactory: func() StreamRunner { return runner },
				Toast:         toast, Confirm: confirm,
				OnWorker: func(fn func(gocui.Task) error) {
					go func() { defer close(exportDone); <-workerGate; _ = fn(nil) }()
				},
			})
			rh := newFakeRunHandle()
			rh.rows = &fullExportStream{gate: streamGate, nextErr: mode == "next_error", closeErr: mode == "close_error"}
			if err := h.openTab("export", rh); err != nil {
				t.Fatal(err)
			}
			tab := h.Active()
			path := filepath.Join(t.TempDir(), "rows.csv")
			menu := popup.NewExportMenu([]string{"CSV"}, []string{"Clipboard", "File"}, []string{"Visible", "Loaded", "Full"}, -1, false)
			menu.SetPath(path)
			menu.MoveField(3)
			menu.MoveValue(2)
			if mode == "overwrite_replace" {
				if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h.exportMenu = &activeExportMenu{tab: tab, menu: menu}
			h.ExportMenuConfirm()
			if mode == "overwrite_replace" {
				runner.Stop()
				h.reattachActiveTab(nil, "select replacement", "select replacement")
				if confirm.lastYes == nil {
					t.Fatal("overwrite confirmation not requested")
				}
				if err := confirm.lastYes(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "before_worker_replace" {
				runner.Stop()
				h.reattachActiveTab(nil, "select replacement", "select replacement")
			}
			close(workerGate)
			if mode != "before_worker_replace" && mode != "overwrite_replace" {
				select {
				case <-runner.requested:
				case <-time.After(time.Second):
					t.Fatal("full-export request did not register")
				}
			}
			close(streamGate)
			select {
			case <-streamDone:
			case <-time.After(time.Second):
				t.Fatal("stream worker waited on held UI append")
			}
			rh.finish()
			if mode == "success" || mode == "cancel" || mode == "replace" || mode == "dispose" {
				select {
				case <-exportDone:
					t.Fatal("export settled before final UI append")
				default:
				}
			}
			if mode == "cancel" {
				h.CancelExport()
			}
			if mode == "reopen_cancel" {
				h.PromptExport()
				h.CancelExport()
			}
			if mode == "replace" {
				replacement := newFakeRunHandle()
				replacement.rows = &fullExportStream{gate: streamGate, total: 1, column: "replacement"}
				h.reattachActiveTab(replacement, "select replacement", "select replacement")
				streamWorkers.Wait()
				replacement.finish()
			}
			if mode == "dispose" {
				tab.dispose()
			}
			if mode != "success" {
				select {
				case <-exportDone:
				case <-time.After(time.Second):
					t.Fatal("failure/cancellation waited for held UI append")
				}
			}
			for len(ui) > 0 {
				if err := (<-ui)(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "replace" {
				cols, rows := tab.Grid().Columns(), tab.Grid().AllRows()
				if len(cols) != 1 || cols[0].Name != "replacement" || len(rows) != 1 || tab.RowCount() != 1 {
					t.Fatalf("stale delivery contaminated replacement: columns=%v rows=%d count=%d", cols, len(rows), tab.RowCount())
				}
			}
			select {
			case <-exportDone:
			case <-time.After(time.Second):
				t.Fatal("export waiter failed to settle")
			}
			data, err := os.ReadFile(path)
			switch mode {
			case "success":
				if err != nil || string(data) != "id\r\n"+fullExportExpectedRows() {
					t.Fatalf("full export omitted/changed rows: bytes=%d err=%v", len(data), err)
				}
				if !strings.Contains(toast.Last(), "export complete") {
					t.Fatalf("completion toast=%q", toast.Last())
				}
			case "overwrite_replace":
				if err != nil || string(data) != "original" {
					t.Fatalf("stale export overwrote existing destination: %q, %v", data, err)
				}
			default:
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed/canceled/stale export opened destination: err=%v", err)
				}
			}
			runner.Stop()
			streamWorkers.Wait()
		})
	}
}

func fullExportExpectedRows() string {
	var s strings.Builder
	for i := 1; i <= 201; i++ {
		fmt.Fprintf(&s, "%d\r\n", i)
	}
	return s.String()
}

// newPathMenuOnFile builds an export menu whose cursor sits on the Path
// field with a File destination — the only state in which the 'i'
// edit-path binding does anything.
func newPathMenuOnFile(initial string) *popup.ExportMenu {
	m := popup.NewExportMenu(
		[]string{"CSV"},
		[]string{"Clipboard", "File"},
		[]string{"Buffered"},
		-1, false,
	)
	m.Prefill(initial)
	// File is default (idx 1). Format(0) → Destination(1) → Path(2).
	m.MoveField(+1)
	m.MoveField(+1)
	if !m.IsPathFieldActive() {
		panic("setup: Path field not active")
	}
	return m
}

// TestExportMenuEditPath_SubmitWritesBackAndReopens verifies that the
// seeded PROMPT receives the current path, a trimmed valid value is
// written back via SetPath, and the menu is re-pushed (return-to-menu).
func TestExportMenuEditPath_SubmitWritesBackAndReopens(t *testing.T) {
	m := newPathMenuOnFile("/tmp/out.csv")

	var gotInitial string
	var submit func(string) error
	pushes := 0

	h := NewResultTabsHelper(ResultTabsHelperDeps{
		PushExportMenu: func() error { pushes++; return nil },
		EditExportPath: func(initial string, onSubmit func(string) error, _ func() error) error {
			gotInitial = initial
			submit = onSubmit
			return nil
		},
	})
	h.exportMenu = &activeExportMenu{menu: m}

	h.ExportMenuEditPath()

	if gotInitial != "/tmp/out.csv" {
		t.Fatalf("prompt seeded with %q; want %q", gotInitial, "/tmp/out.csv")
	}
	if submit == nil {
		t.Fatal("EditExportPath onSubmit not captured")
	}

	if err := submit("  /tmp/new.csv  "); err != nil {
		t.Fatalf("submit returned error: %v", err)
	}
	if got := m.Path(); got != "/tmp/new.csv" {
		t.Errorf("Path = %q; want trimmed %q", got, "/tmp/new.csv")
	}
	if pushes != 1 {
		t.Errorf("PushExportMenu called %d times on submit; want 1", pushes)
	}
}

// TestExportMenuEditPath_RejectsControlChars verifies that on a control-char
// submit the helper: (a) leaves Path unchanged, (b) surfaces an error toast,
// (c) RE-OPENS the prompt seeded with the rejected value, and (d) returns nil
// from onSubmit (so master_editor.go does not swallow an error and drop the
// user out of the flow). The menu must NOT be re-pushed on rejection.
func TestExportMenuEditPath_RejectsControlChars(t *testing.T) {
	m := newPathMenuOnFile("/tmp/out.csv")

	toast := &fakeToaster{}
	var submit func(string) error
	var initials []string
	pushes := 0
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		Toast:          toast,
		PushExportMenu: func() error { pushes++; return nil },
		EditExportPath: func(initial string, onSubmit func(string) error, _ func() error) error {
			initials = append(initials, initial)
			submit = onSubmit
			return nil
		},
	})
	h.exportMenu = &activeExportMenu{menu: m}
	h.ExportMenuEditPath()

	bad := "/tmp/ba\nd.csv"
	if err := submit(bad); err != nil {
		t.Fatalf("onSubmit returned error %v; want nil (error must not propagate to master_editor)", err)
	}
	if got := m.Path(); got != "/tmp/out.csv" {
		t.Errorf("Path mutated on rejected submit: %q", got)
	}
	if pushes != 0 {
		t.Errorf("PushExportMenu called %d times on rejected submit; want 0", pushes)
	}
	if len(toast.Messages()) == 0 {
		t.Error("no toast surfaced on rejected submit; want a validation toast")
	}
	// First open seeds the current path; the rejection re-opens seeded with
	// the rejected value so the user can fix it in place.
	if len(initials) != 2 {
		t.Fatalf("EditExportPath invoked %d times; want 2 (initial open + re-open)", len(initials))
	}
	if initials[1] != bad {
		t.Errorf("re-open seeded with %q; want rejected value %q", initials[1], bad)
	}
}

// TestExportMenuEditPath_CancelLeavesPathAndReopens verifies cancel keeps
// the path unchanged and re-pushes the menu.
func TestExportMenuEditPath_CancelLeavesPathAndReopens(t *testing.T) {
	m := newPathMenuOnFile("/tmp/out.csv")

	var cancel func() error
	pushes := 0
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		PushExportMenu: func() error { pushes++; return nil },
		EditExportPath: func(_ string, _ func(string) error, onCancel func() error) error {
			cancel = onCancel
			return nil
		},
	})
	h.exportMenu = &activeExportMenu{menu: m}
	h.ExportMenuEditPath()

	if err := cancel(); err != nil {
		t.Fatalf("cancel returned error: %v", err)
	}
	if got := m.Path(); got != "/tmp/out.csv" {
		t.Errorf("Path changed on cancel: %q", got)
	}
	if pushes != 1 {
		t.Errorf("PushExportMenu called %d times on cancel; want 1", pushes)
	}
}

// TestExportMenuEditPath_NoOpWhenPathFieldNotActive verifies the edit
// prompt does not open when the cursor is not on the Path field.
func TestExportMenuEditPath_NoOpWhenPathFieldNotActive(t *testing.T) {
	m := popup.NewExportMenu([]string{"CSV"}, []string{"File"}, []string{"Buffered"}, -1, false)
	// Cursor stays on FieldFormat — Path not active.
	opened := false
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		EditExportPath: func(string, func(string) error, func() error) error { opened = true; return nil },
	})
	h.exportMenu = &activeExportMenu{menu: m}
	h.ExportMenuEditPath()
	if opened {
		t.Error("edit prompt opened while Path field inactive")
	}
}
