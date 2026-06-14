package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// orderRecorder records the relative order of "confirm" vs "worker" so the
// deadlock-avoidance guarantee (Confirm must run on the UI thread BEFORE the
// worker is dispatched) can be asserted.
type orderRecorder struct {
	events []string
}

func (o *orderRecorder) add(e string) { o.events = append(o.events, e) }

// syncWorker runs the dispatched closure synchronously (mirrors OnWorker for
// tests) after recording that the worker was reached.
func syncWorker(rec *orderRecorder) func(func(gocui.Task) error) {
	return func(fn func(gocui.Task) error) {
		rec.add("worker")
		_ = fn(nil)
	}
}

// newExportMenuOnFile builds a CSV/File export menu with the Scope on
// "Loaded" (index 1) and the path prefilled. Loaded scope snapshots
// grid.AllRows, which is populated by seedTabWithRows without needing a render.
func newExportMenuOnFile(path string) *popup.ExportMenu {
	m := popup.NewExportMenu(
		[]string{"CSV"},
		[]string{"File", "Clipboard"},
		[]string{"Visible", "Loaded", "Full"},
		-1, false,
	)
	m.Prefill(path)
	// Move the field cursor to Scope (the last navigable field, clamps
	// there) and bump the value to "Loaded" (index 1).
	m.MoveField(+1)
	m.MoveField(+1)
	m.MoveField(+1)
	m.MoveValue(+1)
	return m
}

// seedTabWithRows opens a tab and fills its grid with one column + two rows so
// the export pipeline has something to write.
func seedTabWithRows(t *testing.T, h *ResultTabsHelper) *Tab {
	t.Helper()
	if err := h.openTab("q", nil); err != nil {
		t.Fatalf("openTab: %v", err)
	}
	tab := h.Active()
	g := tab.Grid()
	g.SetColumns([]models.ColumnMeta{{Name: "c0", TypeName: "text"}})
	g.AppendRows([]models.Row{{Values: []any{"a"}}, {Values: []any{"b"}}})
	return tab
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestExportMenuConfirm_RelativePathJoinsDownloadDir verifies a relative menu
// path is resolved under GetDownloadDir() and the file lands there.
func TestExportMenuConfirm_RelativePathJoinsDownloadDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)

	rec := &orderRecorder{}
	toast := &fakeToaster{}
	h, _ := newTestHelper(t, nil)
	h.deps.Toast = toast
	h.deps.OnWorker = syncWorker(rec)

	tab := seedTabWithRows(t, h)
	m := newExportMenuOnFile("out.csv")
	h.exportMenu = &activeExportMenu{tab: tab, menu: m}

	h.ExportMenuConfirm()

	want := filepath.Join(dir, "out.csv")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
	if got := readFile(t, want); !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("file contents %q missing row values", got)
	}
	if last := toast.Last(); !strings.Contains(last, want) {
		t.Errorf("toast %q does not show resolved path %q", last, want)
	}
}

// TestExportMenuConfirm_AbsolutePathHonored verifies an absolute menu path is
// used verbatim (not joined under the download dir).
func TestExportMenuConfirm_AbsolutePathHonored(t *testing.T) {
	t.Setenv("XDG_DOWNLOAD_DIR", t.TempDir())
	dir := t.TempDir()
	abs := filepath.Join(dir, "abs.csv")

	rec := &orderRecorder{}
	toast := &fakeToaster{}
	h, _ := newTestHelper(t, nil)
	h.deps.Toast = toast
	h.deps.OnWorker = syncWorker(rec)

	tab := seedTabWithRows(t, h)
	h.exportMenu = &activeExportMenu{tab: tab, menu: newExportMenuOnFile(abs)}

	h.ExportMenuConfirm()

	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("expected file at %s: %v", abs, err)
	}
	if last := toast.Last(); !strings.Contains(last, abs) {
		t.Errorf("toast %q does not show absolute path %q", last, abs)
	}
}

// TestExportMenuConfirm_TildeExpanded verifies a "~/"-prefixed path is expanded
// against $HOME.
func TestExportMenuConfirm_TildeExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DOWNLOAD_DIR", t.TempDir())

	rec := &orderRecorder{}
	toast := &fakeToaster{}
	h, _ := newTestHelper(t, nil)
	h.deps.Toast = toast
	h.deps.OnWorker = syncWorker(rec)

	tab := seedTabWithRows(t, h)
	h.exportMenu = &activeExportMenu{tab: tab, menu: newExportMenuOnFile("~/tilde.csv")}

	h.ExportMenuConfirm()

	want := filepath.Join(home, "tilde.csv")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected file at %s: %v", want, err)
	}
	if last := toast.Last(); !strings.Contains(last, want) {
		t.Errorf("toast %q does not show expanded path %q", last, want)
	}
}

