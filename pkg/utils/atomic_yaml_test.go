package utils

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
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
