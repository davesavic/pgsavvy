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
