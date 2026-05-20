package logs

import (
	"os"
	"testing"
)

func TestNewTestLogger_NoExternalFs(t *testing.T) {
	dir := t.TempDir()
	l := NewTestLogger(t)
	if l == nil {
		t.Fatal("NewTestLogger returned nil")
	}
	l.Info("nothing should hit disk")
	l.Debug("nor this")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected empty tmp dir, got: %v", names)
	}
}
