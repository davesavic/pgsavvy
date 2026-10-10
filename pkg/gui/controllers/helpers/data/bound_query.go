package data

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// QueryBinding captures a connection generation without exposing its session.
type QueryBinding struct {
	runner  *QueryRunner
	binding *runnerBinding
	action  atomic.Uint64
}

func (r *QueryRunner) CaptureQueryBinding() *QueryBinding {
	if r == nil {
		return nil
	}
	return &QueryBinding{runner: r, binding: r.load()}
}

func (b *QueryBinding) Current() bool {
	return b != nil && b.binding != nil && b.binding.sess != nil && b.runner.load() == b.binding && (b.action.Load() == 0 || b.action.Load() == b.runner.actionGeneration.Load())
}

// WorkAsync serializes metadata work with query launches without waiting for
// UI acknowledgments inside the launch queue.
func (b *QueryBinding) WorkAsync(ctx context.Context, current func() bool, preempt func() bool, work func(context.Context) error, ack func(error)) {
	valid := func() bool { return b.Current() && ctx.Err() == nil && current() }
	if !valid() {
		ack(context.Canceled)
		return
	}
	op := func(lctx context.Context) (launchResult, error) {
		b.runner.boundMu.Lock()
		admitted := valid() && lctx.Err() == nil
		b.runner.boundMu.Unlock()
		if !admitted {
			return launchResult{}, context.Canceled
		}
		if preempt != nil && preempt() {
			b.binding.sess.MarkPreemptPending()
			return launchResult{}, session.ErrPreemptPending
		}
		if !valid() || lctx.Err() != nil {
			return launchResult{}, context.Canceled
		}
		return launchResult{}, work(ctx)
	}
	b.runner.enqueueBoundLaunch(false, []launchStmt{{op: op}}, func(_ int, _ launchResult, err error) { ack(err) }, b.binding, valid, func() bool { return false }, b)
}

// RunAsync uses only the captured session and captured preemption targets.
func (b *QueryBinding) RunAsync(ctx context.Context, q models.Query, current func() bool, preempt func() bool, ack func(*BoundQueryResult, error)) {
	valid := func() bool { return b.Current() && ctx.Err() == nil && (current == nil || current()) }
	if !valid() {
		ack(nil, context.Canceled)
		return
	}
	r := b.runner
	op := func(lctx context.Context) (launchResult, error) {
		// Bind and admission serialize only this check, never a wire wait.
		r.boundMu.Lock()
		admitted := valid() && lctx.Err() == nil
		r.boundMu.Unlock()
		if !admitted {
			return launchResult{}, context.Canceled
		}
		finished := make(chan struct{})
		cancelFinished := make(chan struct{})
		go func() {
			defer close(cancelFinished)
			select {
			case <-finished:
			case <-ctx.Done():
				wireCtx, cancel := context.WithTimeout(context.Background(), wireCancelBound)
				defer cancel()
				r.cancelBindingWire(wireCtx, b.binding)
			}
		}()
		rh, err := b.binding.sess.Stream(context.WithoutCancel(lctx), q)
		close(finished)
		<-cancelFinished
		return launchResult{rh: rh}, err
	}
	r.enqueueBoundLaunch(false, []launchStmt{{op: op}}, func(_ int, result launchResult, err error) {
		if result.rh == nil {
			ack(nil, err)
			return
		}
		owned := &BoundQueryResult{binding: b, handle: result.rh, current: valid, settled: make(chan struct{})}
		r.boundMu.Lock()
		if !valid() {
			r.boundMu.Unlock()
			owned.Discard()
			ack(nil, context.Canceled)
			return
		}
		if r.unadopted == nil {
			r.unadopted = make(map[*BoundQueryResult]struct{})
		}
		r.unadopted[owned] = struct{}{}
		r.boundMu.Unlock()
		go func() {
			select {
			case <-owned.settled:
			case <-ctx.Done():
				owned.Discard()
			}
		}()
		ack(owned, err)
	}, b.binding, valid, preempt, b)
}

// BoundQueryResult owns an orphan until acknowledged UI adoption, including
// when the UI queue stops accepting callbacks during disconnect or shutdown.
type BoundQueryResult struct {
	binding *QueryBinding
	handle  *session.RunHandle
	current func() bool
	once    sync.Once
	settled chan struct{}
}

func (result *BoundQueryResult) Accept(adopt func(*session.RunHandle) bool) bool {
	if result == nil {
		return false
	}
	r := result.binding.runner
	r.boundMu.Lock()
	defer r.boundMu.Unlock()
	if _, exists := r.unadopted[result]; !exists || !result.current() {
		return false
	}
	if !adopt(result.handle) {
		return false
	}
	delete(r.unadopted, result)
	result.once.Do(func() { close(result.settled) })
	return true
}

func (result *BoundQueryResult) Discard() {
	if result == nil {
		return
	}
	r := result.binding.runner
	r.boundMu.Lock()
	delete(r.unadopted, result)
	r.boundMu.Unlock()
	result.dispose()
}

func (result *BoundQueryResult) dispose() {
	result.once.Do(func() {
		close(result.settled)
		go func() {
			_ = result.handle.Cancel()
			_ = result.handle.Rows().Close()
		}()
	})
}

func (r *QueryRunner) discardUnadoptedLocked() {
	for result := range r.unadopted {
		delete(r.unadopted, result)
		result.dispose()
	}
}
