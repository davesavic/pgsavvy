package clipboard

import (
	"errors"
	"testing"
)

// Compile-time assertions: *SystemClipboard satisfies Clipboard, and it
// structurally satisfies the grid/exporter ClipboardWriter seam
// (interface{ Write(string) error }). grid/exporter are intentionally not
// imported here to avoid import cycles; the structural shape is asserted
// directly.
var (
	_ Clipboard                        = (*SystemClipboard)(nil)
	_ interface{ Write(string) error } = (*SystemClipboard)(nil)
)

func TestClipboardReadNormalizesNewlines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "crlf to lf", input: "a\r\nb", want: "a\nb"},
		{name: "multiple crlf", input: "a\r\nb\r\nc", want: "a\nb\nc"},
		{name: "bare lf preserved", input: "a\nb", want: "a\nb"},
		{name: "trailing bare lf preserved", input: "a\n", want: "a\n"},
		{name: "no newline unchanged", input: "abc", want: "abc"},
		{name: "empty unchanged", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &SystemClipboard{
				readFn: func() (string, error) { return tt.input, nil },
			}
			got, err := c.Read()
			if err != nil {
				t.Fatalf("Read() returned unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Read() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClipboardReadReturnsUnderlyingError(t *testing.T) {
	wantErr := errors.New("backend unavailable")
	c := &SystemClipboard{
		readFn: func() (string, error) { return "ignored", wantErr },
	}

	got, err := c.Read()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Read() error = %v, want %v", err, wantErr)
	}
	if got != "" {
		t.Fatalf("Read() value = %q, want empty string on error", got)
	}
}

func TestClipboardWriteDelegatesAndReturnsError(t *testing.T) {
	wantErr := errors.New("write failed")
	var captured string
	c := &SystemClipboard{
		writeFn: func(s string) error {
			captured = s
			return wantErr
		},
	}

	err := c.Write("payload")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Write() error = %v, want %v", err, wantErr)
	}
	if captured != "payload" {
		t.Fatalf("Write() delegated %q, want %q", captured, "payload")
	}
}

func TestClipboardWriteSuccess(t *testing.T) {
	var captured string
	c := &SystemClipboard{
		writeFn: func(s string) error {
			captured = s
			return nil
		},
	}

	if err := c.Write("ok"); err != nil {
		t.Fatalf("Write() returned unexpected error: %v", err)
	}
	if captured != "ok" {
		t.Fatalf("Write() delegated %q, want %q", captured, "ok")
	}
}
