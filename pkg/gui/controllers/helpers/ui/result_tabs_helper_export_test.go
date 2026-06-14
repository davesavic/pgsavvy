package ui

import "testing"

// recordingClipboard captures the payload pushed through ClipboardWriter so the
// export-to-clipboard path can be asserted without a real system clipboard.
type recordingClipboard struct {
	got   string
	calls int
}

func (r *recordingClipboard) Write(text string) error {
	r.got = text
	r.calls++
	return nil
}

// TestBuildDestination_Clipboard_WritesToConfiguredWriter is the regression
// guard: buildDestination used to pass a nil ClipboardWriter,
// so clipboard exports were serialized and then silently discarded on Close.
// The destination must push the buffered payload to the configured writer.
func TestBuildDestination_Clipboard_WritesToConfiguredWriter(t *testing.T) {
	rec := &recordingClipboard{}
	h := NewResultTabsHelper(ResultTabsHelperDeps{ExportClipboard: rec})

	dest, err := h.buildDestination("Clipboard", "")
	if err != nil {
		t.Fatalf("buildDestination: %v", err)
	}

	wc, descriptor, err := dest.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if descriptor != "clipboard" {
		t.Fatalf("descriptor = %q, want %q", descriptor, "clipboard")
	}

	const payload = "a,b\r\n1,2\r\n"
	if _, err := wc.Write([]byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := wc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if rec.calls != 1 {
		t.Fatalf("clipboard Write calls = %d, want 1 (payload discarded?)", rec.calls)
	}
	if rec.got != payload {
		t.Fatalf("clipboard payload = %q, want %q", rec.got, payload)
	}
}

// TestBuildDestination_StdoutRemoved guards the removal of the stdout export
// destination: its bytes were written to os.Stdout from a
// worker while gocui owns the alternate screen, so they could never render.
// The destination is no longer offered, and the builder must reject it.
func TestBuildDestination_StdoutRemoved(t *testing.T) {
	h := NewResultTabsHelper(ResultTabsHelperDeps{})
	if _, err := h.buildDestination("stdout", ""); err == nil {
		t.Fatal("buildDestination(\"stdout\") = nil error, want rejected")
	}
}
