package logs

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// NewTestLogger returns a logger that discards all output. Useful for tests
// inside pkg/logs that want a non-nil *slog.Logger without touching disk.
func NewTestLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.DiscardHandler)
}

// RecordingHandler is a slog.Handler that records every record passed to it.
// Safe for concurrent use. Records() returns a defensive copy.
//
// With* chained handlers SHARE the same underlying record buffer via an
// internal pointer, so tests can attach a single RecordingHandler and assert
// all records regardless of how downstream code derives loggers from it.
type RecordingHandler struct {
	state  *recordingState
	attrs  []slog.Attr
	groups []string
}

type recordingState struct {
	mu      sync.Mutex
	records []slog.Record
}

// NewRecordingHandler constructs a fresh RecordingHandler with an empty record
// buffer.
func NewRecordingHandler() *RecordingHandler {
	return &RecordingHandler{state: &recordingState{}}
}

// Enabled is always true; level filtering is the parent's responsibility.
func (h *RecordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle records a clone of r (with any WithAttrs context prepended).
func (h *RecordingHandler) Handle(_ context.Context, r slog.Record) error {
	rc := r.Clone()
	if len(h.attrs) > 0 {
		rc.AddAttrs(h.attrs...)
	}
	h.state.mu.Lock()
	h.state.records = append(h.state.records, rc)
	h.state.mu.Unlock()
	return nil
}

// WithAttrs returns a handler that shares the same record buffer; the new
// handler's Handle will prepend attrs to every record.
func (h *RecordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := &RecordingHandler{
		state:  h.state,
		attrs:  append(append([]slog.Attr(nil), h.attrs...), attrs...),
		groups: append([]string(nil), h.groups...),
	}
	return nh
}

// WithGroup returns a handler that shares the same record buffer; group
// tracking is informational only (no key transformation happens here — the
// recorded records preserve their original keys).
func (h *RecordingHandler) WithGroup(name string) slog.Handler {
	nh := &RecordingHandler{
		state:  h.state,
		attrs:  append([]slog.Attr(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
	}
	if name != "" {
		nh.groups = append(nh.groups, name)
	}
	return nh
}

// Records returns a defensive copy of the recorded slog.Records slice.
func (h *RecordingHandler) Records() []slog.Record {
	h.state.mu.Lock()
	defer h.state.mu.Unlock()
	out := make([]slog.Record, len(h.state.records))
	copy(out, h.state.records)
	return out
}
