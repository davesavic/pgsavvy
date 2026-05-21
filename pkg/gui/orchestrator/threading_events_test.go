package orchestrator_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"
)

// bufLogger returns a JSON DebugLevel *slog.Logger writing to a fresh
// buffer. Used by the cat=state worker_* tests to inspect emitted lines
// without depending on file-side instrumentation.
func bufLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), buf
}

// countLines counts lines in buf containing all of subs.
func countLines(buf *bytes.Buffer, subs ...string) int {
	n := 0
	for _, ln := range strings.Split(buf.String(), "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				ok = false
				break
			}
		}
		if ok && ln != "" {
			n++
		}
	}
	return n
}

// grepLines returns every line in buf containing all of subs.
func grepLines(buf *bytes.Buffer, subs ...string) []string {
	var out []string
	for _, ln := range strings.Split(buf.String(), "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				ok = false
				break
			}
		}
		if ok && ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// TestOnWorker_AlwaysEmitsQuiescenceTransitions verifies the single-call
// quiescence rule: one OnWorker call from a quiescent state must emit
// BOTH worker_start (busy_before=0, busy_after=1) AND, on completion,
// worker_end (busy_after=0). Sampling does NOT decimate either.
func TestOnWorker_AlwaysEmitsQuiescenceTransitions(t *testing.T) {
	l, buf := bufLogger()
	g, _, _ := buildTestGuiWithLogger(t, l)
	defer func() { _ = g.Close() }()

	done := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		close(done)
		return nil
	})
	<-done
	g.WaitWorkers()

	starts := grepLines(buf, `"evt":"worker_start"`)
	ends := grepLines(buf, `"evt":"worker_end"`)
	require.Len(t, starts, 1, "expected exactly one worker_start; got %v", buf.String())
	require.Contains(t, starts[0], `"busy_before":0`)
	require.Contains(t, starts[0], `"busy_after":1`)
	require.GreaterOrEqual(t, len(ends), 1, "expected at least one worker_end; got %v", buf.String())
	var sawQuiescence bool
	for _, ln := range ends {
		if strings.Contains(ln, `"busy_after":0`) && strings.Contains(ln, `"busy_before":1`) {
			sawQuiescence = true
			break
		}
	}
	require.True(t, sawQuiescence, "expected a worker_end with busy_before=1/busy_after=0; got %v", buf.String())
}

// TestOnWorker_SamplesBurstAt_2plus100 is the AD-20 contract test: 1000
// OnWorker invocations launched concurrently (no early completions) must
// produce exactly 2 + 100 cat=state worker_* lines —
//   - 1 transition worker_start (call #1, busy_before=0).
//   - 100 sampled worker_start lines (calls #10, #20, ..., #1000).
//   - 1 transition worker_end (last completing worker, busy_after=0).
//
// = 101 starts + 1 end = 102. Workers block until released so all 1000
// are simultaneously in flight when sampling fires.
func TestOnWorker_SamplesBurstAt_2plus100(t *testing.T) {
	l, buf := bufLogger()
	g, _, _ := buildTestGuiWithLogger(t, l)
	defer func() { _ = g.Close() }()

	const n = 1000
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(n)

	for i := 0; i < n; i++ {
		g.OnWorker(func(_ gocui.Task) error {
			started.Done()
			<-release
			return nil
		})
	}

	started.Wait()

	startCount := countLines(buf, `"evt":"worker_start"`)
	close(release)
	g.WaitWorkers()
	endCount := countLines(buf, `"evt":"worker_end"`)

	require.Equal(t, 101, startCount,
		"expected 101 worker_start lines (1 transition + 100 sampled); got %d\nbuf=%s", startCount, buf.String())
	require.Equal(t, 1, endCount,
		"expected exactly 1 worker_end line (quiescence transition); got %d", endCount)
	require.Equal(t, 102, startCount+endCount, "total worker_* line count must equal 2+100")
}

// TestOnWorker_PanicEmitsWorkerEndWithRecovered verifies that a panicking
// worker still leaves a worker_end{panic_recovered:true} trace alongside
// the existing Errorf log line.
func TestOnWorker_PanicEmitsWorkerEndWithRecovered(t *testing.T) {
	l, buf := bufLogger()
	g, _, _ := buildTestGuiWithLogger(t, l)
	defer func() { _ = g.Close() }()

	done := make(chan struct{})
	g.OnWorker(func(_ gocui.Task) error {
		defer close(done)
		panic("synthetic panic")
	})
	<-done
	g.WaitWorkers()

	panicEnds := grepLines(buf, `"evt":"worker_end"`, `"panic_recovered":true`)
	require.Len(t, panicEnds, 1, "expected one panic_recovered worker_end; got %v", buf.String())
}

// TestOnWorker_ErrEmitsWorkerEnd verifies a non-nil error return from the
// worker fn emits a worker_end{err:...} line on top of the existing
// Errorf log line.
func TestOnWorker_ErrEmitsWorkerEnd(t *testing.T) {
	l, buf := bufLogger()
	g, _, _ := buildTestGuiWithLogger(t, l)
	defer func() { _ = g.Close() }()

	g.OnWorker(func(_ gocui.Task) error {
		return errors.New("synthetic worker error")
	})
	g.WaitWorkers()

	errEnds := grepLines(buf, `"evt":"worker_end"`, `synthetic worker error`)
	require.Len(t, errEnds, 1, "expected one err worker_end; got %v", buf.String())
}
