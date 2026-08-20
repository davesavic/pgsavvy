package orchestrator

import (
	"testing"
	"time"
)

// TestWorkerGroupWaitTimeoutNilWhenIdle: a quiescent group returns
// immediately, well under any bound.
func TestWorkerGroupWaitTimeoutNilWhenIdle(t *testing.T) {
	g := newWorkerGroup()
	start := time.Now()
	if err := g.WaitTimeout(10 * time.Millisecond); err != nil {
		t.Fatalf("idle WaitTimeout err = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("idle WaitTimeout took %v, want instant", elapsed)
	}
}

// TestWorkerGroupWaitTimeoutNilAfterDrain: a worker that finishes within
// the bound lets the wait return nil.
func TestWorkerGroupWaitTimeoutNilAfterDrain(t *testing.T) {
	g := newWorkerGroup()
	release := make(chan struct{})
	g.Go(func() { <-release })
	done := make(chan error, 1)
	go func() { done <- g.WaitTimeout(2 * time.Second) }()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitTimeout after drain err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitTimeout did not return after the worker drained")
	}
}

// TestWorkerGroupWaitTimeoutExpiresWithStuckWorker is the C6 primitive
// contract: a never-finishing worker cannot hang the wait — the bound
// fires and an error is returned (fail loud) instead of blocking forever.
func TestWorkerGroupWaitTimeoutExpiresWithStuckWorker(t *testing.T) {
	g := newWorkerGroup()
	release := make(chan struct{})
	g.Add(1)
	go func() {
		defer g.Done()
		<-release // never Done until the test releases it
	}()
	start := time.Now()
	err := g.WaitTimeout(50 * time.Millisecond)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("WaitTimeout took %v, want ~50ms bound", elapsed)
	}
	if err == nil {
		t.Fatal("WaitTimeout on a stuck worker returned nil, want error")
	}
	// Release the goroutine so nothing leaks.
	close(release)
	g.Wait()
}

// TestWorkerGroupReusableAcrossEpochs: the primitive keeps its blocking
// Wait and bounded WaitTimeout working across 0→1→0 cycles.
func TestWorkerGroupReusableAcrossEpochs(t *testing.T) {
	g := newWorkerGroup()
	for range 3 {
		g.Go(func() {})
		g.Wait()
	}
	if err := g.WaitTimeout(time.Millisecond); err != nil {
		t.Fatalf("WaitTimeout after reuse err = %v, want nil", err)
	}
}
