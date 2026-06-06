package logs

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func TestNewTestLogger_NoExternalFs(t *testing.T) {
	dir := t.TempDir()
	l := NewTestLogger(t)
	if l == nil {
		t.Fatal("NewTestLogger returned nil")
	}
	l.Info("nothing should hit disk")
	l.Debug("nor this")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected empty tmp dir, got: %v", names)
	}
}

func TestRecordingHandler_RaceFree(t *testing.T) {
	h := NewRecordingHandler()
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := range 50 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 100 {
				r := slog.NewRecord(time.Now(), slog.LevelDebug, "msg", 0)
				r.AddAttrs(slog.Int("g", g), slog.Int("i", i))
				_ = h.Handle(ctx, r)
			}
		}(g)
	}
	// Concurrent readers.
	for range 10 {
		wg.Go(func() {
			for range 50 {
				_ = h.Records()
			}
		})
	}
	wg.Wait()

	if got := len(h.Records()); got != 50*100 {
		t.Fatalf("expected %d records, got %d", 50*100, got)
	}
}
