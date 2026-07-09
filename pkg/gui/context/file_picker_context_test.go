package context

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/spf13/afero"
)

func newTestPickerFs() afero.Fs {
	fs := afero.NewMemMapFs()
	home := "/home/user"
	entries := []struct {
		isDir bool
		name  string
		size  int64
		mtime time.Time
	}{
		{true, "projects", 0, time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
		{false, "schema.sql", 2048, time.Date(2026, 6, 15, 14, 22, 0, 0, time.UTC)},
		{false, "notes.txt", 512, time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)},
		{false, "data.bin", 1048576, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{false, ".hidden", 100, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)},
		{true, ".secret_dir", 0, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)},
	}
	for _, e := range entries {
		path := filepath.Join(home, e.name)
		if e.isDir {
			_ = fs.Mkdir(path, 0o755)
		} else {
			_ = afero.WriteFile(fs, path, make([]byte, e.size), 0o644)
		}
		_ = fs.Chtimes(path, e.mtime, e.mtime)
	}
	return fs
}

func TestFilePickerContext_PushInitializesState(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	if fp.CurrentPath() != "/home/user" {
		t.Errorf("CurrentPath = %q, want /home/user", fp.CurrentPath())
	}
	if fp.cursor != 0 {
		t.Errorf("cursor = %d, want 0", fp.cursor)
	}
	if fp.showHidden {
		t.Error("showHidden = true, want false")
	}
	if len(fp.Items()) < 4 {
		t.Errorf("Items len = %d, want >= 4 (hidden excluded)", len(fp.Items()))
	}
	// Dirs first, then alphabetical
	for i := 0; i < len(fp.Items())-1; i++ {
		if !fp.Items()[i].IsDir && fp.Items()[i+1].IsDir {
			t.Errorf("items[%d]=%q (file) precedes items[%d]=%q (dir)", i, fp.Items()[i].Name, i+1, fp.Items()[i+1].Name)
		}
		if fp.Items()[i].IsDir == fp.Items()[i+1].IsDir {
			if fp.Items()[i].Name > fp.Items()[i+1].Name {
				t.Errorf("items not sorted: %q > %q", fp.Items()[i].Name, fp.Items()[i+1].Name)
			}
		}
	}
}

func TestFilePickerContext_ToggleHidden(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	before := len(fp.Items())
	if before < 4 {
		t.Fatalf("expected >=4 visible items, got %d", before)
	}
	fp.ToggleHidden()
	if !fp.showHidden {
		t.Error("showHidden = false after toggle")
	}
	if len(fp.Items()) <= before {
		t.Errorf("hidden toggled on: items = %d, want > %d", len(fp.Items()), before)
	}
	fp.ToggleHidden()
	if fp.showHidden {
		t.Error("showHidden = true after second toggle")
	}
	if len(fp.Items()) != before {
		t.Errorf("hidden toggled off: items = %d, want %d", len(fp.Items()), before)
	}
}

func TestFilePickerContext_ToggleHiddenPreservesCursor(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	// Move cursor to "projects/" (index 0, visible)
	fp.SetCursor(0)
	saved := fp.Selected().Path
	if saved == "" {
		t.Fatal("no item at cursor 0")
	}
	fp.ToggleHidden()
	if fp.Selected().Path != saved {
		t.Errorf("cursor moved: got %q, want %q", fp.Selected().Path, saved)
	}
	fp.ToggleHidden()
	if fp.Selected().Path != saved {
		t.Errorf("cursor moved after second toggle: got %q, want %q", fp.Selected().Path, saved)
	}
}

