package controllers_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

type reviewPickerFs struct {
	afero.Fs
	target  string
	statErr error
	openErr error
}

func (fs *reviewPickerFs) LstatIfPossible(path string) (os.FileInfo, bool, error) {
	return fs.Fs.(afero.Lstater).LstatIfPossible(path)
}

func (fs *reviewPickerFs) Stat(path string) (os.FileInfo, error) {
	if path == fs.target && fs.statErr != nil {
		return nil, fs.statErr
	}
	return fs.Fs.Stat(path)
}

func (fs *reviewPickerFs) Open(path string) (afero.File, error) {
	if path == fs.target && fs.openErr != nil {
		return nil, fs.openErr
	}
	return fs.Fs.Open(path)
}

type reviewPickerDriver struct {
	types.GuiDriver
	content string
}

func (d *reviewPickerDriver) SetContent(_ string, content string) error {
	d.content = content
	return nil
}

func (d *reviewPickerDriver) SetCaretEnabled(bool) {}

func (d *reviewPickerDriver) SetViewCursor(string, int, int) error { return nil }

func TestReview_picker_links_controller(t *testing.T) {
	for _, scenario := range []string{"directory", "file", "relative_directory", "relative_file", "dangling", "stat_denied", "directory_read_denied", "disappeared_directory", "disappeared_file", "retry_after_stat_denied", "save_new_filename", "save_file_link", "save_directory_link", "save_dangling_link", "save_disappeared_link", "unsafe_file_error", "unsafe_directory_error"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			listing := filepath.Join(root, "listing")
			target := filepath.Join(root, "target.sql")
			link := filepath.Join(listing, "link.sql")
			require.NoError(t, os.Mkdir(listing, 0o700))
			isDir := scenario == "directory" || scenario == "relative_directory" || scenario == "directory_read_denied" || scenario == "disappeared_directory" || scenario == "save_directory_link" || scenario == "unsafe_directory_error"
			if isDir {
				require.NoError(t, os.Mkdir(target, 0o700))
			} else if scenario != "dangling" && scenario != "save_dangling_link" {
				require.NoError(t, os.WriteFile(target, []byte("select 1;"), 0o600))
			}
			linkTarget := target
			if scenario == "relative_directory" || scenario == "relative_file" {
				linkTarget = "../target.sql"
			}
			require.NoError(t, os.Symlink(linkTarget, link))
			fs := &reviewPickerFs{Fs: afero.NewOsFs(), target: link}
			if scenario == "stat_denied" || scenario == "retry_after_stat_denied" {
				fs.statErr = os.ErrPermission
			}
			if scenario == "directory_read_denied" {
				fs.openErr = os.ErrPermission
			}
			driver := &reviewPickerDriver{}
			base := guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.FILE_PICKER, ViewName: string(types.FILE_PICKER), Kind: types.TEMPORARY_POPUP})
			fp := guicontext.NewFilePickerContext(base, guicontext.Deps{GuiDriver: driver})
			fp.SetFs(fs)
			fp.SetViewSize(500, 20)
			var confirmed string
			var confirms, cancels, pops int
			mode := guicontext.PickerOpen
			start := listing
			switch scenario {
			case "save_new_filename":
				mode = guicontext.PickerSave
				start = filepath.Join(listing, "new.sql")
			case "save_file_link", "save_directory_link", "save_dangling_link", "save_disappeared_link":
				mode = guicontext.PickerSave
				start = filepath.Join(listing, "new.sql")
			}
			fp.Push(mode, start, func(path string) { require.Equal(t, 1, pops); confirmed = path; confirms++ }, func() { cancels++ }, func() error { pops++; return nil })
			if mode == guicontext.PickerSave {
				fp.ToggleInputFocus()
				if scenario != "save_new_filename" {
					fp.SetFilename(filepath.Base(link))
				}
			}
			if scenario == "disappeared_directory" || scenario == "disappeared_file" {
				require.NoError(t, os.Remove(target))
			}
			if scenario == "unsafe_file_error" || scenario == "unsafe_directory_error" {
				fs.statErr = errors.New("synthetic\x1b]0;title\a\n\tfailure")
			}
			if scenario == "save_disappeared_link" {
				require.NoError(t, os.Remove(link))
			}
			ctrl := controllers.NewFilePickerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, func() *guicontext.FilePickerContext { return fp })
			reg := commands.NewRegistry()
			ctrl.RegisterActions(reg)
			cmd, ok := reg.Get(commands.FilePickerConfirm)
			require.True(t, ok)
			require.NoError(t, cmd.Handler(commands.ExecCtx{}))
			switch scenario {
			case "directory", "relative_directory", "save_directory_link":
				require.Equal(t, link, fp.CurrentPath())
				require.Contains(t, driver.content, link)
				require.Zero(t, confirms)
				require.Zero(t, pops)
			case "file", "relative_file", "save_new_filename", "save_file_link":
				want := link
				if scenario == "save_new_filename" {
					want = start
				}
				require.Equal(t, want, confirmed)
				require.Equal(t, 1, confirms)
				require.Equal(t, 1, pops)
			default:
				require.Zero(t, confirms)
				require.Zero(t, pops)
				require.Contains(t, driver.content, "error:")
				if scenario == "directory_read_denied" {
					require.Contains(t, driver.content, "Read error:")
				} else {
					require.Equal(t, listing, fp.CurrentPath())
					require.Contains(t, driver.content, "Link error:")
				}
				if scenario == "unsafe_file_error" || scenario == "unsafe_directory_error" {
					require.NotContains(t, driver.content, "\x1b]0;title")
					require.Contains(t, driver.content, "Link error: synthetic failure")
				}
			}
			require.Zero(t, cancels)
			if scenario == "retry_after_stat_denied" {
				fs.statErr = nil
				require.NoError(t, cmd.Handler(commands.ExecCtx{}))
				require.Equal(t, link, confirmed)
				require.Equal(t, 1, confirms)
				require.Equal(t, 1, pops)
			}
		})
	}
}

