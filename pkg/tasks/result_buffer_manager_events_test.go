package tasks_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/tasks"
)

func newBufLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, buf
}

func linesContainingAll(buf *bytes.Buffer, subs ...string) []string {
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

func TestLaunchAndCleanup_EmitEvents(t *testing.T) {
	h := newTestHarness()
	defer h.stopUI()
	l, buf := newBufLogger()
	h.mgr.SetLogger(l)

	stream := newStubRowStream(5)
	doneCh := make(chan struct{})
	var mu sync.Mutex
	var got []models.Row
	appendF := h.makeAppendRows(&mu, &got)

	require.NoError(t, h.mgr.NewQueryTask(
		"k1",
		func(_ context.Context) (drivers.RowStream, error) { return stream, nil },
		appendF,
		5,
		func() { close(doneCh) },
	))

	// Wait for done.
	select {
	case <-doneCh:
	case <-time.After(time.Second):
		// Continue — Stop will force cleanup.
	}
	h.mgr.Stop()
	h.workersWG.Wait()

	launches := linesContainingAll(buf, `"cat":"state"`, `"evt":"rbm_task_launch"`, `"taskKey":"k1"`)
	require.Len(t, launches, 1, "expected one rbm_task_launch for k1; got %v", buf.String())
	require.Contains(t, launches[0], `"preempted_prior":false`)
	require.Contains(t, launches[0], `"rows_to_read":5`)

	cleanups := linesContainingAll(buf, `"cat":"state"`, `"evt":"rbm_task_cleanup"`, `"taskKey":"k1"`)
	require.Len(t, cleanups, 1, "expected one rbm_task_cleanup for k1; got %v", buf.String())
	require.Contains(t, cleanups[0], `"cleared":true`)
}

// TestRBMTaskLaunch_CapturesPreemptedBeforePriorStop verifies that the
// preempted_prior field reflects the state at NewQueryTask invocation
// time, NOT after priorStop runs. We use a blocking stream so the prior
// task is provably still alive when the second NewQueryTask hits the
// emit point.
func TestRBMTaskLaunch_CapturesPreemptedBeforePriorStop(t *testing.T) {
	h := newTestHarness()
	defer h.stopUI()
	l, buf := newBufLogger()
	h.mgr.SetLogger(l)

	// First task: a blocking stream parked at row 0.
	s1 := &stubRowStream{total: 100, errAt: -1, blockAt: 0, release: make(chan struct{})}
	done1 := make(chan struct{})
	var mu sync.Mutex
	var got []models.Row
	appendF := h.makeAppendRows(&mu, &got)

	require.NoError(t, h.mgr.NewQueryTask(
		"k1",
		func(_ context.Context) (drivers.RowStream, error) { return s1, nil },
		appendF,
		0, // skip initial fill so the worker enters the chan loop, parks on Next
		func() { close(done1) },
	))

	// Give the worker a moment to enter Next and park.
	time.Sleep(20 * time.Millisecond)

	// Second task: a fresh stream. priorStop will be non-nil here, so
	// preempted_prior must be true.
	s2 := newStubRowStream(3)
	done2 := make(chan struct{})

	go func() {
		_ = h.mgr.NewQueryTask(
			"k2",
			func(_ context.Context) (drivers.RowStream, error) { return s2, nil },
			appendF,
			3,
			func() { close(done2) },
		)
	}()

	// Allow the second NewQueryTask to emit its launch event and then
	// block on priorStop. Release s1 so the prior worker can exit.
	time.Sleep(20 * time.Millisecond)
	close(s1.release)

	select {
	case <-done2:
	case <-time.After(time.Second):
		// Continue — cleanup will run via Stop.
	}
	h.mgr.Stop()
	h.workersWG.Wait()

	k2Launches := linesContainingAll(buf, `"evt":"rbm_task_launch"`, `"taskKey":"k2"`)
	require.Len(t, k2Launches, 1)
	require.Contains(t, k2Launches[0], `"preempted_prior":true`,
		"second launch must observe preempted_prior=true (prior task alive at emit time)")

	k1Launches := linesContainingAll(buf, `"evt":"rbm_task_launch"`, `"taskKey":"k1"`)
	require.Len(t, k1Launches, 1)
	require.Contains(t, k1Launches[0], `"preempted_prior":false`)
}

func TestSetEstimatedRows_EmitsEvent(t *testing.T) {
	// Build a bare manager without any worker activity — we just exercise
	// the SetEstimatedRows surface.
	m := tasks.New(func(func(gocui.Task) error) {}, func(func() error) {})
	l, buf := newBufLogger()
	m.SetLogger(l)

	m.SetEstimatedRows(1234)

	lines := linesContainingAll(buf, `"cat":"state"`, `"evt":"rbm_estimated_rows"`, `"n":1234`)
	require.Len(t, lines, 1, "expected one rbm_estimated_rows line; got %v", buf.String())
}
