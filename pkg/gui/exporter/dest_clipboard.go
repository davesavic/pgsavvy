package exporter

import (
	"bytes"
	"fmt"
	"io"
)

// ClipboardWriter is the minimal interface exporter requires to push final
// text to the system clipboard. Concrete implementations live elsewhere
// (e.g., pkg/gui/grid) to avoid pulling that dependency into this package.
type ClipboardWriter interface {
	Write(text string) error
}

// clipboardDest buffers output in memory up to a cap and on Close pushes
// the buffer to the configured ClipboardWriter.
type clipboardDest struct {
	cb     ClipboardWriter
	maxLen int64
	buf    *cappedBuffer
}

// NewClipboardDest returns a Destination that buffers up to maxBytes and
// emits the buffer to cb on Close. A nil cb is tolerated and yields a
// no-op Close.
func NewClipboardDest(cb ClipboardWriter, maxBytes int64) Destination {
	return &clipboardDest{cb: cb, maxLen: maxBytes}
}

func (d *clipboardDest) Open() (io.WriteCloser, string, error) {
	d.buf = &cappedBuffer{max: d.maxLen}
	return &clipboardWriteCloser{dest: d}, "clipboard", nil
}

type cappedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.max {
		return 0, fmt.Errorf("exporter: clipboard output exceeds cap of %d bytes", b.max)
	}
	return b.Buffer.Write(p)
}

type clipboardWriteCloser struct {
	dest *clipboardDest
}

func (w *clipboardWriteCloser) Write(p []byte) (int, error) {
	return w.dest.buf.Write(p)
}

func (w *clipboardWriteCloser) Close() error {
	if w.dest.cb == nil {
		return nil // no-op clipboard; user can still see output via stdout/file paths
	}
	return w.dest.cb.Write(w.dest.buf.String())
}
