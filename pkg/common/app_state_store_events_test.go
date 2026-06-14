package common

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// newLoggerWithBuf returns a DEBUG-level slog logger whose JSON output is
// captured into the returned buffer. Used by the cat=state instrumentation
// tests (ported to slog) to verify the
// expected line is emitted.
func newLoggerWithBuf() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// linesContaining returns every line in buf that contains all of subs.
func linesContaining(buf *bytes.Buffer, subs ...string) []string {
	var out []string
	for ln := range strings.SplitSeq(buf.String(), "\n") {
		hit := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				hit = false
				break
			}
		}
		if hit && ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func TestMutateAndSave_EmitsScheduledEvent(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	t.Cleanup(func() { _ = s.Close() })
	l, buf := newLoggerWithBuf()
	s.SetLogger(l)

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })

	lines := linesContaining(buf, `"cat":"state"`, `"evt":"appstate_mutate_scheduled"`)
	require.Len(t, lines, 1, "expected exactly one appstate_mutate_scheduled line; got %v", buf.String())
	require.Contains(t, lines[0], `"has_pending":false`)
}

func TestSaveFire_EmitsEvent(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	t.Cleanup(func() { _ = s.Close() })
	l, buf := newLoggerWithBuf()
	s.SetLogger(l)

	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })
	// Drain buffer of the scheduled event we don't care about here.
	buf.Reset()

	clk.Advance(DebounceWindow + time.Millisecond)
	require.NoError(t, s.Flush())

	lines := linesContaining(buf, `"cat":"state"`, `"evt":"appstate_save_fire"`)
	require.Len(t, lines, 1, "expected one appstate_save_fire line; got %v", buf.String())
	// `ms` field must be present.
	require.Contains(t, lines[0], `"ms":`)
}

func TestClose_EmitsCloseEvent_NoPending(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	l, buf := newLoggerWithBuf()
	s.SetLogger(l)

	require.NoError(t, s.Close())

	lines := linesContaining(buf, `"cat":"state"`, `"evt":"appstate_close"`)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], `"drained_pending":0`)
}

func TestClose_EmitsCloseEvent_WithPending(t *testing.T) {
	fs := afero.NewMemMapFs()
	clk := newFakeClock()
	s := NewAppStateStore(fs, "/state.yml", clk)
	l, buf := newLoggerWithBuf()
	s.SetLogger(l)

	// Arm a pending save without advancing the clock — Close should report
	// drained_pending=1.
	s.MutateAndSave(func(a *AppState) { a.LastConnectionID = "x" })
	buf.Reset()

	require.NoError(t, s.Close())

	lines := linesContaining(buf, `"cat":"state"`, `"evt":"appstate_close"`)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], `"drained_pending":1`)
}
