package logs

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
)

// captureHook records the last entry seen.
type captureHook struct {
	last *logrus.Entry
}

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *captureHook) Fire(e *logrus.Entry) error {
	// Copy data to a fresh map so subsequent mutations don't race.
	cp := logrus.Fields{}
	for k, v := range e.Data {
		cp[k] = v
	}
	dup := *e
	dup.Data = cp
	h.last = &dup
	return nil
}

func TestEvent_AlwaysSetsCatField(t *testing.T) {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.DebugLevel)
	h := &captureHook{}
	l.AddHook(h)

	Event(l, "cat1", "evt1", logrus.Fields{"cat": "overridden", "extra": 42})

	if h.last == nil {
		t.Fatal("no entry captured")
	}
	if got := h.last.Data["cat"]; got != "cat1" {
		t.Fatalf("cat = %v, want cat1", got)
	}
	if got := h.last.Data["evt"]; got != "evt1" {
		t.Fatalf("evt = %v, want evt1", got)
	}
	if got := h.last.Data["extra"]; got != 42 {
		t.Fatalf("extra = %v, want 42", got)
	}
}
