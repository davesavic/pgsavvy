package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/.ssh/id_rsa", filepath.Join(home, ".ssh", "id_rsa")},
		{"absolute unchanged", "/etc/passwd", "/etc/passwd"},
		{"relative unchanged", "foo/bar", "foo/bar"},
		{"empty unchanged", "", ""},
		{"tilde-prefixed name not expanded", "~user/file", "~user/file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExpandHome(tt.in)
			if err != nil {
				t.Fatalf("ExpandHome(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ExpandHome(%q)=%q want=%q", tt.in, got, tt.want)
			}
		})
	}
}
