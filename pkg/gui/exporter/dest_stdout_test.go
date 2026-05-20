package exporter

import (
	"testing"
)

func TestStdoutDest_DescriptorIsStdout(t *testing.T) {
	d := NewStdoutDest()
	wc, descriptor, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if descriptor != "stdout" {
		t.Fatalf("descriptor=%q want=stdout", descriptor)
	}
	if wc == nil {
		t.Fatal("nil WriteCloser")
	}
}

func TestStdoutDest_CloseIsNoop(t *testing.T) {
	d := NewStdoutDest()
	wc, _, err := d.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Calling Close twice should also be safe.
	if err := wc.Close(); err != nil {
		t.Fatalf("Close (2nd): %v", err)
	}
}
