package orchestrator

import "time"

// queryRunState is the generation-tagged slot recording the query run
// currently in flight (pgsavvy-vky3.2). Pure state: it is set when a
// run starts and cleared when that same run finishes; nothing here
// schedules repaints or touches views.
type queryRunState struct {
	active    bool
	runID     string
	startedAt time.Time
}

// SetQueryRunStarted records that the query run identified by runID is
// now in flight, stamping startedAt from the spinner clock (read under
// spinnerMu with the same nil-guard discipline as SpinnerFrame; a nil
// clock — zero-value Gui — falls back to time.Now). Last-wins: a later
// Set overwrites any earlier run's slot, which is exactly the
// generation hand-off when a new run supersedes a still-un cleared one.
// Safe to call from any goroutine.
func (g *Gui) SetQueryRunStarted(runID string) {
	g.spinnerState.spinnerMu.Lock()
	clk := g.spinnerState.clock
	g.spinnerState.spinnerMu.Unlock()

	started := time.Now()
	if clk != nil {
		started = clk.Now()
	}

	g.queryRunMu.Lock()
	g.queryRun = queryRunState{active: true, runID: runID, startedAt: started}
	g.queryRunMu.Unlock()
}

// ClearQueryRun clears the in-flight slot ONLY if runID matches the
// current generation (the runID the slot was last Set with). A stale
// generation's clear is a no-op, so a slow run finishing after a newer
// run started cannot wipe the newer run's state — mirror of the runID
// discipline NoticeHelper applies to OnRunEnd/Finish. Clearing an idle
// slot is also a no-op. Safe to call from any goroutine.
func (g *Gui) ClearQueryRun(runID string) {
	g.queryRunMu.Lock()
	if g.queryRun.active && g.queryRun.runID == runID {
		g.queryRun = queryRunState{}
	}
	g.queryRunMu.Unlock()
}

// QueryRunStarted reports the in-flight query run, if any: startedAt
// and runID are meaningful only when ok is true (ok=false means no run
// is in flight). Safe to call from any goroutine.
func (g *Gui) QueryRunStarted() (startedAt time.Time, runID string, ok bool) {
	g.queryRunMu.Lock()
	st := g.queryRun
	g.queryRunMu.Unlock()
	return st.startedAt, st.runID, st.active
}
