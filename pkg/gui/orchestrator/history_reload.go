package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/davesavic/pgsavvy/pkg/query"
)

const historyReadTimeout = 2 * time.Second

// historyReload owns a single read pump, independently joined at shutdown.
// Publication is queue admission only: the worker never awaits UI execution.
type historyReload struct {
	mu         sync.Mutex
	generation uint64
	active     bool
	pending    bool
	closed     bool
	cancel     context.CancelFunc
	done       chan struct{}
	read       func(context.Context, int) ([]query.HistoryRow, error)
	onUI       func(func() error)
	publish    func([]query.HistoryRow, error)
}

func (r *historyReload) request() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.generation++
	r.pending = true
	if r.active {
		r.mu.Unlock()
		return
	}
	r.active = true
	r.done = make(chan struct{})
	r.mu.Unlock()
	go r.run()
}

func (r *historyReload) run() {
	for {
		r.mu.Lock()
		if r.closed || !r.pending {
			r.active = false
			close(r.done)
			r.mu.Unlock()
			return
		}
		r.pending = false
		generation := r.generation
		ctx, cancel := context.WithTimeout(context.Background(), historyReadTimeout)
		r.cancel = cancel
		r.mu.Unlock()

		rows, err := r.read(ctx, historyRecentLimit)
		cancel()
		r.mu.Lock()
		r.cancel = nil
		current := !r.closed && generation == r.generation
		r.mu.Unlock()
		if current {
			r.onUI(func() error {
				r.mu.Lock()
				defer r.mu.Unlock()
				if !r.closed && generation == r.generation {
					r.publish(rows, err)
				}
				return nil
			})
		}
	}
}

func (r *historyReload) wait() {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (r *historyReload) close() {
	r.mu.Lock()
	r.closed = true
	r.pending = false
	r.generation++
	if r.cancel != nil {
		r.cancel()
	}
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}
