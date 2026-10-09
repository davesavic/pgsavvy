package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// recordingClipboard captures the payload pushed through ClipboardWriter so the
// export-to-clipboard path can be asserted without a real system clipboard.
type recordingClipboard struct {
	got   string
	calls int
}

func (r *recordingClipboard) Write(text string) error {
	r.got = text
	r.calls++
	return nil
}

// TestBuildDestination_Clipboard_WritesToConfiguredWriter is the regression
// guard: buildDestination used to pass a nil ClipboardWriter,
// so clipboard exports were serialized and then silently discarded on Close.
// The destination must push the buffered payload to the configured writer.
func TestBuildDestination_Clipboard_WritesToConfiguredWriter(t *testing.T) {
	rec := &recordingClipboard{}
	h := NewResultTabsHelper(ResultTabsHelperDeps{ExportClipboard: rec})

	dest, err := h.buildDestination("Clipboard", "")
	if err != nil {
		t.Fatalf("buildDestination: %v", err)
	}

	wc, descriptor, err := dest.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if descriptor != "clipboard" {
		t.Fatalf("descriptor = %q, want %q", descriptor, "clipboard")
	}

	const payload = "a,b\r\n1,2\r\n"
	if _, err := wc.Write([]byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if rec.calls != 1 {
		t.Fatalf("clipboard Write calls = %d, want 1 (payload discarded?)", rec.calls)
	}
	if rec.got != payload {
		t.Fatalf("clipboard payload = %q, want %q", rec.got, payload)
	}
}

// TestBuildDestination_StdoutRemoved guards the removal of the stdout export
// destination: its bytes were written to os.Stdout from a
// worker while gocui owns the alternate screen, so they could never render.
// The destination is no longer offered, and the builder must reject it.
func TestBuildDestination_StdoutRemoved(t *testing.T) {
	h := NewResultTabsHelper(ResultTabsHelperDeps{})
	if _, err := h.buildDestination("stdout", ""); err == nil {
		t.Fatal("buildDestination(\"stdout\") = nil error, want rejected")
	}
}

func TestExportMenuScopedThresholds(t *testing.T) {
	for _, format := range []string{"JSON Array", "Markdown", "NDJSON"} {
		for _, tc := range []struct {
			name     string
			scope    int
			buffered int
			visible  int
			estimate int64
			blocked  bool
		}{
			{"visible at threshold", exportScopeVisible, 8, 5, 100, false},
			{"visible above threshold", exportScopeVisible, 8, 6, 100, true},
			{"visible unknown estimate", exportScopeVisible, 8, 6, 0, true},
			{"buffered at threshold", exportScopeLoaded, 5, 2, 100, false},
			{"buffered above threshold", exportScopeLoaded, 6, 2, 100, true},
			{"buffered unknown estimate", exportScopeLoaded, 6, 2, 0, true},
			{"full at threshold", exportScopeFull, 6, 2, 5, false},
			{"full above threshold", exportScopeFull, 2, 2, 6, true},
			{"full unknown estimate", exportScopeFull, 6, 2, 0, false},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				h, runner, clipboard, toast := newScopedExportHelper(t, tc.buffered, tc.visible)
				runner.setEstimatedRows(tc.estimate)
				h.PromptExport()
				selectExportClipboardScope(h, format, tc.scope)
				wantBlocked := tc.blocked && format != "NDJSON"
				if got := h.exportMenu.menu.ConfirmBlockedReason(); (got != "") != wantBlocked {
					t.Errorf("blocked reason = %q, want blocked=%v", got, wantBlocked)
				}
				if body := h.ExportMenuBody(); strings.Contains(body, "Confirm disabled") != wantBlocked {
					t.Errorf("threshold warning disagrees with scope: %q", body)
				}
				h.ExportMenuConfirm()
				if wantBlocked {
					if clipboard.calls != 0 || runner.ReadToEndCount() != 0 || !h.ExportMenuActive() {
						t.Fatal("blocked export wrote, drained, or closed the menu")
					}
					if !strings.Contains(toast.Last(), "threshold") {
						t.Fatalf("blocked toast = %q", toast.Last())
					}
					return
				}
				wantRows := tc.buffered
				if tc.scope == exportScopeVisible {
					wantRows = tc.visible
				}
				if clipboard.calls != 1 || strings.Count(clipboard.got, "row-") != wantRows || !strings.Contains(toast.Last(), "export complete") {
					t.Fatalf("export: clipboard=%q calls=%d toast=%q, want %d rows", clipboard.got, clipboard.calls, toast.Last(), wantRows)
				}
				wantDrains := 0
				if tc.scope == exportScopeFull {
					wantDrains = 1
				}
				if got := runner.ReadToEndCount(); got != wantDrains {
					t.Fatalf("drains=%d, want %d", got, wantDrains)
				}
			})
		}
	}
}