// TestExportMenuConfirm_TargetExists_ConfirmShown_YesOverwrites verifies that
// when the target already exists, a confirmation is shown and Yes overwrites.
func TestExportMenuConfirm_TargetExists_ConfirmShown_YesOverwrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)
	target := filepath.Join(dir, "exists.csv")
	if err := os.WriteFile(target, []byte("OLD CONTENT"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	rec := &orderRecorder{}
	confirm := &fakeConfirmer{autoYes: true}
	h, _ := newTestHelper(t, nil)
	h.deps.OnWorker = func(fn func(gocui.Task) error) { rec.add("worker"); _ = fn(nil) }
	h.deps.Confirm = wrapConfirmOrder(confirm, rec)

	tab := seedTabWithRows(t, h)
	h.exportMenu = &activeExportMenu{tab: tab, menu: newExportMenuOnFile("exists.csv")}

	h.ExportMenuConfirm()

	if confirm.Calls() != 1 {
		t.Fatalf("Confirm calls = %d, want 1 (overwrite prompt expected)", confirm.Calls())
	}
	got := readFile(t, target)
	if strings.Contains(got, "OLD CONTENT") {
		t.Errorf("file not overwritten: %q", got)
	}
	if !strings.Contains(got, "a") {
		t.Errorf("overwritten file missing new rows: %q", got)
	}
}

// TestExportMenuConfirm_TargetExists_NoWritesNothing verifies that declining
// the overwrite prompt leaves the existing file untouched and never dispatches
// the worker.
func TestExportMenuConfirm_TargetExists_NoWritesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)
	target := filepath.Join(dir, "exists.csv")
	if err := os.WriteFile(target, []byte("OLD CONTENT"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	rec := &orderRecorder{}
	confirm := &fakeConfirmer{autoNo: true}
	workerCalled := false
	h, _ := newTestHelper(t, nil)
	h.deps.OnWorker = func(fn func(gocui.Task) error) { workerCalled = true; _ = fn(nil) }
	h.deps.Confirm = wrapConfirmOrder(confirm, rec)

	tab := seedTabWithRows(t, h)
	h.exportMenu = &activeExportMenu{tab: tab, menu: newExportMenuOnFile("exists.csv")}

	h.ExportMenuConfirm()

	if confirm.Calls() != 1 {
		t.Fatalf("Confirm calls = %d, want 1", confirm.Calls())
	}
	if workerCalled {
		t.Error("worker dispatched on No; want no write")
	}
	if got := readFile(t, target); got != "OLD CONTENT" {
		t.Errorf("file changed on No: %q", got)
	}
}

// TestExportMenuConfirm_ConfirmPrecedesWorker proves the sequencing guarantee:
// when an overwrite confirm fires, "confirm" is recorded before "worker" (the
// confirm runs on the UI thread, the worker only after Yes).
func TestExportMenuConfirm_ConfirmPrecedesWorker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)
	target := filepath.Join(dir, "exists.csv")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	rec := &orderRecorder{}
	confirm := &fakeConfirmer{autoYes: true}
	h, _ := newTestHelper(t, nil)
	h.deps.OnWorker = func(fn func(gocui.Task) error) { rec.add("worker"); _ = fn(nil) }
	h.deps.Confirm = wrapConfirmOrder(confirm, rec)

	tab := seedTabWithRows(t, h)
	h.exportMenu = &activeExportMenu{tab: tab, menu: newExportMenuOnFile("exists.csv")}

	h.ExportMenuConfirm()

	if len(rec.events) < 2 {
		t.Fatalf("events = %v, want at least [confirm worker]", rec.events)
	}
	if rec.events[0] != "confirm" || rec.events[1] != "worker" {
		t.Errorf("event order = %v, want confirm before worker", rec.events)
	}
}

// TestExportMenuConfirm_EmptyPath_ToastsNoWrite verifies an empty File path
// (the user cleared it via the 'i' edit prompt) is rejected with a toast and
// aborts before any write: no file/.partial lands in the download dir, the
// worker is never dispatched, and no overwrite Confirm fires.
func TestExportMenuConfirm_EmptyPath_ToastsNoWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DOWNLOAD_DIR", dir)

	toast := &fakeToaster{}
	confirm := &fakeConfirmer{autoYes: true}
	workerCalled := false
	h, _ := newTestHelper(t, nil)
	h.deps.Toast = toast
	h.deps.OnWorker = func(fn func(gocui.Task) error) { workerCalled = true; _ = fn(nil) }
	h.deps.Confirm = confirm

	tab := seedTabWithRows(t, h)
	h.exportMenu = &activeExportMenu{tab: tab, menu: newExportMenuOnFile("")}

	h.ExportMenuConfirm()

	if last := toast.Last(); !strings.Contains(last, "empty") {
		t.Errorf("toast %q does not surface the empty-path error", last)
	}
	if workerCalled {
		t.Error("worker dispatched on empty path; want abort with no write")
	}
	if confirm.Calls() != 0 {
		t.Errorf("Confirm calls = %d, want 0 (no overwrite prompt on empty path)", confirm.Calls())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read download dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("download dir not empty: %v (stray file/.partial written)", entries)
	}
}

// wrapConfirmOrder wraps a fakeConfirmer so it records "confirm" in the shared
// order recorder before delegating, letting tests assert confirm-before-worker.
func wrapConfirmOrder(inner *fakeConfirmer, rec *orderRecorder) confirmer {
	return &orderConfirmer{inner: inner, rec: rec}
}

type orderConfirmer struct {
	inner *fakeConfirmer
	rec   *orderRecorder
}

func (c *orderConfirmer) Confirm(title, body string, onYes, onNo func() error) error {
	c.rec.add("confirm")
	return c.inner.Confirm(title, body, onYes, onNo)
}
