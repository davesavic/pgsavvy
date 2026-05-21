package logs

import (
	"log/slog"
	"testing"
)

// attrByKey looks up an attr by key in r. Returns the value and true if found.
func attrByKey(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, found = a.Value, true
			return false
		}
		return true
	})
	return v, found
}

func TestEvent_AlwaysSetsCatField(t *testing.T) {
	h := NewRecordingHandler()
	l := slog.New(h)

	Event(l, "cat1", "evt1",
		slog.String("cat", "overridden"),
		slog.String("evt", "overridden"),
		slog.Int("extra", 42),
	)

	recs := h.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	r := recs[0]

	if v, ok := attrByKey(r, "cat"); !ok || v.String() != "cat1" {
		t.Fatalf("cat = %v (ok=%v), want cat1", v, ok)
	}
	if v, ok := attrByKey(r, "evt"); !ok || v.String() != "evt1" {
		t.Fatalf("evt = %v (ok=%v), want evt1", v, ok)
	}
	if v, ok := attrByKey(r, "extra"); !ok || v.Int64() != 42 {
		t.Fatalf("extra = %v (ok=%v), want 42", v, ok)
	}
	if r.Message != "evt1" {
		t.Fatalf("message = %q, want evt1", r.Message)
	}
	if r.Level != slog.LevelDebug {
		t.Fatalf("level = %v, want Debug", r.Level)
	}
}

func TestEvent_NilLoggerNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with nil logger: %v", r)
		}
	}()
	Event(nil, "x", "y", slog.Int("n", 1))
}
