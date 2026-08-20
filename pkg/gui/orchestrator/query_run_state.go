package orchestrator

import (
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/status"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// queryRunState is the generation-tagged slot recording the query run
// currently in flight (pgsavvy-vky3.2). Pure state: it is set when a
// run starts and cleared when that same run finishes; the repaint
// scheduling lives in the helpers below, driven by the Set/Clear sites.
type queryRunState struct {
	active    bool
	runID     string
	startedAt time.Time
}

// queryRunClockNow reads the spinner clock under spinnerMu with the same
// nil-guard discipline as SpinnerFrame: a nil clock (zero-value Gui)
// falls back to time.Now.
func (g *Gui) queryRunClockNow() time.Time {
	g.spinnerState.spinnerMu.Lock()
	clk := g.spinnerState.clock
	g.spinnerState.spinnerMu.Unlock()
	if clk != nil {
		return clk.Now()
	}
	return time.Now()
}

// SetQueryRunStarted records that the query run identified by runID is
// now in flight, stamping startedAt from the spinner clock (read under
// spinnerMu with the same nil-guard discipline as SpinnerFrame; a nil
// clock — zero-value Gui — falls back to time.Now). Last-wins: a later
// Set overwrites any earlier run's slot, which is exactly the
// generation hand-off when a new run supersedes a still-un cleared one.
// Safe to call from any goroutine. After storing the state it posts an
// immediate subtitle repaint (content-only) so the running indicator
// appears without waiting for the first spinner tick — posting from the
// UI thread is safe because OnUIThreadContentOnly only enqueues onto
// gocui's userEvents buffer; the closure runs on the next MainLoop
// iteration.
func (g *Gui) SetQueryRunStarted(runID string) {
	started := g.queryRunClockNow()

	g.queryRunMu.Lock()
	g.queryRun = queryRunState{active: true, runID: runID, startedAt: started}
	g.queryRunMu.Unlock()

	g.OnUIThreadContentOnly(func() error {
		g.repaintQueryRunSubtitle()
		return nil
	})
}

// ClearQueryRun clears the in-flight slot ONLY if runID matches the
// current generation (the runID the slot was last Set with). A stale
// generation's clear is a no-op, so a slow run finishing after a newer
// run started cannot wipe the newer run's state — mirror of the runID
// discipline NoticeHelper applies to OnRunEnd/Finish. Clearing an idle
// slot is also a no-op. Safe to call from any goroutine. A
// generation-matched clear additionally posts a forced subtitle wipe so
// the indicator disappears at settle time even though the ticker
// disarms at the same transition — deliberately NOT routed through
// promptOnTop or any resize gate, so the clear lands while a prompt
// popup is on top.
func (g *Gui) ClearQueryRun(runID string) {
	g.queryRunMu.Lock()
	cleared := false
	if g.queryRun.active && g.queryRun.runID == runID {
		g.queryRun = queryRunState{}
		cleared = true
	}
	g.queryRunMu.Unlock()

	if !cleared {
		return
	}
	g.OnUIThreadContentOnly(func() error {
		g.forceClearQueryRunSubtitle()
		return nil
	})
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

// applyQueryRunSubtitle is the single subtitle-write primitive: it sets
// the shared rail view's Subtitle field and records the string as
// lastPainted under queryRunMu so later repaints can skip unchanged
// writes. Every subtitle mutation funnels through here — the layout
// pass, the tick/set-site repaint, and the clear-site wipe — so
// lastPainted can never drift from the field. A nil view (partial test
// wiring, pre-layout) records nothing: the next repaint self-heals by
// comparing against the still-stale lastPainted.
func (g *Gui) applyQueryRunSubtitle(v *gocui.View, built string) {
	if v == nil {
		return
	}
	v.Subtitle = built
	g.queryRunMu.Lock()
	g.queryRunSubtitlePainted = built
	g.queryRunMu.Unlock()
}

// lastQueryRunSubtitle returns the last string applyQueryRunSubtitle
// wrote, mutex-guarded.
func (g *Gui) lastQueryRunSubtitle() string {
	g.queryRunMu.Lock()
	s := g.queryRunSubtitlePainted
	g.queryRunMu.Unlock()
	return s
}

// repaintQueryRunSubtitle is the spinner-tick / set-site repaint for
// the running-query subtitle (paint-only, content-only). Guards, in
// order:
//
//   - missing view (gocui looks up by name) → silent return, no error;
//   - no run in flight → return: ticks never clear — the wipe belongs
//     to ClearQueryRun's forced repaint alone, so a tick landing
//     between a run's state clear and its wipe closure cannot fight it;
//   - a non-editor leaf owns the shared view → return: ticks never
//     touch non-editor renders, because the accompanying editor
//     re-render would clobber the list leaf's buffer (the pre-mortem
//     blocker).
//
// The frame derives from the RUN's startedAt — not g.SpinnerFrame,
// whose epoch is the ticker's arm time and is therefore the wrong base
// for a run that started mid-busy. When the built string differs from
// the last painted one it is applied and the editor content is
// re-rendered (paintQueryEditorLeaf) to taint the view, so the
// content-only flush repaints the frame the subtitle is drawn on;
// per-tick re-rendering is accepted as wasteful-but-safe.
func (g *Gui) repaintQueryRunSubtitle() {
	if g == nil || g.driver == nil || g.registry == nil || g.registry.QueryRail == nil {
		return
	}
	v, err := g.driver.ViewByName(guicontext.QueryRailViewName)
	if err != nil || v == nil {
		return
	}
	startedAt, _, ok := g.QueryRunStarted()
	if !ok {
		return
	}
	if g.registry.QueryRail.ActiveLeafKey() != types.QUERY_EDITOR {
		return
	}
	elapsed := g.queryRunClockNow().Sub(startedAt)
	built := status.BuildRunningSubtitle(int64(elapsed/spinnerTickInterval), elapsed)
	if g.lastQueryRunSubtitle() == built {
		return
	}
	g.applyQueryRunSubtitle(v, built)
	g.paintQueryEditorLeaf(v)
}

// forceClearQueryRunSubtitle is ClearQueryRun's generation-matched wipe:
// it blanks the subtitle field and lastPainted unconditionally, then
// re-renders the editor content (taint) ONLY when the editor leaf owns
// the shared view — never while a list leaf owns the buffer. When a
// list leaf is active the field-clear alone is enough: the Tier-1.4
// layout pass already keeps the non-editor branch clean. Not routed
// through promptOnTop — the clear must land even under a prompt popup.
func (g *Gui) forceClearQueryRunSubtitle() {
	if g == nil || g.driver == nil || g.registry == nil || g.registry.QueryRail == nil {
		return
	}
	v, err := g.driver.ViewByName(guicontext.QueryRailViewName)
	if err != nil || v == nil {
		return
	}
	g.applyQueryRunSubtitle(v, "")
	if g.registry.QueryRail.ActiveLeafKey() == types.QUERY_EDITOR {
		g.paintQueryEditorLeaf(v)
	}
}

// paintQueryRunSubtitleFromState paints the subtitle from the live run
// state during a FULL layout pass on the editor leaf — the safety net
// so a full frame never shows a stale or missing subtitle (leaf
// switches back to the editor, settles between ticks). No taint is
// needed: the full flush repaints everything anyway.
func (g *Gui) paintQueryRunSubtitleFromState(v *gocui.View) {
	startedAt, _, ok := g.QueryRunStarted()
	if !ok {
		g.applyQueryRunSubtitle(v, "")
		return
	}
	elapsed := g.queryRunClockNow().Sub(startedAt)
	built := status.BuildRunningSubtitle(int64(elapsed/spinnerTickInterval), elapsed)
	if g.lastQueryRunSubtitle() == built {
		return
	}
	g.applyQueryRunSubtitle(v, built)
}
