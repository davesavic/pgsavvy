package logs

import (
	"io"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
)

// recordingHook captures every fired entry (deep-copied) for later assertion.
type recordingHook struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (h *recordingHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *recordingHook) Fire(e *logrus.Entry) error {
	cp := logrus.Fields{}
	for k, v := range e.Data {
		cp[k] = v
	}
	dup := *e
	dup.Data = cp
	h.mu.Lock()
	h.entries = append(h.entries, &dup)
	h.mu.Unlock()
	return nil
}

func (h *recordingHook) snapshot() []*logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*logrus.Entry, len(h.entries))
	copy(out, h.entries)
	return out
}

// newBridgeLogger returns a debug-level discard logger with hook attached
// for assertions.
func newBridgeLogger() (*logrus.Logger, *recordingHook) {
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.DebugLevel)
	h := &recordingHook{}
	l.AddHook(h)
	return l, h
}

func TestNewSlogHandler_LevelMapping(t *testing.T) {
	cases := []struct {
		name string
		emit func(*slog.Logger)
		want logrus.Level
	}{
		{"debug", func(s *slog.Logger) { s.Debug("m") }, logrus.DebugLevel},
		{"info", func(s *slog.Logger) { s.Info("m") }, logrus.InfoLevel},
		{"warn", func(s *slog.Logger) { s.Warn("m") }, logrus.WarnLevel},
		{"error", func(s *slog.Logger) { s.Error("m") }, logrus.ErrorLevel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, h := newBridgeLogger()
			s := slog.New(NewSlogHandler(l))
			tc.emit(s)
			es := h.snapshot()
			if len(es) != 1 {
				t.Fatalf("entries = %d, want 1", len(es))
			}
			if es[0].Level != tc.want {
				t.Fatalf("level = %v, want %v", es[0].Level, tc.want)
			}
		})
	}
}

func TestNewSlogHandler_AttrsPreserved(t *testing.T) {
	l, h := newBridgeLogger()
	s := slog.New(NewSlogHandler(l)).With("k", "v")
	s.Info("msg")
	es := h.snapshot()
	if len(es) != 1 {
		t.Fatalf("entries = %d, want 1", len(es))
	}
	if got := es[0].Data["k"]; got != "v" {
		t.Errorf("Data[k] = %v, want v", got)
	}
	if got := es[0].Data["cat"]; got != "db" {
		t.Errorf("Data[cat] = %v, want db", got)
	}
	if es[0].Message != "msg" {
		t.Errorf("Message = %q, want msg", es[0].Message)
	}
}

func TestNewSlogHandler_GroupsAsDottedKeys(t *testing.T) {
	l, h := newBridgeLogger()
	s := slog.New(NewSlogHandler(l)).WithGroup("db").WithGroup("pg")
	s.With("sid", 7).Info("x")
	es := h.snapshot()
	if len(es) != 1 {
		t.Fatalf("entries = %d, want 1", len(es))
	}
	if got := es[0].Data["db.pg.sid"]; got != int64(7) {
		t.Errorf("Data[db.pg.sid] = %v (%T), want 7 (int64)", got, got)
	}
}

func TestNewSlogHandler_AlwaysSetsCatDB(t *testing.T) {
	l, h := newBridgeLogger()
	s := slog.New(NewSlogHandler(l))
	s.Info("no-attrs")
	es := h.snapshot()
	if len(es) != 1 {
		t.Fatalf("entries = %d, want 1", len(es))
	}
	if got := es[0].Data["cat"]; got != "db" {
		t.Fatalf("Data[cat] = %v, want db", got)
	}
}

// TestNewSlogHandler_DoesNotCallRedactor enforces the AD-5 single-redaction
// bilayer: the bridge MUST NOT carry a Redactor reference. We verify
// structurally (no `redactor` field on slogBridge) AND behaviourally (a
// hook attached as a Redactor-stand-in fires exactly once per emit — the
// usual logrus path — not twice).
func TestNewSlogHandler_DoesNotCallRedactor(t *testing.T) {
	// Structural: bridge has no redactor field.
	if _, ok := reflect.TypeOf(slogBridge{}).FieldByName("redactor"); ok {
		t.Fatal("slogBridge has a redactor field; bridge must not own redaction")
	}

	// Behavioural: a counting hook fires exactly once per slog emit.
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.DebugLevel)
	var calls atomic.Int32
	l.AddHook(&countingHook{n: &calls})
	s := slog.New(NewSlogHandler(l))
	s.Info("once")
	if got := calls.Load(); got != 1 {
		t.Fatalf("hook fired %d times, want 1", got)
	}
}

type countingHook struct{ n *atomic.Int32 }

func (h *countingHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *countingHook) Fire(*logrus.Entry) error {
	h.n.Add(1)
	return nil
}

func TestNewSlogHandler_RaceFree(t *testing.T) {
	l, _ := newBridgeLogger()
	s := slog.New(NewSlogHandler(l))
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			s.With("g", i).WithGroup("db").Info("race", "i", i)
		}(i)
	}
	wg.Wait()
}
