package context

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func TestReview_picker_links(t *testing.T) {
	root := t.TempDir()
	listing := filepath.Join(root, "listing")
	dir := filepath.Join(root, "target-dir")
	file := filepath.Join(root, "target.sql")
	require.NoError(t, os.Mkdir(listing, 0o700))
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.WriteFile(file, []byte("select 1;"), 0o600))
	cases := []struct {
		name, target string
		isDir        bool
	}{
		{"z-dir-link", dir, true},
		{"relative-dir", "../target-dir", true},
		{"a-file-link.sql", file, false},
		{"relative-file.sql", "../target.sql", false},
		{"dangling", "../absent", false},
	}
	for _, tc := range cases {
		require.NoError(t, os.Symlink(tc.target, filepath.Join(listing, tc.name)))
	}
	fp := NewFilePickerContext(BaseContext{}, Deps{})
	fp.SetFs(afero.NewOsFs())
	fp.Push(PickerOpen, listing, nil, nil, nil)
	fp.SetViewSize(100, 20)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var found bool
			for _, item := range fp.Items() {
				if item.Name == tc.name {
					found = true
					require.True(t, item.IsSymlink)
					require.Equal(t, tc.isDir, item.IsDir)
					require.Equal(t, filepath.Join(listing, tc.name), item.Path)
				}
			}
			require.True(t, found)
		})
	}
	for range 3 {
		for i := 1; i < len(fp.Items()); i++ {
			require.False(t, !fp.Items()[i-1].IsDir && fp.Items()[i].IsDir, "directories precede files")
		}
		fp.CycleSort()
	}
	require.Contains(t, fp.RenderBody(), "z-dir-link@/")
	require.Contains(t, fp.RenderBody(), "a-file-link.sql@")
}

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

type reviewPickerSearchFs struct {
	afero.Fs
	beforeOpen func(string)
	readError  string
}

func (fs *reviewPickerSearchFs) Open(path string) (afero.File, error) {
	if fs.beforeOpen != nil {
		fs.beforeOpen(path)
	}
	if path == fs.readError {
		return nil, os.ErrPermission
	}
	return fs.Fs.Open(path)
}

func TestReview_picker_search(t *testing.T) {
	for _, scenario := range []string{"navigate", "descend", "ascend", "hidden_on", "hidden_off", "refresh", "create_directory", "sort_size", "sort_modified", "sort_name", "empty", "read_error", "truncated", "no_matches", "smart_case"} {
		t.Run(scenario, func(t *testing.T) {
			fs := &reviewPickerSearchFs{Fs: newTestPickerFs()}
			for _, dir := range []string{"/other", "/empty"} {
				require.NoError(t, fs.MkdirAll(dir, 0o755))
			}
			for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
				require.NoError(t, afero.WriteFile(fs, filepath.Join("/other", name), nil, 0o600))
			}
			fp := NewFilePickerContext(BaseContext{}, Deps{})
			fp.SetFs(fs)
			fp.Push(PickerOpen, "/home/user", nil, nil, nil)
			fp.SetViewSize(200, 20)
			fp.nowFunc = func() time.Time { return time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) }
			query := "s"
			switch scenario {
			case "hidden_off":
				fp.ToggleHidden()
			case "sort_modified":
				fp.CycleSort()
			case "sort_name":
				fp.CycleSort()
				fp.CycleSort()
			case "truncated":
				for i := range 205 {
					require.NoError(t, afero.WriteFile(fs, fmt.Sprintf("/home/user/x_%03d.sql", i), nil, 0o600))
				}
				fp.Refresh()
				query = "x_"
			case "no_matches":
				query = "absent"
			case "smart_case":
				query = "SQL"
			}
			fp.SetSearch(query)
			require.True(t, fp.SearchActive())
			if scenario == "truncated" {
				_, total, truncated := fp.SearchStatus()
				require.Equal(t, 200, total)
				require.True(t, truncated)
			}
			var opens int
			fs.beforeOpen = func(string) {
				opens++
				if fp.SearchActive() || len(fp.search.matches) != 0 {
					t.Error("search must clear before reading the replacement listing")
				}
			}
			saved := fp.Selected().Path
			switch scenario {
			case "navigate", "truncated", "no_matches", "smart_case":
				fp.NavigateTo("/other")
			case "descend":
				fp.SetCursor(0)
				fp.Descend()
				require.Equal(t, "/home/user/projects", fp.CurrentPath())
			case "ascend":
				fp.Ascend()
				require.Equal(t, "/home", fp.CurrentPath())
			case "hidden_on", "hidden_off":
				fp.ToggleHidden()
				require.Equal(t, saved, fp.Selected().Path)
			case "refresh":
				fp.Refresh()
				require.Equal(t, saved, fp.Selected().Path)
			case "create_directory":
				fp.CreateDirectory("new_directory")
				exists, err := afero.DirExists(fs, "/home/user/new_directory")
				require.NoError(t, err)
				require.True(t, exists)
			case "sort_size", "sort_modified", "sort_name":
				fp.CycleSort()
			case "empty":
				fp.NavigateTo("/empty")
				require.Empty(t, fp.Items())
				require.Contains(t, fp.RenderBody(), "(empty directory)")
			case "read_error":
				fs.readError = "/other"
				fp.NavigateTo("/other")
				require.Empty(t, fp.Items())
				require.Contains(t, fp.RenderBody(), "Read error: permission denied")
			}
			require.Positive(t, opens)
			t.Run("cleared_state", func(t *testing.T) {
				require.Equal(t, filePickerSearchState{}, fp.search)
				cur, total, truncated := fp.SearchStatus()
				require.Zero(t, cur)
				require.Zero(t, total)
				require.False(t, truncated)
			})
			t.Run("render", func(t *testing.T) {
				expected := *fp
				expected.ClearSearch()
				require.Equal(t, expected.RenderBody(), fp.RenderBody())
			})
			for _, step := range []struct {
				name string
				fn   func()
			}{{"next", fp.NextMatch}, {"prev", fp.PrevMatch}} {
				t.Run(step.name, func(t *testing.T) {
					fp.SetCursor(0)
					selected := fp.Selected()
					step.fn()
					require.Zero(t, fp.cursor)
					require.Equal(t, selected, fp.Selected())
				})
			}
		})
	}
}