func TestFilePickerActionsKeepSaveDestination(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/exports", 0o700))
	require.NoError(t, afero.WriteFile(fs, "/exports/notes.sql", []byte("select 1;"), 0o600))
	driver := &reviewPickerDriver{}
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.FILE_PICKER, ViewName: string(types.FILE_PICKER), Kind: types.TEMPORARY_POPUP})
	fp := guicontext.NewFilePickerContext(base, guicontext.Deps{GuiDriver: driver})
	fp.SetFs(fs)
	fp.SetViewSize(80, 20)
	var events []string
	fp.Push(guicontext.PickerSave, "/exports/report.csv", func(path string) {
		events = append(events, "confirm:"+path)
	}, func() { events = append(events, "cancel") }, func() error {
		events = append(events, "pop")
		return nil
	})
	view := gocui.NewView(string(types.FILE_PICKER), 0, 0, 80, 20, gocui.OutputNormal)
	fp.SetView(view)
	ctrl := controllers.NewFilePickerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, func() *guicontext.FilePickerContext { return fp })
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	run := func(id string) {
		t.Helper()
		cmd, ok := reg.Get(id)
		require.True(t, ok)
		require.NoError(t, cmd.Handler(commands.ExecCtx{}))
	}
	typeText := func(text string) {
		view.TextArea.Clear()
		view.TextArea.TypeString(text)
	}
	run(commands.FilePickerFocusInput)
	typeText("edited.csv")
	run(commands.FilePickerSearch)
	require.Empty(t, fp.Buffer())
	typeText("notes")
	run(commands.FilePickerConfirm)
	require.Equal(t, "edited.csv", fp.Buffer())
	_, total, _ := fp.SearchStatus()
	require.Equal(t, 1, total)
	run(commands.FilePickerNewDir)
	require.Empty(t, fp.Buffer())
	typeText("cancelled")
	run(commands.FilePickerFocusInput)
	exists, err := afero.Exists(fs, "/exports/cancelled")
	require.NoError(t, err)
	require.False(t, exists)
	run(commands.FilePickerNewDir)
	require.Empty(t, fp.Buffer())
	typeText("nested")
	run(commands.FilePickerConfirm)
	info, err := fs.Stat("/exports/nested")
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	require.Empty(t, events)
	for i, item := range fp.Items() {
		if item.Name == "nested" {
			fp.SetCursor(i)
		}
	}
	run(commands.FilePickerConfirm)
	require.Equal(t, "/exports/nested", fp.CurrentPath())
	run(commands.FilePickerAscend)
	require.Equal(t, "/exports", fp.CurrentPath())
	run(commands.FilePickerSearch)
	typeText("discard")
	run(commands.FilePickerFocusInput)
	require.False(t, fp.SearchActive())
	require.Empty(t, events)
	run(commands.FilePickerFocusInput)
	require.Equal(t, "edited.csv", fp.Buffer())
	run(commands.FilePickerConfirm)
	require.Equal(t, []string{"pop", "confirm:/exports/edited.csv"}, events)
	content, err := afero.ReadFile(fs, "/exports/notes.sql")
	require.NoError(t, err)
	require.Equal(t, "select 1;", string(content))
	exists, err = afero.Exists(fs, "/exports/edited.csv")
	require.NoError(t, err)
	require.False(t, exists, "picker confirmation must not write the destination")
}
