package orchestrator

import (
	"testing"
	"time"
)

// TestQueryRunStartedNilClockFallsBackToWallClock covers the nil-clock
// guard: a bare zero-value Gui (spinnerState.clock nil — the same
// internal-test backdoor search_status_accessor_test.go /
// fk_cache_adapter_test.go use for bare Gui values) must fall back to
// time.Now when stamping startedAt, mirroring SpinnerFrame's nil-clock
// discipline, instead of panicking.
func TestQueryRunStartedNilClockFallsBackToWallClock(t *testing.T) {
	g := &Gui{}

	before := time.Now()
	g.SetQueryRunStarted("run-nilclock")
	after := time.Now()

	startedAt, runID, ok := g.QueryRunStarted()
	if !ok {
		t.Fatal("ok=false after SetQueryRunStarted, want true")
	}
	if runID != "run-nilclock" {
		t.Fatalf("runID = %q, want %q", runID, "run-nilclock")
	}
	if startedAt.Before(before) || startedAt.After(after) {
		t.Fatalf("startedAt = %v, want a wall-clock stamp within [%v, %v]", startedAt, before, after)
	}
}
