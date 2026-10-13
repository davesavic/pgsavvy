package app

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func TestDeferredStderr_Release(t *testing.T) {
	var output bytes.Buffer
	w := &deferredStderr{output: &output}
	_, err := io.WriteString(w, "startup warning\n")
	require.NoError(t, err)
	require.Empty(t, output.String(), "must not write into the TUI")
	w.release()
	w.release()
	_, err = io.WriteString(w, "after close\n")
	require.NoError(t, err)
	require.Equal(t, "startup warning\nafter close\n", output.String())
}

func TestDeferredStderr_ConcurrentRelease(t *testing.T) {
	var output bytes.Buffer
	w := &deferredStderr{output: &output}
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			_, _ = fmt.Fprintf(w, "warning %d\n", i)
		})
	}
	w.release()
	wg.Wait()
	for i := range 100 {
		require.Contains(t, output.String(), fmt.Sprintf("warning %d\n", i))
	}
}

func TestDeferredStderr_BoundedReplay(t *testing.T) {
	var output bytes.Buffer
	w := &deferredStderr{output: &output}
	first := strings.Repeat("x", maxDeferredStderrBytes-1) + "\n"
	n, err := io.WriteString(w, first)
	require.NoError(t, err)
	require.Len(t, first, n)
	for range 100 {
		n, err = io.WriteString(w, "omitted\n")
		require.NoError(t, err)
		require.Equal(t, 8, n)
	}
	require.LessOrEqual(t, w.buffer.Len(), maxDeferredStderrBytes)
	require.Empty(t, output.String())
	w.release()
	require.Equal(t, first+"pgsavvy: 800 diagnostic bytes omitted from terminal replay\n", output.String())
}

func TestWireSessionLogger_DefersTerminalButNotFile(t *testing.T) {
	for _, mode := range []string{"primary", "fallback", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(disableSessionLogEnv, "")
			dir := "/state/pgsavvy"
			if mode == "fallback" {
				dir = ""
			}
			if mode == "disabled" {
				t.Setenv(disableSessionLogEnv, "1")
			}
			fs := afero.NewMemMapFs()
			var output bytes.Buffer
			stderr := &deferredStderr{output: &output}
			log, closer, err := wireSessionLogger(dir, false, fs, newBuildInfo(), stderr)
			require.NoError(t, err)
			if closer != nil {
				defer func() { _ = closer.Close() }()
			}
			log.Warn("keybindings: ambiguous_prefix", "dsn", "postgres://u:secret@h/d")
			require.Empty(t, output.String(), "warnings must not reach the raw-mode terminal")
			if mode == "primary" {
				entries, err := afero.ReadDir(fs, filepath.Join(dir, "sessions"))
				require.NoError(t, err)
				require.Len(t, entries, 1)
				body, err := afero.ReadFile(fs, filepath.Join(dir, "sessions", entries[0].Name()))
				require.NoError(t, err)
				require.Contains(t, string(body), "keybindings: ambiguous_prefix", "file logging must remain immediate")
				require.NotContains(t, string(body), "secret")
			}
			stderr.release()
			require.Contains(t, output.String(), "keybindings: ambiguous_prefix")
			require.Contains(t, output.String(), "***")
			require.NotContains(t, output.String(), "secret")
		})
	}
}
