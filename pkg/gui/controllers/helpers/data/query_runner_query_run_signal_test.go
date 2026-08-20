package data_test

import (
	"sync"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
)

// This file covers the query-run signal seam (pgsavvy-vky3.2):
// SetQueryRunSignal installs the set/clear hooks that
// NotifyQueryRunStarted / NotifyQueryRunFinished invoke, so controllers
// can signal run start/finish through the runner without importing
// orchestrator.

// TestQueryRunSignalUnwiredRunnerNotifiesAreNoops proves Notify* on a
// runner nobody wired is a no-op — unit tests that skip SetQueryRunSignal
// must be able to call the Notify methods freely.
func TestQueryRunSignalUnwiredRunnerNotifiesAreNoops(t *testing.T) {
	runner := data.NewQueryRunner(nil, drivers.Capabilities{})

	runner.NotifyQueryRunStarted("run-a")
	runner.NotifyQueryRunFinished("run-a") // must not panic
}

// TestQueryRunSignalWiredHooksReceiveRunID proves the wired hooks fire
// with the runID they were notified with, set and clear each exactly
// once per Notify call.
func TestQueryRunSignalWiredHooksReceiveRunID(t *testing.T) {
	runner := data.NewQueryRunner(nil, drivers.Capabilities{})

	var mu sync.Mutex
	var setGot, clearGot []string
	runner.SetQueryRunSignal(
		func(runID string) { mu.Lock(); setGot = append(setGot, runID); mu.Unlock() },
		func(runID string) { mu.Lock(); clearGot = append(clearGot, runID); mu.Unlock() },
	)

	runner.NotifyQueryRunStarted("run-42")
	runner.NotifyQueryRunFinished("run-42")

	mu.Lock()
	defer mu.Unlock()
	if len(setGot) != 1 || setGot[0] != "run-42" {
		t.Fatalf("set hook saw %v, want exactly [run-42]", setGot)
	}
	if len(clearGot) != 1 || clearGot[0] != "run-42" {
		t.Fatalf("clear hook saw %v, want exactly [run-42]", clearGot)
	}
}

// TestQueryRunSignalNilHooksAndNilReceiverSafe proves SetQueryRunSignal
// accepts nil hooks (clearing a wired seam) and that both the setter
// and the Notify methods are safe on a nil receiver — the same
// contract SetBusyHold documents.
func TestQueryRunSignalNilHooksAndNilReceiverSafe(t *testing.T) {
	runner := data.NewQueryRunner(nil, drivers.Capabilities{})
	runner.SetQueryRunSignal(
		func(string) { t.Error("set hook fired after being cleared") },
		func(string) { t.Error("clear hook fired after being cleared") },
	)
	runner.SetQueryRunSignal(nil, nil)
	runner.NotifyQueryRunStarted("run-a")
	runner.NotifyQueryRunFinished("run-a")

	var nilRunner *data.QueryRunner
	nilRunner.SetQueryRunSignal(nil, nil)
	nilRunner.NotifyQueryRunStarted("run-a")
	nilRunner.NotifyQueryRunFinished("run-a")
}
