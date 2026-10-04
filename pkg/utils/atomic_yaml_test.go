package utils

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type renameFailFs struct {
	afero.Fs
}

func (r *renameFailFs) Rename(oldname, newname string) error {
	return errors.New("simulated rename failure")
}

func TestAtomicWriteYAML_HappyPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	type doc struct {
		Name  string `yaml:"name"`
		Count int    `yaml:"count"`
	}
	if err := AtomicWriteYAML(fs, "/a/b/c.yml", doc{Name: "x", Count: 3}, 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := afero.ReadFile(fs, "/a/b/c.yml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "name: x") || !strings.Contains(s, "count: 3") {
		t.Errorf("file body = %q, want fields present", s)
	}
	// tmp must be gone
	if exists, _ := afero.Exists(fs, "/a/b/c.yml.tmp"); exists {
		t.Errorf("tmp file still present after success")
	}
}

func TestAtomicWriteYAML_RenameFailureRemovesTmp(t *testing.T) {
	base := afero.NewMemMapFs()
	fs := &renameFailFs{Fs: base}
	err := AtomicWriteYAML(fs, "/x.yml", map[string]string{"k": "v"}, 0o600)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("error = %q; want it to mention rename", err.Error())
	}
	if exists, _ := afero.Exists(base, "/x.yml.tmp"); exists {
		t.Errorf("tmp file %q still present after rename failure", "/x.yml.tmp")
	}
	if exists, _ := afero.Exists(base, "/x.yml"); exists {
		t.Errorf("final file unexpectedly created after rename failure")
	}
}

func TestAtomicWriteYAML_FileMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := AtomicWriteYAML(fs, "/m.yml", map[string]int{"a": 1}, 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := fs.Stat("/m.yml")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != os.FileMode(0o600) {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

var errReviewYAML = errors.New("synthetic YAML I/O failure")

type reviewYAMLFile struct {
	afero.File
	owner *reviewYAMLFS
}

func (f *reviewYAMLFile) Write(p []byte) (int, error) {
	switch f.owner.fail {
	case "write":
		n, _ := f.File.Write(p[:len(p)/2])
		return n, errReviewYAML
	case "short":
		return f.File.Write(p[:len(p)/2])
	default:
		return f.File.Write(p)
	}
}

func (f *reviewYAMLFile) Close() error {
	f.owner.closed = true
	err := f.File.Close()
	if f.owner.fail == "close" {
		return errReviewYAML
	}
	return err
}

type reviewYAMLFS struct {
	afero.Fs
	fail    string
	tmp     string
	flags   int
	mode    os.FileMode
	closed  bool
	arrived chan string
	release chan struct{}
}

func (f *reviewYAMLFS) OpenFile(name string, flags int, mode os.FileMode) (afero.File, error) {
	f.tmp, f.flags, f.mode = name, flags, mode
	if f.fail == "open" {
		return nil, errReviewYAML
	}
	file, err := f.Fs.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &reviewYAMLFile{File: file, owner: f}, nil
}

func (f *reviewYAMLFS) Rename(oldname, newname string) error {
	if !f.closed {
		return errors.New("rename before close")
	}
	if f.arrived != nil {
		f.arrived <- oldname
		<-f.release
	}
	if f.fail == "rename" {
		return errReviewYAML
	}
	return f.Fs.Rename(oldname, newname)
}

func reviewYAMLWait(t *testing.T, arrived <-chan string) string {
	t.Helper()
	select {
	case name := <-arrived:
		return name
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach rename barrier")
		return ""
	}
}

func reviewYAMLFixture(t *testing.T, native bool) (afero.Fs, string) {
	t.Helper()
	if !native {
		return afero.NewMemMapFs(), "/fixture/state.yml"
	}
	return afero.NewOsFs(), filepath.Join(t.TempDir(), "state.yml")
}

func TestReview_yaml(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "MemMapFs"
		if native {
			name = "OsFs"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("overlap", func(t *testing.T) {
				base, path := reviewYAMLFixture(t, native)
				writers := make([]*reviewYAMLFS, 2)
				results := make([]chan error, 2)
				done := make([]chan struct{}, 2)
				docs := []map[string]string{{"writer": "one", "payload": strings.Repeat("a", 4096)}, {"writer": "two", "payload": strings.Repeat("b", 8192)}}
				for i := range writers {
					writers[i] = &reviewYAMLFS{Fs: base, arrived: make(chan string, 1), release: make(chan struct{})}
					results[i] = make(chan error, 1)
					done[i] = make(chan struct{})
					go func() {
						defer close(done[i])
						results[i] <- AtomicWriteYAML(writers[i], path, docs[i], 0o640)
					}()
					// Defer releases even when an assertion fails at the barrier.
					defer func() {
						select {
						case <-writers[i].release:
						default:
							close(writers[i].release)
						}
						<-done[i]
					}()
				}
				temps := []string{reviewYAMLWait(t, writers[0].arrived), reviewYAMLWait(t, writers[1].arrived)}
				// Check each publication, not merely whichever writer wins last.
				for i := range writers {
					close(writers[i].release)
					require.NoError(t, <-results[i])
					data, err := afero.ReadFile(base, path)
					require.NoError(t, err)
					var got map[string]string
					require.NoError(t, yaml.Unmarshal(data, &got))
					require.Equal(t, docs[i], got)
					require.Equal(t, filepath.Dir(path), filepath.Dir(temps[i]))
					require.NotZero(t, writers[i].flags&os.O_EXCL)
					require.Equal(t, os.FileMode(0o640), writers[i].mode)
				}
				require.NotEqual(t, temps[0], temps[1])
				entries, err := afero.ReadDir(base, filepath.Dir(path))
				require.NoError(t, err)
				require.Len(t, entries, 1)
			})
			for _, phase := range []string{"open", "write", "short", "close", "rename"} {
				t.Run(phase, func(t *testing.T) {
					base, path := reviewYAMLFixture(t, native)
					require.NoError(t, base.MkdirAll(filepath.Dir(path), 0o700))
					prior := []byte("prior: intact\n")
					require.NoError(t, afero.WriteFile(base, path, prior, 0o600))
					// A sibling writer and the old fixed temp name are not ours.
					require.NoError(t, afero.WriteFile(base, path+".tmp", []byte("untouched"), 0o600))
					survivor := &reviewYAMLFS{Fs: base, arrived: make(chan string, 1), release: make(chan struct{})}
					result := make(chan error, 1)
					done := make(chan struct{})
					go func() {
						defer close(done)
						result <- AtomicWriteYAML(survivor, path, map[string]string{"survivor": "intact"}, 0o600)
					}()
					defer func() {
						select {
						case <-survivor.release:
						default:
							close(survivor.release)
						}
						<-done
					}()
					tmp := reviewYAMLWait(t, survivor.arrived)
					failed := &reviewYAMLFS{Fs: base, fail: phase}
					err := AtomicWriteYAML(failed, path, map[string]string{"failed": "partial"}, 0o600)
					if phase == "short" {
						require.ErrorIs(t, err, io.ErrShortWrite)
					} else {
						require.ErrorIs(t, err, errReviewYAML)
					}
					data, err := afero.ReadFile(base, path)
					require.NoError(t, err)
					require.Equal(t, prior, data)
					data, err = afero.ReadFile(base, path+".tmp")
					require.NoError(t, err)
					require.Equal(t, "untouched", string(data))
					exists, err := afero.Exists(base, tmp)
					require.NoError(t, err)
					require.True(t, exists, "sibling's temporary file must survive")
					if phase != "open" {
						require.True(t, failed.closed)
						exists, err = afero.Exists(base, failed.tmp)
						require.NoError(t, err)
						require.False(t, exists, "failed writer's temporary file must be removed")
					}
					close(survivor.release)
					require.NoError(t, <-result)
					data, err = afero.ReadFile(base, path)
					require.NoError(t, err)
					require.Equal(t, "survivor: intact\n", string(data))
				})
			}
		})
	}
	t.Run("directory_modes_and_custom_root", func(t *testing.T) {
		base := afero.NewMemMapFs()
		require.NoError(t, base.MkdirAll("/root/existing", 0o751))
		fs := afero.NewBasePathFs(base, "/root")
		for _, path := range []string{"existing/doc.yml", "new/nested/doc.yml"} {
			require.NoError(t, AtomicWriteYAML(fs, path, map[string]int{"value": 1}, 0o640))
			info, err := fs.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
		}
		for path, mode := range map[string]os.FileMode{"existing": 0o751, "new": 0o700, "new/nested": 0o700} {
			info, err := fs.Stat(path)
			require.NoError(t, err)
			require.Equal(t, mode, info.Mode().Perm())
		}
	})
}

