package exporter

import "io"

// Destination opens a sink for exported bytes.
// Implementations:
//   - fileDest: writes to a .partial file, atomically renames on Footer success.
//   - clipboardDest: writes to memory; on Close, pushes the buffer to grid.ClipboardWriter.
//   - stdoutDest: returns os.Stdout wrapped in nopCloser; Close is a no-op (must NOT close stdout).
type Destination interface {
	// Open returns the writer to feed Format methods into, and a human-readable
	// descriptor (e.g., final filename, "clipboard", "stdout") for the UI/toast.
	Open() (io.WriteCloser, string, error)
}
