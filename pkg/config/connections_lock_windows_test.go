package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

func TestConnectionsWriteLock_WindowsBasenames(t *testing.T) {
	for _, name := range []string{"CONNEC~1.YML", "connections.yml.", "connections.yml ", "connections.yml:stream"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := SaveConnections(afero.NewOsFs(), filepath.Join(dir, name), nil); err == nil {
				t.Fatal("ambiguous Windows basename accepted")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected name created artifacts: %v, %v", entries, err)
			}
		})
	}
}
