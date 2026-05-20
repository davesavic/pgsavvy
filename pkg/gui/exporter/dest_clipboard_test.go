package exporter

import (
	"testing"
)

type fakeClipboard struct {
	last  string
	calls int
}

func (f *fakeClipboard) Write(text string) error {
	f.last = text
	f.calls++
	return nil
}

func TestClipboardDest_CapEnforced(t *testing.T) {
	d := NewClipboardDest(&fakeClipboard{}, 10)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("123456789012345")); err == nil {
		t.Fatal("expected cap error on oversize write")
	}
}

func TestClipboardDest_OnCloseFlushesToWriter(t *testing.T) {
	cb := &fakeClipboard{}
	d := NewClipboardDest(cb, 1024)
	wc, descriptor, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if descriptor != "clipboard" {
		t.Fatalf("descriptor=%q want=clipboard", descriptor)
	}
	if _, err := wc.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cb.calls != 1 {
		t.Fatalf("clipboard calls=%d want=1", cb.calls)
	}
	if cb.last != "hello" {
		t.Fatalf("clipboard text=%q want=%q", cb.last, "hello")
	}
}

func TestClipboardDest_UnderCapWorks(t *testing.T) {
	cb := &fakeClipboard{}
	d := NewClipboardDest(cb, 1024)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("small")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestClipboardDest_NilWriter_Tolerated(t *testing.T) {
	d := NewClipboardDest(nil, 1024)
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := wc.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close with nil writer: %v", err)
	}
}