type reviewYAMLCollisionFS struct {
	afero.Fs
	remaining int
	collided  []string
}

func (f *reviewYAMLCollisionFS) OpenFile(path string, flags int, mode os.FileMode) (afero.File, error) {
	if f.remaining > 0 {
		f.remaining--
		// Simulate a different owner claiming the random name before OpenFile.
		if err := afero.WriteFile(f.Fs, path, []byte("other owner"), 0o600); err != nil {
			return nil, err
		}
		f.collided = append(f.collided, path)
		return nil, fmt.Errorf("wrapped collision: %w", os.ErrExist)
	}
	return f.Fs.OpenFile(path, flags, mode)
}

func TestReview_yaml_collisions(t *testing.T) {
	for _, collisions := range []int{1, 10000} {
		t.Run(fmt.Sprint(collisions), func(t *testing.T) {
			base := afero.NewMemMapFs()
			prior := []byte("prior: intact\n")
			require.NoError(t, afero.WriteFile(base, "/state.yml", prior, 0o600))
			fs := &reviewYAMLCollisionFS{Fs: base, remaining: collisions}
			err := AtomicWriteYAML(fs, "/state.yml", map[string]string{"new": "complete"}, 0o600)
			if collisions == 1 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrExist)
				require.Less(t, len(fs.collided), collisions, "collision retry must be bounded")
			}
			for _, path := range fs.collided {
				data, err := afero.ReadFile(base, path)
				require.NoError(t, err)
				require.Equal(t, "other owner", string(data))
			}
			data, err := afero.ReadFile(base, "/state.yml")
			require.NoError(t, err)
			if collisions == 1 {
				require.Equal(t, "new: complete\n", string(data))
			} else {
				require.Equal(t, prior, data)
			}
		})
	}
}
