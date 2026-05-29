// Package clipboard provides the shared clipboard seam used by the vim
// editor and the results grid + exporter. It abstracts the system clipboard
// behind a small interface so call sites stay decoupled from the concrete
// backend (atotto, which shells out to xclip/wl-copy/pbcopy).
package clipboard

import (
	"strings"

	"github.com/atotto/clipboard"
)

// Clipboard is the seam through which the application reads from and writes
// to the host clipboard.
type Clipboard interface {
	Read() (string, error)
	Write(text string) error
}

// SystemClipboard is the production Clipboard backed by atotto/clipboard.
// readFn and writeFn are injectable so the CRLF-normalization logic can be
// unit-tested without a real clipboard backend present.
type SystemClipboard struct {
	readFn  func() (string, error)
	writeFn func(string) error
}

// NewSystemClipboard returns a Clipboard backed by the OS clipboard.
func NewSystemClipboard() Clipboard {
	return &SystemClipboard{
		readFn:  clipboard.ReadAll,
		writeFn: clipboard.WriteAll,
	}
}

// Write delegates to the underlying backend and returns its error unmodified.
func (c *SystemClipboard) Write(text string) error {
	return c.writeFn(text)
}

// Read returns the clipboard contents with CRLF sequences normalized to LF.
// A bare "\n" is never altered. The underlying error is returned unmodified.
func (c *SystemClipboard) Read() (string, error) {
	s, err := c.readFn()
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(s, "\r\n", "\n"), nil
}
