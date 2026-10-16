//go:build linux || darwin || windows

package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/spf13/afero"
)

func TestMachineProfiles_Allowlist(t *testing.T) {
	profile := models.Connection{Name: "dev", Driver: "postgres", Tags: []string{"tag"}, Label: "label", DSN: "SECRET", Host: "SECRET", User: "SECRET", Password: "SECRET", PasswordCommand: "SECRET", KeyringRef: "SECRET", PgpassPath: "SECRET", SSHTunnel: &models.SSHTunnelConfig{Host: "SECRET"}}
	public := PublicConnection(profile)
	public.Tags[0] = "copy"
	encoded, err := json.Marshal(public)
	if err != nil || strings.Contains(string(encoded), "SECRET") || profile.Tags[0] != "tag" {
		t.Fatal("profile serialization leaked or shares caller data")
	}
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	if len(fields) != 7 || fields["credential_status"] != "not_checked" || fields["cli_access"] != "disabled" {
		t.Fatal("profile allowlist changed")
	}
}

func TestMachineProfiles_RegularFile(t *testing.T) {
	dir := t.TempDir()
	fs := machineProfileFS{afero.NewOsFs()}
	if _, err := fs.Open(dir); !errors.Is(err, os.ErrInvalid) {
		t.Fatal("profile directory did not return invalid file")
	}
	if _, err := fs.Open(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("profile adapter lost not-exist error")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("profile adapter created files")
	}
	for _, fs := range []afero.Fs{afero.NewOsFs(), afero.NewMemMapFs()} {
		path := filepath.Join(dir, "connections λ with spaces.yml")
		if err := fs.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := afero.WriteFile(fs, path, []byte("connections: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := (machineProfileFS{fs}).Open(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil || string(data) != "connections: []\n" {
			t.Fatal("regular profile adapter did not preserve bytes")
		}
	}
}

type machineProfileTestFS struct {
	afero.Fs
	stat func(string) (os.FileInfo, error)
	open func(string, int, os.FileMode) (afero.File, error)
}

func (fs machineProfileTestFS) Stat(name string) (os.FileInfo, error) {
	if fs.stat != nil {
		return fs.stat(name)
	}
	return fs.Fs.Stat(name)
}

func (fs machineProfileTestFS) OpenFile(name string, flags int, mode os.FileMode) (afero.File, error) {
	return fs.open(name, flags, mode)
}

type machineProfileTestFile struct {
	afero.File
	statError error
	closed    bool
}

func (f *machineProfileTestFile) Stat() (os.FileInfo, error) {
	if f.statError != nil {
		return nil, f.statError
	}
	return f.File.Stat()
}

func (f *machineProfileTestFile) Close() error {
	f.closed = true
	return f.File.Close()
}

func TestMachineProfiles_OpenChecks(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := filepath.Join(t.TempDir(), "profile.yml")
	if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(fs, path, []byte("connections: []"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"stat", "open", "opened-stat", "opened-directory"} {
		t.Run(failure, func(t *testing.T) {
			var opened *machineProfileTestFile
			adapter := machineProfileTestFS{Fs: fs}
			if failure == "stat" {
				adapter.stat = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
			}
			adapter.open = func(name string, flags int, mode os.FileMode) (afero.File, error) {
				if flags != machineFileOpenFlags() || mode != 0 {
					t.Fatal("profile open flags changed")
				}
				if failure == "open" {
					return nil, os.ErrPermission
				}
				if failure == "opened-directory" {
					name = filepath.Dir(path)
				}
				file, err := fs.OpenFile(name, flags, mode)
				if err != nil {
					t.Fatal(err)
				}
				opened = &machineProfileTestFile{File: file}
				if failure == "opened-stat" {
					opened.statError = os.ErrPermission
				}
				return opened, nil
			}
			_, err := (machineProfileFS{adapter}).Open(path)
			want := os.ErrPermission
			if strings.HasPrefix(failure, "opened-") {
				want = os.ErrInvalid
			}
			if !errors.Is(err, want) || opened != nil && !opened.closed {
				t.Fatal("profile pre/post-open validation or cleanup changed")
			}
			if failure == "stat" && opened != nil {
				t.Fatal("failed pre-open stat still opened a file")
			}
		})
	}
}
