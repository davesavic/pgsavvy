package exporter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileDest_WritesPartialThenRenames(t *testing.T) {
	dir := t.TempDir()
	d := NewFileDest(dir, "out.csv")
	wc, descriptor, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := filepath.Join(dir, "out.csv")
	if descriptor != want {
		t.Fatalf("descriptor=%q want=%q", descriptor, want)
	}
	if _, err := wc.Write([]byte("hello,world\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Final exists.
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("final not found: %v", err)
	}
	// .partial does not.
	if _, err := os.Stat(want + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial still exists: err=%v", err)
	}
}

func TestFileDest_RefusesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	d := NewFileDest(dir, filepath.Join("..", "escape.csv"))
	if _, _, err := d.Open(); err == nil {
		t.Fatal("expected error for traversal filename")
	}
}

func TestFileDest_AbortRemovesPartial(t *testing.T) {
	dir := t.TempDir()
	d := NewFileDest(dir, "out.csv").(*fileDest)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("partial-data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Abort()
	final := filepath.Join(dir, "out.csv")
	if _, err := os.Stat(final + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial still exists after Abort: err=%v", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final unexpectedly exists after Abort: err=%v", err)
	}
}

func TestFileDest_FileMode0o600(t *testing.T) {
	dir := t.TempDir()
	d := NewFileDest(dir, "out.csv")
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "out.csv"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm() & 0o777; got != 0o600 {
		t.Fatalf("mode=%o want=0600", got)
	}
}

func TestFileDest_RejectsAbsolutePathInFilename(t *testing.T) {
	dir := t.TempDir()
	d := NewFileDest(dir, "/etc/passwd")
	if _, _, err := d.Open(); err == nil {
		t.Fatal("expected error for absolute filename")
	}
}

func TestNewFileDestPath_WritesPartialThenRenames(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "saved.csv")
	d := NewFileDestPath(full)
	wc, descriptor, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if descriptor != full {
		t.Fatalf("descriptor=%q want=%q", descriptor, full)
	}
	if _, err := wc.Write([]byte("a,b\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// .partial exists before Close.
	if _, err := os.Stat(full + ".partial"); err != nil {
		t.Fatalf(".partial not found before Close: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("final not found: %v", err)
	}
	if _, err := os.Stat(full + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial still exists: err=%v", err)
	}
}

func TestNewFileDestPath_OverwritesExistingFinal(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "saved.csv")
	if err := os.WriteFile(full, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d := NewFileDestPath(full)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("new")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("content=%q want=%q", got, "new")
	}
}

func TestNewFileDestPath_FileMode0o600(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "saved.csv")
	d := NewFileDestPath(full)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm() & 0o777; got != 0o600 {
		t.Fatalf("mode=%o want=0600", got)
	}
}

func TestNewFileDestPath_AbortRemovesPartial(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "saved.csv")
	d := NewFileDestPath(full).(*fileDest)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("partial")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	d.Abort()
	if _, err := os.Stat(full + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial still exists after Abort: err=%v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("final unexpectedly exists after Abort: err=%v", err)
	}
}

func TestNewFileDestPath_Rejects(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
	}{
		{"directory trailing slash", dir + string(filepath.Separator)},
		{"dot filename", dir + string(filepath.Separator) + "."},
		{"dotdot filename", dir + string(filepath.Separator) + ".."},
		{"control char in name", filepath.Join(dir, "a\nb.csv")},
		{"parent dir missing", filepath.Join(dir, "nope", "out.csv")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewFileDestPath(tt.path)
			if _, _, err := d.Open(); err == nil {
				t.Fatalf("expected error for %q", tt.path)
			}
		})
	}
}

func TestNewFileDestPath_WritesFilenameWithSpaceVerbatim(t *testing.T) {
	full := filepath.Join(t.TempDir(), "q2 report.csv")
	d := NewFileDestPath(full)
	wc, descriptor, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if descriptor != full {
		t.Fatalf("descriptor=%q want=%q", descriptor, full)
	}
	want := []byte("a,b\r\n")
	if _, err := wc.Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// File exists at the exact path with the space preserved.
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("final not found at %q: %v", full, err)
	}
	if base := filepath.Base(full); base != "q2 report.csv" {
		t.Fatalf("base=%q want=%q", base, "q2 report.csv")
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content=%q want=%q", got, want)
	}
}

func TestNewFileDestPath_NonWritableParentReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod perms ineffective as root")
	}
	subdir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(subdir, 0o500); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(subdir, 0o700) })

	d := NewFileDestPath(filepath.Join(subdir, "f.csv"))
	if _, _, err := d.Open(); err == nil {
		t.Fatal("expected error opening file in non-writable parent dir")
	}
}

func TestNewFileDestPath_ParentIsFile(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	d := NewFileDestPath(filepath.Join(notADir, "out.csv"))
	if _, _, err := d.Open(); err == nil {
		t.Fatal("expected error when parent is a file")
	}
}
