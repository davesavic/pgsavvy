//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/cli"
)

func machineTestLLMHelpOffline(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			profile := filepath.Join(root, "config", "pgsavvy", "connections.yml")
			const invalidProfile = "connections: [PRIVATE_PROFILE_SENTINEL"
			if mode != "missing" {
				if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
					t.Fatal(err)
				}
				if mode == "fifo" {
					if err := syscall.Mkfifo(profile, 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(profile, []byte(invalidProfile), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() []string {
				t.Helper()
				var paths []string
				if err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
					paths = append(paths, path)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return paths
			}
			before := snapshot()
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = writer.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, machineTestBinary, "llm-help", "--format", "json", "--timeout", "3s")
			command.Env = machineTestEnv(root)
			command.Stdin = input // Keep the writer open: discovery must not wait for SQL or EOF.
			var output, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &output, &diagnostic
			if err := command.Run(); err != nil {
				t.Fatalf("offline guide failed with %s config: %v, stderr=%q", mode, err, diagnostic.String())
			}
			if output.Len() > cli.MaxOutputBytes || strings.Contains(output.String()+diagnostic.String(), "PRIVATE_PROFILE_SENTINEL") {
				t.Fatal("guide exceeded output bound or disclosed profile data")
			}
			doc := machineTestDocument(t, output.Bytes(), 0)
			if doc["command"] != "llm-help" || doc["result_complete"] != true || doc["truncated"] != false {
				t.Fatal("guide did not return a complete machine envelope")
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("guide created configuration, logs, history or other artifacts")
			}
			if mode == "malformed" {
				if data, err := os.ReadFile(profile); err != nil || string(data) != invalidProfile {
					t.Fatal("guide modified profile configuration")
				}
			}
		})
	}
}
