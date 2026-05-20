package exporter

import (
	"io"
	"os"
)

// stdoutDest writes to os.Stdout. Close MUST NOT close os.Stdout — the
// nopCloser wrapper makes that explicit.
type stdoutDest struct{}

// NewStdoutDest returns a Destination that writes to os.Stdout. Close is
// a no-op so subsequent stdout writes from the rest of the process still
// work.
func NewStdoutDest() Destination { return stdoutDest{} }

func (stdoutDest) Open() (io.WriteCloser, string, error) {
	return nopCloser{os.Stdout}, "stdout", nil
}

type nopCloser struct {
	io.Writer
}

func (nopCloser) Close() error { return nil }
