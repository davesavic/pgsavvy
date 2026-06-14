package orchestrator

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogPanicStack_RecordsValueAndStack verifies the permanent panic guard
// writes the recovered value and a goroutine stack to the session log under
// cat=app, evt=panic — the durable post-mortem breadcrumb that survives the
// TUI teardown wiping the terminal scrollback.
func TestLogPanicStack_RecordsValueAndStack(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logPanicStack(logger, "boom")

	out := buf.String()
	for _, want := range []string{`"evt":"panic"`, `"cat":"app"`, `"value":"boom"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q\ngot: %s", want, out)
		}
	}
	// The stack attr must carry a real trace (this test frame proves it is
	// the live goroutine stack, not an empty placeholder).
	if !strings.Contains(out, "logPanicStack") {
		t.Fatalf("log output missing goroutine stack\ngot: %s", out)
	}
}

// TestLogPanicStack_NilLogger guards the no-op path so the guard never
// panics-within-a-panic when the logger is absent.
func TestLogPanicStack_NilLogger(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logPanicStack(nil, ...) panicked: %v", r)
		}
	}()
	logPanicStack(nil, "boom")
}