func TestExportMenuScopeAfterDeclinedDrain(t *testing.T) {
	for _, format := range []string{"JSON Array", "Markdown"} {
		for _, scope := range []int{exportScopeVisible, exportScopeLoaded} {
			t.Run(fmt.Sprintf("%s/%d", format, scope), func(t *testing.T) {
				h, runner, clipboard, toast := newScopedExportHelper(t, 5, 2)
				confirm := &fakeConfirmer{autoNo: true}
				estimate := &fakeEstimator{est: 2_000_000}
				h.deps.Confirm = confirm
				h.deps.EstimateRows = estimate.fn
				h.Active().SetOrigin("select * from large_result", nil, "")
				h.ReadToEnd()
				if confirm.Calls() != 1 || runner.ReadToEndCount() != 0 || runner.EstimatedRows() != 2_000_000 {
					t.Fatal("setup did not cache the estimate and decline the drain")
				}
				h.PromptExport()
				selectExportClipboardScope(h, format, exportScopeFull)
				if h.exportMenu.menu.ConfirmBlockedReason() == "" {
					t.Fatal("all rows must retain its safety gate")
				}
				h.ExportMenuMoveValue(scope - exportScopeFull)
				if reason := h.exportMenu.menu.ConfirmBlockedReason(); reason != "" {
					t.Errorf("small scope remains blocked after declining G: %s", reason)
				}
				h.ExportMenuConfirm()
				wantRows := 5
				if scope == exportScopeVisible {
					wantRows = 2
				}
				if runner.ReadToEndCount() != 0 || clipboard.calls != 1 || strings.Count(clipboard.got, "row-") != wantRows {
					t.Fatalf("export drained or lost scoped rows: drains=%d calls=%d payload=%q toast=%q", runner.ReadToEndCount(), clipboard.calls, clipboard.got, toast.Last())
				}
			})
		}
	}
}

func TestExportMenuThresholdRechecksGrowingBuffer(t *testing.T) {
	for _, stage := range []string{"before confirm", "before worker"} {
		t.Run(stage, func(t *testing.T) {
			h, _, clipboard, toast := newScopedExportHelper(t, 5, 2)
			var worker func(gocui.Task) error
			h.deps.OnWorker = func(fn func(gocui.Task) error) { worker = fn }
			h.PromptExport()
			selectExportClipboardScope(h, "JSON Array", exportScopeLoaded)
			if stage == "before worker" {
				h.ExportMenuConfirm()
				if worker == nil {
					t.Fatal("at-threshold export not dispatched")
				}
			}
			h.Active().Grid().AppendRows([]models.Row{{Values: []any{"row-extra"}}})
			if stage == "before confirm" {
				h.ExportMenuConfirm()
				if worker != nil {
					t.Error("over-threshold buffer dispatched")
				}
			} else if err := worker(nil); err != nil {
				t.Fatal(err)
			}
			if clipboard.calls != 0 || !strings.Contains(toast.Last(), "threshold") {
				t.Fatalf("growing buffer bypassed threshold: calls=%d toast=%q", clipboard.calls, toast.Last())
			}
		})
	}
}

func TestExportMenuThresholdRefreshesOnRender(t *testing.T) {
	h, _, clipboard, _ := newScopedExportHelper(t, 5, 2)
	h.PromptExport()
	selectExportClipboardScope(h, "JSON Array", exportScopeLoaded)
	if strings.Contains(h.ExportMenuBody(), "Confirm disabled") {
		t.Fatal("at-threshold buffer is blocked")
	}
	h.Active().Grid().AppendRows([]models.Row{{Values: []any{"row-extra"}}})
	if body := h.ExportMenuBody(); !strings.Contains(body, "Confirm disabled") {
		t.Fatalf("render did not refresh the warning after rows arrived: %q", body)
	}
	if h.exportMenu.menu.ConfirmBlockedReason() == "" || clipboard.calls != 0 {
		t.Fatal("rendered warning disagrees with confirmation gate or published data")
	}
}

func newScopedExportHelper(t *testing.T, buffered, visible int) (*ResultTabsHelper, *fakeStreamRunner, *recordingClipboard, *fakeToaster) {
	t.Helper()
	runner := &fakeStreamRunner{}
	clipboard := &recordingClipboard{}
	toast := &fakeToaster{}
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		StreamFactory:                  func() StreamRunner { return runner },
		ExportClipboard:                clipboard,
		ExportBufferedRowWarnThreshold: 5,
		Toast:                          toast,
		OnWorker: func(fn func(gocui.Task) error) {
			if err := fn(nil); err != nil {
				t.Fatal(err)
			}
		},
	})
	rh := newFakeRunHandle()
	t.Cleanup(rh.finish)
	if err := h.openTab("export scope", rh); err != nil {
		t.Fatal(err)
	}
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "value", TypeName: "text"}})
	rows := make([]models.Row, buffered)
	for i := range rows {
		rows[i] = models.Row{Values: []any{fmt.Sprintf("row-%d", i)}}
	}
	g.AppendRows(rows)
	g.Render(gocui.NewView("scoped-export", 0, 0, 50, visible+2, gocui.OutputNormal))
	if got := len(g.VisibleRows()); got != visible {
		t.Fatalf("visible rows=%d, want %d", got, visible)
	}
	return h, runner, clipboard, toast
}

func selectExportClipboardScope(h *ResultTabsHelper, format string, scope int) {
	h.ExportMenuMoveValue(indexOf(exportFormatLabels(false), format))
	h.ExportMenuMoveField(1)
	h.ExportMenuMoveValue(-1)
	h.ExportMenuMoveField(1)
	h.ExportMenuMoveValue(scope)
}