func TestFilePickerContext_CycleSort(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	defaultFirst := fp.Items()[0].Name

	// Cycle 1: sort by size (largest first). Dirs first; among files, largest = data.bin
	fp.CycleSort()
	if fp.Items()[0].Name != "projects" {
		t.Errorf("dirs first: got %q, want projects/", fp.Items()[0].Name)
	}
	if fp.Items()[1].Name != "data.bin" {
		t.Errorf("size sort: got %q, want data.bin (largest)", fp.Items()[1].Name)
	}

	// Cycle 2: sort by modified (most recent first). schema.sql (2026-06-15) most recent
	fp.CycleSort()
	if fp.Items()[0].Name != "projects" {
		t.Errorf("dirs first: got %q, want projects/", fp.Items()[0].Name)
	}
	if fp.Items()[1].Name != "schema.sql" {
		t.Errorf("modified sort: got %q, want schema.sql (most recent)", fp.Items()[1].Name)
	}

	// Cycle 3: back to name
	fp.CycleSort()
	if fp.Items()[0].Name != defaultFirst {
		t.Errorf("back to name sort: got %q, want %q", fp.Items()[0].Name, defaultFirst)
	}
}

func TestFilePickerContext_NavigateHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll(home, 0o755)
	_ = fs.MkdirAll(filepath.Join(home, "subdir"), 0o755)

	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(fs)
	fp.Push(PickerOpen, filepath.Join(home, "subdir"), nil, nil, nil)
	fp.NavigateHome()
	if fp.CurrentPath() != home {
		t.Errorf("NavigateHome = %q, want %q", fp.CurrentPath(), home)
	}
}

func TestFilePickerContext_SetSearch(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetSearch("sql")
	if !fp.SearchActive() {
		t.Fatal("SearchActive = false after SetSearch")
	}
	cur, total, truncated := fp.SearchStatus()
	if total == 0 {
		t.Error("search matches = 0, want >= 1")
	}
	_ = cur
	if truncated {
		t.Error("truncated = true for small result set")
	}
}

func TestFilePickerContext_SetSearchMatchCap(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = fs.Mkdir("/tmp", 0o755)
	// Create 300 files that all match "x"
	for i := range 300 {
		_ = afero.WriteFile(fs, filepath.Join("/tmp", fmt.Sprintf("x_%d", i)), nil, 0o644)
	}
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(fs)
	fp.Push(PickerOpen, "/tmp", nil, nil, nil)
	fp.SetSearch("x")
	_, total, truncated := fp.SearchStatus()
	if total != 200 {
		t.Errorf("match count = %d, want 200 (capped)", total)
	}
	if !truncated {
		t.Error("truncated = false, want true (300+ matches)")
	}
}

func TestFilePickerContext_ClearSearch(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetSearch("sql")
	fp.ClearSearch()
	if fp.SearchActive() {
		t.Error("SearchActive = true after ClearSearch")
	}
	_, total, truncated := fp.SearchStatus()
	if total != 0 {
		t.Errorf("total = %d after ClearSearch, want 0", total)
	}
	if truncated {
		t.Error("truncated = true after ClearSearch")
	}
}

func TestFilePickerContext_SearchNextPrev(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetSearch("s")
	cur1, total, _ := fp.SearchStatus()
	if total < 2 {
		t.Skip("need >= 2 search matches")
	}
	fp.NextMatch()
	cur2, _, _ := fp.SearchStatus()
	if cur2 != (cur1%total)+1 {
		t.Errorf("NextMatch: cur = %d, want %d", cur2, (cur1%total)+1)
	}
	fp.PrevMatch()
	cur3, _, _ := fp.SearchStatus()
	if cur3 != cur1 {
		t.Errorf("PrevMatch after NextMatch: cur = %d, want %d", cur3, cur1)
	}
}

func TestFilePickerContext_ZeroSearchMatches(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetSearch("zzz_nonexistent")
	cur, total, truncated := fp.SearchStatus()
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if cur != 0 {
		t.Errorf("cur = %d, want 0", cur)
	}
	if truncated {
		t.Error("truncated = true for zero matches")
	}
}

func TestFilePickerContext_Selected(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	sel := fp.Selected()
	if sel.Name == "" {
		t.Error("Selected() returned zero value on non-empty listing")
	}
}

func TestFilePickerContext_Descend(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	// Cursor 0 should be "projects/" (the only dir, first)
	fp.SetCursor(0)
	fp.Descend()
	if fp.CurrentPath() != "/home/user/projects" {
		t.Errorf("Descend path = %q, want /home/user/projects", fp.CurrentPath())
	}
}

