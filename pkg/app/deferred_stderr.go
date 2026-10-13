package app

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

const maxDeferredStderrBytes = 64 * 1024

// deferredStderr keeps diagnostics off the raw-mode terminal until the TUI
// restores it. Session-file logging bypasses this writer and stays immediate.
type deferredStderr struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	output   io.Writer
	released bool
	omitted  uint64
}

func (w *deferredStderr) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return w.output.Write(p)
	}
	// Keep complete writes rather than cutting a diagnostic in half. File
	// logging is unaffected; the replay reports any omitted terminal output.
	if w.omitted > 0 || len(p) > maxDeferredStderrBytes-w.buffer.Len() {
		w.omitted += uint64(len(p))
		return len(p), nil
	}
	return w.buffer.Write(p)
}

func (w *deferredStderr) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return
	}
	w.released = true
	_, _ = w.buffer.WriteTo(w.output)
	if w.omitted > 0 {
		_, _ = fmt.Fprintf(w.output, "pgsavvy: %d diagnostic bytes omitted from terminal replay\n", w.omitted)
	}
}