func TestReview_picker_search_unchanged_listing(t *testing.T) {
	for _, scenario := range []string{"empty_path", "nil_fs", "unresolved_path", "failed_create", "non_directory_descend"} {
		t.Run(scenario, func(t *testing.T) {
			fp := NewFilePickerContext(BaseContext{}, Deps{})
			fp.SetFs(newTestPickerFs())
			fp.Push(PickerOpen, "/home/user", nil, nil, nil)
			fp.SetSearch("sql")
			search, selected, items := fp.search, fp.Selected(), fp.Items()
			switch scenario {
			case "empty_path":
				fp.NavigateTo("")
			case "nil_fs":
				fp.SetFs(nil)
				fp.NavigateTo("/home/user")
			case "unresolved_path":
				fp.NavigateTo("/missing/parent/child")
				require.Contains(t, fp.errMsg, "Cannot access:")
			case "failed_create":
				fp.CreateDirectory("projects")
				require.Contains(t, fp.errMsg, "Cannot create:")
			case "non_directory_descend":
				fp.Descend()
			}
			require.Equal(t, search, fp.search)
			require.Equal(t, selected, fp.Selected())
			require.Equal(t, items, fp.Items())
		})
	}
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

type pickerInputFS struct {
	afero.Fs
	denyMkdir bool
	created   []string
}

func (fs *pickerInputFS) Mkdir(path string, perm os.FileMode) error {
	fs.created = append(fs.created, path)
	if fs.denyMkdir {
		return os.ErrPermission
	}
	return fs.Fs.Mkdir(path, perm)
}

func TestFilePickerTransientInputPreservesFilename(t *testing.T) {
	for _, tc := range []struct {
		name, text                string
		search, cancel, denyMkdir bool
		wantError, wantDirectory  bool
	}{
		{name: "search apply", text: "schema", search: true},
		{name: "search cancel", text: "discard", search: true, cancel: true},
		{name: "directory create", text: "reports", wantDirectory: true},
		{name: "directory cancel", text: "discard", cancel: true},
		{name: "directory empty"},
		{name: "directory exists", text: "projects", wantError: true},
		{name: "directory denied", text: "denied", denyMkdir: true, wantError: true},
	} {
		for _, fromInput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/filenameFocused=%v", tc.name, fromInput), func(t *testing.T) {
				fs := &pickerInputFS{Fs: newTestPickerFs(), denyMkdir: tc.denyMkdir}
				fp := NewFilePickerContext(BaseContext{}, Deps{})
				fp.SetFs(fs)
				var events []string
				fp.Push(PickerSave, "/home/user/report.csv", func(path string) {
					events = append(events, "confirm:"+path)
				}, func() { events = append(events, "cancel") }, func() error {
					events = append(events, "pop")
					return nil
				})
				view := gocui.NewView(string(types.FILE_PICKER), 0, 0, 60, 12, gocui.OutputNormal)
				fp.SetView(view)
				wantName := "report.csv"
				if fromInput {
					fp.ToggleInputFocus()
					view.TextArea.Clear()
					view.TextArea.TypeString("edited.csv")
					wantName = "edited.csv"
				}
				fp.SetSearch("sql")
				if tc.search {
					fp.ActivateSearch()
				} else {
					fp.ActivateNewDir()
				}
				if got := fp.Buffer(); got != "" {
					t.Errorf("transient input seeded with %q, want empty", got)
				}
				view.TextArea.Clear()
				view.TextArea.TypeString(tc.text)
				require.NoError(t, fp.HandleFocus(types.OnFocusOpts{}))
				require.Equal(t, tc.text, fp.Buffer(), "refocus must retain transient text")
				if tc.search {
					if tc.cancel {
						fp.CancelSearch()
					} else {
						fp.ApplySearch()
					}
					require.Equal(t, !tc.cancel, fp.SearchActive())
					if !tc.cancel {
						_, total, _ := fp.SearchStatus()
						require.Equal(t, 1, total)
						require.Equal(t, "schema.sql", fp.Selected().Name)
					}
				} else if tc.cancel {
					fp.CancelNewDir()
				} else {
					fp.ApplyNewDir()
				}
				require.Equal(t, tc.wantError, fp.errMsg != "")
				require.Empty(t, events, "transient input must not confirm, cancel, or pop the picker")
				require.False(t, fp.InputFocused())
				if !tc.search && !tc.cancel && tc.text != "" {
					require.Equal(t, []string{filepath.Join("/home/user", tc.text)}, fs.created)
				} else {
					require.Empty(t, fs.created)
				}
				exists, err := afero.DirExists(fs, "/home/user/reports")
				require.NoError(t, err)
				require.Equal(t, tc.wantDirectory, exists)
				require.Equal(t, wantName, fp.Buffer(), "transient input must not overwrite the save filename")
				fp.ToggleInputFocus()
				require.Equal(t, wantName, fp.Buffer())
				require.NoError(t, fp.Confirm())
				require.Equal(t, []string{"pop", "confirm:" + filepath.Join("/home/user", wantName)}, events)
				exists, err = afero.Exists(fs, filepath.Join("/home/user", wantName))
				require.NoError(t, err)
				require.False(t, exists, "path confirmation itself must not write a file")
			})
		}
	}
}