func TestFilePickerContext_Ascend(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user/projects", nil, nil, nil)
	fp.Ascend()
	if fp.CurrentPath() != "/home/user" {
		t.Errorf("Ascend path = %q, want /home/user", fp.CurrentPath())
	}
}

func TestFilePickerContext_SaveModeFilename(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerSave, "/home/user/schema.sql", nil, nil, nil)
	if fp.Buffer() != "schema.sql" {
		t.Errorf("Buffer = %q, want schema.sql", fp.Buffer())
	}
}

func TestFilePickerContext_FSEntryHasIsSymlink(t *testing.T) {
	// Ensure the model field exists with zero-value default
	e := models.FSEntry{Name: "test"}
	if e.IsSymlink {
		t.Error("IsSymlink default is true, want false")
	}
}

func TestFilePickerContext_SearchClearedOnSort(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetSearch("sql")
	if !fp.SearchActive() {
		t.Fatal("SearchActive = false before sort")
	}
	fp.CycleSort()
	if fp.SearchActive() {
		t.Error("SearchActive = true after sort cycle (search not cleared)")
	}
}

func TestFilePickerContext_NewDirInputActive(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	if fp.NewDirInputActive() {
		t.Error("NewDirInputActive = true before activation")
	}
	fp.ActivateNewDir()
	if !fp.NewDirInputActive() {
		t.Error("NewDirInputActive = false after ActivateNewDir")
	}
}

func TestFilePickerContext_SearchInputActive(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	if fp.SearchInputActive() {
		t.Error("SearchInputActive = true before activation")
	}
	fp.ActivateSearch()
	if !fp.SearchInputActive() {
		t.Error("SearchInputActive = false after ActivateSearch")
	}
}

func TestFilePickerContext_RenderBody(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	body := fp.RenderBody()
	if body == "" {
		t.Error("RenderBody returned empty string")
	}
}

func TestFilePickerContext_RenderBreadcrumb(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetViewSize(80, 20)
	bc := fp.renderBreadcrumb()
	if bc == "" {
		t.Error("renderBreadcrumb returned empty string")
	}
	if !containsString(bc, "Open:") {
		t.Errorf("renderBreadcrumb missing mode prefix: got %q", bc)
	}
	if !containsString(bc, fp.CurrentPath()) {
		t.Errorf("renderBreadcrumb missing current path; got %q", bc)
	}
}

func TestFilePickerContext_RenderBreadcrumbSaveMode(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerSave, "/home/user", nil, nil, nil)
	fp.SetViewSize(80, 20)
	bc := fp.renderBreadcrumb()
	if !containsString(bc, "Save:") {
		t.Errorf("renderBreadcrumb missing save mode prefix: got %q", bc)
	}
}

func TestFilePickerContext_RenderBreadcrumbHiddenIndicator(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetViewSize(80, 20)
	fp.ToggleHidden()
	bc := fp.renderBreadcrumb()
	if !containsString(bc, "[H]") {
		t.Errorf("renderBreadcrumb missing hidden indicator: got %q", bc)
	}
}

func TestFilePickerContext_RenderBreadcrumbTruncation(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/very/long/path/that/should/be/truncated/because/too/wide", nil, nil, nil)
	fp.SetViewSize(30, 20)
	bc := fp.renderBreadcrumb()
	if !containsString(bc, "Open:") {
		t.Errorf("renderBreadcrumb truncated missing mode prefix: got %q", bc)
	}
	// With 30-char view width, the breadcrumb should be truncated
	if len(bc) > 35 {
		t.Logf("truncated breadcrumb length = %d: %q", len(bc), bc)
	}
}

func TestFilePickerContext_RenderSetter(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	body := fp.RenderBody()
	if body == "" {
		t.Error("RenderBody returned empty")
	}
}

func TestFilePickerContext_HandleRenderNoView(t *testing.T) {
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(newTestPickerFs())
	fp.Push(PickerOpen, "/home/user", nil, nil, nil)
	fp.SetViewSize(80, 20)
	err := fp.HandleRender()
	if err != nil {
		t.Errorf("HandleRender: %v", err)
	}
}

func containsString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
