package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestHistoryReload_SingleFlight(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	type call struct {
		ctx   context.Context
		limit int
	}
	started := make(chan call, 4)
	release := make(chan struct{})
	var count, active, maximum atomic.Int32
	updates := make(chan func() error, 8)
	var published []query.HistoryRow
	r := &historyReload{
		read: func(ctx context.Context, limit int) ([]query.HistoryRow, error) {
			n := active.Add(1)
			if n > maximum.Load() {
				maximum.Store(n)
			}
			defer active.Add(-1)
			id := count.Add(1)
			started <- call{ctx, limit}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []query.HistoryRow{{ID: int64(id)}}, nil
		},
		onUI:    func(fn func() error) { updates <- fn },
		publish: func(rows []query.HistoryRow, _ error) { published = rows },
	}
	t.Cleanup(r.close)
	r.request()
	first := <-started
	deadline, ok := first.ctx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
	require.Positive(t, time.Until(deadline))
	require.Equal(t, 500, first.limit)
	for range 100 {
		r.request()
	}
	release <- struct{}{}
	second := <-started
	require.Equal(t, 500, second.limit)
	require.EqualValues(t, 2, count.Load())
	require.EqualValues(t, 1, maximum.Load())
	release <- struct{}{}
	r.wait() // UI publication has intentionally not run: no acknowledgement wait.
	require.Len(t, updates, 1, "obsolete first completion must not publish")
	require.NoError(t, (<-updates)())
	require.EqualValues(t, 2, published[0].ID)
	require.EqualValues(t, 0, active.Load())
}

func TestHistoryReload_QueuedGenerationAndErrors(t *testing.T) {
	updates := make(chan func() error, 8)
	var count int
	rows := []query.HistoryRow{{ID: 9}}
	r := &historyReload{
		read: func(context.Context, int) ([]query.HistoryRow, error) {
			count++
			if count == 3 {
				return nil, errors.New("fixture read failure")
			}
			return []query.HistoryRow{{ID: int64(count)}}, nil
		},
		onUI: func(fn func() error) { updates <- fn },
		publish: func(next []query.HistoryRow, err error) {
			if err == nil {
				rows = next
			}
		},
	}
	defer r.close()
	r.request()
	r.wait()
	old := <-updates
	r.request()
	r.wait()
	require.NoError(t, old())
	require.EqualValues(t, 9, rows[0].ID, "queued stale generation changed rows")
	require.NoError(t, (<-updates)())
	require.EqualValues(t, 2, rows[0].ID)
	r.request()
	r.wait()
	require.NoError(t, (<-updates)())
	require.EqualValues(t, 2, rows[0].ID, "read failure lost prior rows")
}

func TestHistoryReload_Shutdown(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	started, stopped := make(chan struct{}), make(chan struct{})
	updates := make(chan func() error, 8)
	var publications atomic.Int32
	r := &historyReload{
		read: func(ctx context.Context, _ int) ([]query.HistoryRow, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return nil, ctx.Err()
		},
		onUI:    func(fn func() error) { updates <- fn },
		publish: func([]query.HistoryRow, error) { publications.Add(1) },
	}
	r.request()
	<-started
	r.request() // Pending work must not start after shutdown.
	r.close()
	select {
	case <-stopped:
	default:
		t.Fatal("close returned before the read joined")
	}
	r.request()
	r.close()
	require.Empty(t, updates)
	require.Zero(t, publications.Load())

	// Already queued UI callbacks are fenced even if executed after close.
	r = &historyReload{
		read:    func(context.Context, int) ([]query.HistoryRow, error) { return nil, nil },
		onUI:    func(fn func() error) { updates <- fn },
		publish: func([]query.HistoryRow, error) { publications.Add(1) },
	}
	r.request()
	r.wait()
	r.close()
	require.NoError(t, (<-updates)())
	require.Zero(t, publications.Load())
}