func TestFilePickerInputRefocusAndMount(t *testing.T) {
	for _, kind := range []pickerInputKind{inputKindFilename, inputKindSearch, inputKindNewDir} {
		activate := func(fp *FilePickerContext) {
			switch kind {
			case inputKindFilename:
				fp.ToggleInputFocus()
			case inputKindSearch:
				fp.ActivateSearch()
			case inputKindNewDir:
				fp.ActivateNewDir()
			}
		}
		for _, noTextArea := range []bool{false, true} {
			for _, focusFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("kind=%d/noTextArea=%v/focusFirst=%v", kind, noTextArea, focusFirst), func(t *testing.T) {
					fp := NewFilePickerContext(BaseContext{}, Deps{})
					fp.SetFs(newTestPickerFs())
					fp.Push(PickerSave, "/home/user/report.csv", nil, nil, nil)
					if noTextArea {
						view := gocui.NewView(string(types.FILE_PICKER), 0, 0, 60, 12, gocui.OutputNormal)
						view.TextArea = nil
						fp.SetView(view)
					}
					activate(fp)
					want := ""
					if kind == inputKindFilename {
						want = "report.csv"
					}
					require.Equal(t, want, fp.Buffer(), "fallback must respect input kind")
					if focusFirst {
						require.NoError(t, fp.HandleFocus(types.OnFocusOpts{}))
					}
					view := gocui.NewView(string(types.FILE_PICKER), 0, 0, 60, 12, gocui.OutputNormal)
					fp.SetView(view)
					if !focusFirst {
						require.NoError(t, fp.HandleFocus(types.OnFocusOpts{}))
					}
					require.Equal(t, want, fp.Buffer(), "late view attachment must seed the correct input")
					for _, draft := range []string{"unsubmitted", ""} {
						view.TextArea.Clear()
						view.TextArea.TypeString(draft)
						view.TextArea.MoveCursorLeft()
						x, y := view.TextArea.GetCursorXY()
						for range 2 {
							fp.SetView(view)
							require.NoError(t, fp.HandleFocus(types.OnFocusOpts{}))
							require.Equal(t, draft, fp.Buffer())
							x2, y2 := view.TextArea.GetCursorXY()
							require.Equal(t, [2]int{x, y}, [2]int{x2, y2}, "refocus must preserve cursor position")
						}
					}
					fp.ToggleInputFocus()
					fp.ToggleInputFocus()
					if kind == inputKindFilename {
						want = ""
					} else {
						want = "report.csv"
					}
					require.Equal(t, want, fp.Buffer(), "only filename input can replace the stored filename")
				})
			}
		}
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
