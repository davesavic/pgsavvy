package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// DefaultMaxResultTabs is the shipped tab-count cap. The helper accepts
// an override via ResultTabsHelperDeps.MaxTabs; the override falls back
// to this default when 0 or negative. Matches dbsavvy-66p §D9 default
// (ui.result_tabs_max = 8).
const DefaultMaxResultTabs = 8

// resultTabInitialRows is the initial-fill row count handed to
// ResultBufferManager.NewQueryTask. Matches dbsavvy-66p §D13 default
// (ui.result_initial_rows = 200). The dedicated config knob is wired in
// a follow-up; for now the constant matches the design value.
const resultTabInitialRows = 200

// resultTabToastTTL is the lifetime of toasts surfaced by the helper.
const resultTabToastTTL = 4 * time.Second

// resultTabLabelMax bounds the SQL-prefix portion of the tab title.
// Mirrors controllers.resultTabLabelMax (kept in sync; both are
// derived from the dbsavvy-66p §7 spec).
const resultTabLabelMax = 40

// TabState classifies the lifecycle phase of a result tab. The string
// values are surfaced directly in the rendered title.
type TabState string

const (
	// StateQueued — opened while a prior run was still streaming; the
	// tab's RowStream has not yet been opened.
	StateQueued TabState = "queued"
	// StateRunning — RowStream open; rows actively draining.
	StateRunning TabState = "running"
	// StateComplete — clean EOF.
	StateComplete TabState = "complete"
	// StateCancelled — server-side cancel completed.
	StateCancelled TabState = "cancelled"
	// StateDetached — client-side detach (e.g. Esc); server may still run.
	StateDetached TabState = "detached"
	// StateErrored — driver / stream surfaced an error.
	StateErrored TabState = "error"
	// StatePlan — tab holds an EXPLAIN result (raw text); no live stream.
	StatePlan TabState = "plan"
	// StateError — alias of StateErrored for ShowError-created tabs that
	// never had a stream attached. Distinct from a stream error.
	StateError TabState = "error"
)

// runHandle is the helper's narrow view of *session.RunHandle. *session.RunHandle
// satisfies it structurally; tests pass an in-memory fake. The helper
// only needs the lifecycle channel, the cancel surface, and the rows
// stream (handed to ResultBufferManager via streamFn).
type runHandle interface {
	Done() <-chan struct{}
	Cancel() error
	Rows() drivers.RowStream
}

// StreamRunner is the helper's narrow view of *tasks.ResultBufferManager.
// Tests inject a fake that records NewQueryTask invocations without
// spinning up a worker goroutine. Production wires *tasks.ResultBufferManager
// constructed against OnWorker / OnUIThread.
//
// Exported so the orchestrator (which constructs the real
// *tasks.ResultBufferManager) can pass a typed factory closure.
type StreamRunner interface {
	NewQueryTask(
		taskKey string,
		streamFn func(ctx context.Context) (drivers.RowStream, error),
		appendRows func([]models.Row),
		initialRows int,
		onDone func(),
	) error
	Stop()
}

// StreamRunnerFactory builds a StreamRunner per tab. Real production
// wraps tasks.New(OnWorker, OnUIThread).
type StreamRunnerFactory func() StreamRunner

// toastShower is the narrow surface the helper uses for surface-level
// toasts. *ui.ToastHelper satisfies it; tests inject a recorder.
type toastShower interface {
	Show(message string, ttl time.Duration)
}

// ResultTabsHelperDeps bundles the helper's collaborators. All fields
// are optional during unit testing; production wires the orchestrator's
// driver / threading helpers / toast helper.
type ResultTabsHelperDeps struct {
	// Driver is the gocui-runtime surface used to create / destroy /
	// raise per-tab views. May be nil in unit tests that don't exercise
	// the layout-side rendering.
	Driver types.GuiDriver

	// Toast is the surface used for "no tab N" / "tab cap reached"
	// notifications. May be nil; toasts then become no-ops.
	Toast toastShower

	// MaxTabs overrides the shipped DefaultMaxResultTabs cap. Non-
	// positive values fall back to the default.
	MaxTabs int

	// StreamFactory builds a per-tab StreamRunner. Production passes a
	// closure over tasks.New(g.OnWorker, g.OnUIThreadContentOnly); tests
	// pass a fake that records calls. A nil factory disables streaming
	// (Open creates tabs in the Running state with no RBM attached —
	// useful for tab-management unit tests).
	StreamFactory StreamRunnerFactory

	// Now is the time source used to stamp createdAt for eviction
	// ordering. Defaults to time.Now when nil.
	Now func() time.Time
}

// ResultTabsHelper owns the multi-result-tab pane in the orchestrator's
// "secondary" window slot. It is the concrete satisfier of
// controllers.ResultTabsHelper.
//
// dbsavvy-66p.12.
type ResultTabsHelper struct {
	deps    ResultTabsHelperDeps
	maxTabs int
	nextID  atomic.Int64
	now     func() time.Time

	mu       sync.Mutex
	tabs     []*Tab // ordered by Slot (0..max-1)
	activeID int64  // 0 when no tab is active
}

// NewResultTabsHelper constructs a helper with deps. The returned value
// is non-nil and safe to use even if every dep field is zero.
func NewResultTabsHelper(deps ResultTabsHelperDeps) *ResultTabsHelper {
	max := deps.MaxTabs
	if max <= 0 {
		max = DefaultMaxResultTabs
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &ResultTabsHelper{
		deps:    deps,
		maxTabs: max,
		now:     now,
	}
}

// Tab is one entry in the result-tab list. Exported so the layout pass
// can read state for rendering; mutators (Pin, Close, etc.) are owned
// by the helper.
type Tab struct {
	id        int64
	slot      int
	label     string
	createdAt time.Time

	// mu protects state, pinned, rowCount, cancelled. Held briefly in
	// mutators; helpers.mu must NOT be held when waiting on this mu to
	// avoid the {helper.mu ↔ tab.mu} cycle.
	mu        sync.Mutex
	state     TabState
	pinned    bool
	rowCount  int64
	err       error
	plan      models.Plan
	planRaw   string
	cancelled bool

	rh           runHandle
	grid         *grid.View
	runner       StreamRunner
	queuedCancel chan struct{} // closed to abort the queued-wait goroutine
	disposeOnce  sync.Once
	disposed     atomic.Bool

	doneCh chan struct{} // closed when the tab is fully torn down
}

// ID returns the tab's monotonically-allocated identifier. Stable for
// the tab's lifetime; reused identifiers never appear (the ID counter
// only ever increases).
func (t *Tab) ID() int64 { return t.id }

// Slot returns the 0-based slot index the tab occupies (recycled on close).
func (t *Tab) Slot() int { return t.slot }

// Label returns the tab title prefix supplied at Open.
func (t *Tab) Label() string { return t.label }

// State returns the current TabState.
func (t *Tab) State() TabState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Pinned reports the pin flag.
func (t *Tab) Pinned() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pinned
}

// RowCount returns the count of rows delivered to the tab so far.
func (t *Tab) RowCount() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rowCount
}

// Err returns the terminal error for an errored tab, or nil.
func (t *Tab) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Grid returns the embedded grid view for the tab; nil for plan / error tabs.
func (t *Tab) Grid() *grid.View {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.grid
}

// Plan returns the parsed plan tree for plan tabs; zero value otherwise.
func (t *Tab) Plan() models.Plan {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.plan
}

// ViewName returns the gocui view-name the tab is rendered under
// ("result_tab_<slot>").
func (t *Tab) ViewName() string {
	return string(types.ResultTabKey(t.slot))
}

// Title builds the rendered title: "<label> (<state>, N rows)" with
// label truncated to resultTabLabelMax characters.
func (t *Tab) Title() string {
	t.mu.Lock()
	state := t.state
	rows := t.rowCount
	t.mu.Unlock()
	label := truncateLabel(t.label, resultTabLabelMax)
	return fmt.Sprintf("result %d: %s (%s, %d rows)", t.slot+1, label, state, rows)
}

// --- Public surface (controllers.ResultTabsHelper) -----------------------

// ErrTabCapReached is returned by OpenResultTab / OpenPlanTab /
// ShowError when every tab is pinned and the cap is reached.
var ErrTabCapReached = errors.New("result tabs: cap reached; unpin a tab")

// OpenResultTab implements controllers.ResultTabsHelper. Creates a new
// streaming tab fed by rh's RowStream. The tab is added to the active
// list, made active, and (if a prior tab is still streaming) queued
// behind it.
func (h *ResultTabsHelper) OpenResultTab(label string, rh *session.RunHandle) error {
	if rh == nil {
		return h.openTab(label, nil)
	}
	return h.openTab(label, rh)
}

// OpenPlanTab implements controllers.ResultTabsHelper. Creates a tab
// holding the supplied plan; no stream is attached.
func (h *ResultTabsHelper) OpenPlanTab(label string, plan models.Plan) error {
	tab, err := h.allocTab(label)
	if err != nil {
		return err
	}
	tab.mu.Lock()
	tab.state = StatePlan
	tab.plan = plan
	tab.planRaw = plan.RawText
	tab.mu.Unlock()
	h.setActive(tab.id)
	h.materialiseView(tab)
	return nil
}

// ShowError implements controllers.ResultTabsHelper. Creates a tab
// surfacing err; no stream is attached.
func (h *ResultTabsHelper) ShowError(label string, err error) {
	tab, allocErr := h.allocTab(label)
	if allocErr != nil {
		// Allocation failed (e.g. all tabs pinned at cap). Surface the
		// original err through the toast as a fallback so the user sees
		// it; AllocErr is already toasted by allocTab.
		h.toast(err.Error())
		return
	}
	tab.mu.Lock()
	tab.state = StateError
	tab.err = err
	tab.mu.Unlock()
	h.setActive(tab.id)
	h.materialiseView(tab)
}

// --- Tab-management actions ---------------------------------------------

// Active returns the currently active tab, or nil when no tabs exist.
func (h *ResultTabsHelper) Active() *Tab {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.findByIDLocked(h.activeID)
}

// Tabs returns a snapshot of the tab list in slot order. Read-only;
// callers must not mutate the returned slice or its tabs through this
// accessor.
func (h *ResultTabsHelper) Tabs() []*Tab {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Tab, len(h.tabs))
	copy(out, h.tabs)
	sort.Slice(out, func(i, j int) bool { return out[i].slot < out[j].slot })
	return out
}

// Count returns the number of open tabs.
func (h *ResultTabsHelper) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.tabs)
}

// Max returns the configured tab cap.
func (h *ResultTabsHelper) Max() int { return h.maxTabs }

// CloseActive closes the active tab (disposing its stream synchronously).
// No-op when no tab is active.
func (h *ResultTabsHelper) CloseActive() error {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return nil
	}
	return h.Close(t)
}

// Close disposes t and removes it from the list. Active selection
// shifts to slot N-1 (or slot 0 when N==0). Safe to call on a tab
// that's already been closed.
func (h *ResultTabsHelper) Close(t *Tab) error {
	if t == nil {
		return nil
	}
	t.dispose()
	h.mu.Lock()
	idx := -1
	for i, tab := range h.tabs {
		if tab.id == t.id {
			idx = i
			break
		}
	}
	if idx == -1 {
		h.mu.Unlock()
		return nil
	}
	closedSlot := h.tabs[idx].slot
	h.tabs = append(h.tabs[:idx], h.tabs[idx+1:]...)
	// Re-pick active: prefer the tab whose slot is closedSlot-1, fall
	// back to slot 0.
	var newActive int64
	if len(h.tabs) > 0 {
		// Find tab at slot closedSlot-1 if it exists.
		target := closedSlot - 1
		if target < 0 {
			target = 0
		}
		var best *Tab
		for _, tab := range h.tabs {
			if tab.slot == target {
				best = tab
				break
			}
		}
		if best == nil {
			// Fallback: pick the smallest-slot remaining tab.
			best = h.tabs[0]
			for _, tab := range h.tabs[1:] {
				if tab.slot < best.slot {
					best = tab
				}
			}
		}
		newActive = best.id
	}
	h.activeID = newActive
	h.mu.Unlock()
	if h.deps.Driver != nil {
		_ = h.deps.Driver.DeleteView(t.ViewName())
	}
	return nil
}

// PinActive toggles the pinned flag on the active tab. Returns the new
// pinned state. Returns false (with toast) when no tab is active.
func (h *ResultTabsHelper) PinActive() bool {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return false
	}
	return h.Pin(t)
}

// Pin toggles t's pinned flag. Returns the new state.
func (h *ResultTabsHelper) Pin(t *Tab) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	t.pinned = !t.pinned
	pinned := t.pinned
	t.mu.Unlock()
	return pinned
}

// Jump activates the tab at 1-based index i (i.e. <leader>1 → slot 0).
// Out-of-range indices toast "no tab N" and leave the active selection
// unchanged.
func (h *ResultTabsHelper) Jump(i int) {
	h.mu.Lock()
	if len(h.tabs) == 0 {
		h.mu.Unlock()
		h.toast("no result tabs")
		return
	}
	target := i - 1
	var found *Tab
	for _, t := range h.tabs {
		if t.slot == target {
			found = t
			break
		}
	}
	if found == nil {
		h.mu.Unlock()
		h.toast(fmt.Sprintf("no tab %d", i))
		return
	}
	h.activeID = found.id
	h.mu.Unlock()
}

// Cycle moves active to next (dir == +1) / prev (dir == -1) tab in
// slot order, wrapping at boundaries. No-op when no tabs exist.
func (h *ResultTabsHelper) Cycle(dir int) {
	if dir == 0 {
		return
	}
	h.mu.Lock()
	if len(h.tabs) == 0 {
		h.mu.Unlock()
		h.toast("no result tabs")
		return
	}
	// Sort by slot for deterministic cycling order.
	ordered := make([]*Tab, len(h.tabs))
	copy(ordered, h.tabs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].slot < ordered[j].slot })

	currentIdx := 0
	for i, t := range ordered {
		if t.id == h.activeID {
			currentIdx = i
			break
		}
	}
	step := 1
	if dir < 0 {
		step = -1
	}
	next := (currentIdx + step + len(ordered)) % len(ordered)
	h.activeID = ordered[next].id
	h.mu.Unlock()
}

// CancelActive cancels the active tab's stream. For a queued tab the
// queue waiter is aborted without ever calling the driver Cancel
// surface; for a running tab the underlying RunHandle.Cancel runs.
func (h *ResultTabsHelper) CancelActive() error {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return nil
	}
	return h.cancelTab(t)
}

func (h *ResultTabsHelper) cancelTab(t *Tab) error {
	t.mu.Lock()
	state := t.state
	cancelled := t.cancelled
	t.cancelled = true
	t.mu.Unlock()

	if cancelled {
		return nil
	}

	switch state {
	case StateQueued:
		// Drop from queue without driver involvement: signal the queued
		// waiter to bail before it ever issues NewQueryTask. The wait
		// goroutine sets the state to Cancelled and decrements rowCount.
		if t.queuedCancel != nil {
			select {
			case <-t.queuedCancel:
			default:
				close(t.queuedCancel)
			}
		}
		t.mu.Lock()
		t.state = StateCancelled
		t.mu.Unlock()
		return nil
	case StateRunning:
		t.mu.Lock()
		t.state = StateCancelled
		rh := t.rh
		t.mu.Unlock()
		if rh != nil {
			return rh.Cancel()
		}
		return nil
	default:
		// Complete / Errored / Plan / Detached tabs: cancellation is a
		// no-op (idempotent).
		return nil
	}
}

// --- Internal helpers ----------------------------------------------------

// openTab is the streaming-tab entry shared by OpenResultTab and tests.
// rh may be nil for tests that exercise the tab-management layer only.
func (h *ResultTabsHelper) openTab(label string, rh runHandle) error {
	tab, err := h.allocTab(label)
	if err != nil {
		return err
	}

	tab.mu.Lock()
	tab.rh = rh
	tab.mu.Unlock()

	// Determine whether to start streaming immediately or queue.
	priorRunning := h.priorRunningTab(tab.id)
	if priorRunning == nil || rh == nil {
		h.startStreaming(tab)
	} else {
		h.queueBehind(tab, priorRunning)
	}

	h.setActive(tab.id)
	h.materialiseView(tab)
	return nil
}

// allocTab allocates a tab in the next free slot, evicting the oldest
// non-pinned tab if at cap. Returns ErrTabCapReached when every tab is
// pinned. The returned tab has a fresh grid + runner attached (when a
// StreamFactory is wired) but no state set yet — the caller flips
// state immediately.
func (h *ResultTabsHelper) allocTab(label string) (*Tab, error) {
	h.mu.Lock()
	if len(h.tabs) >= h.maxTabs {
		// Find oldest non-pinned candidate.
		var victim *Tab
		for _, t := range h.tabs {
			if t.pinned {
				continue
			}
			if victim == nil || t.createdAt.Before(victim.createdAt) {
				victim = t
			}
		}
		if victim == nil {
			h.mu.Unlock()
			h.toast("tab cap reached; unpin a tab")
			return nil, ErrTabCapReached
		}
		// Evict victim BEFORE materialising the new tab.
		h.mu.Unlock()
		_ = h.Close(victim)
		h.mu.Lock()
	}
	slot := h.nextFreeSlotLocked()
	id := h.nextID.Add(1)
	now := h.now()
	t := &Tab{
		id:        id,
		slot:      slot,
		label:     label,
		createdAt: now,
		state:     StateRunning, // overwritten by caller as needed
		doneCh:    make(chan struct{}),
		grid:      grid.NewView(),
	}
	if h.deps.StreamFactory != nil {
		t.runner = h.deps.StreamFactory()
	}
	h.tabs = append(h.tabs, t)
	h.mu.Unlock()
	return t, nil
}

func (h *ResultTabsHelper) nextFreeSlotLocked() int {
	used := make(map[int]bool, len(h.tabs))
	for _, t := range h.tabs {
		used[t.slot] = true
	}
	for i := 0; i < h.maxTabs; i++ {
		if !used[i] {
			return i
		}
	}
	// Should not reach here — allocTab checks cap first.
	return 0
}

// priorRunningTab returns the most recent (by createdAt) tab other
// than the given excludeID that is currently in StateRunning OR
// StateQueued. Returns nil when none exists.
func (h *ResultTabsHelper) priorRunningTab(excludeID int64) *Tab {
	h.mu.Lock()
	candidates := make([]*Tab, 0, len(h.tabs))
	for _, t := range h.tabs {
		if t.id == excludeID {
			continue
		}
		st := t.State()
		if st == StateRunning || st == StateQueued {
			candidates = append(candidates, t)
		}
	}
	h.mu.Unlock()
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].createdAt.After(candidates[j].createdAt)
	})
	return candidates[0]
}

// startStreaming kicks off the per-tab StreamRunner immediately. tab.rh
// MAY be nil (used by unit tests of the tab-management layer); in that
// case state is set to Running but no NewQueryTask runs.
func (h *ResultTabsHelper) startStreaming(tab *Tab) {
	tab.mu.Lock()
	tab.state = StateRunning
	rh := tab.rh
	runner := tab.runner
	tab.mu.Unlock()
	if rh == nil || runner == nil {
		return
	}
	gridView := tab.grid
	id := tab.id
	streamFn := func(ctx context.Context) (drivers.RowStream, error) {
		_ = ctx
		return rh.Rows(), nil
	}
	appendRows := func(rows []models.Row) {
		gridView.AppendRows(rows)
		tab.mu.Lock()
		tab.rowCount += int64(len(rows))
		tab.mu.Unlock()
	}
	taskKey := fmt.Sprintf("result_tab_%d", id)
	onDone := func() {
		// Finalise tab state from the worker goroutine. Idempotent —
		// dispose() may have already set Cancelled / etc.
		tab.mu.Lock()
		if tab.state == StateRunning {
			if tab.cancelled {
				tab.state = StateCancelled
			} else {
				tab.state = StateComplete
			}
		}
		tab.mu.Unlock()
	}
	_ = runner.NewQueryTask(taskKey, streamFn, appendRows, resultTabInitialRows, onDone)
}

// queueBehind marks tab Queued, opens a queuedCancel chan, and spawns
// a goroutine that waits for prior.rh.Done() before flipping the tab
// into Running and starting the stream. Cancellation of the queued tab
// closes queuedCancel and bails before NewQueryTask is ever called.
func (h *ResultTabsHelper) queueBehind(tab *Tab, prior *Tab) {
	cancelCh := make(chan struct{})
	tab.mu.Lock()
	tab.state = StateQueued
	tab.queuedCancel = cancelCh
	tab.mu.Unlock()

	// If we have no OnWorker plumbing the test path is fully sync — fire
	// the waiter on a plain goroutine; the test cooperates by closing
	// prior.rh.Done() before asserting.
	go func() {
		prior.mu.Lock()
		priorRH := prior.rh
		prior.mu.Unlock()
		var doneCh <-chan struct{}
		if priorRH != nil {
			doneCh = priorRH.Done()
		} else {
			// No prior handle: race with cancelCh — if cancelCh fires
			// first we cancel; otherwise fall through and start.
			doneCh = make(chan struct{})
		}
		select {
		case <-doneCh:
		case <-cancelCh:
			// Queued tab was cancelled before its turn arrived; abort.
			return
		}
		// Verify we still want to run: if cancelTab was called between
		// doneCh firing and our scheduling, bail.
		tab.mu.Lock()
		cancelled := tab.cancelled
		tab.mu.Unlock()
		if cancelled {
			return
		}
		h.startStreaming(tab)
	}()
}

// setActive updates activeID under helper.mu.
func (h *ResultTabsHelper) setActive(id int64) {
	h.mu.Lock()
	h.activeID = id
	h.mu.Unlock()
}

// materialiseView creates the gocui view for the tab at a placeholder
// rect (0,0,0,0). The layout pass repositions per frame; we just need
// the view to exist so SetViewOnTop / DeleteView have a target. Driver
// may be nil in unit tests.
func (h *ResultTabsHelper) materialiseView(tab *Tab) {
	if h.deps.Driver == nil {
		return
	}
	// Create at zero-area; layout pass will resize. Tolerate
	// ErrUnknownView (the "first SetView" sentinel) so creation succeeds.
	_, err := h.deps.Driver.SetView(tab.ViewName(), 0, 0, 1, 1, 0)
	if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
		// Best-effort: log via toast if available, otherwise swallow.
		h.toast(fmt.Sprintf("result tab view error: %v", err))
	}
}

// findByIDLocked returns the tab with the supplied id under helper.mu.
// Callers must hold h.mu.
func (h *ResultTabsHelper) findByIDLocked(id int64) *Tab {
	if id == 0 {
		return nil
	}
	for _, t := range h.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

func (h *ResultTabsHelper) toast(msg string) {
	if h.deps.Toast == nil {
		return
	}
	h.deps.Toast.Show(msg, resultTabToastTTL)
}

// LayoutPaint is called from the orchestrator's layout pass. It
// positions every existing result-tab view at rect; the active tab is
// raised to the top via SetViewOnTop so it occlude the others. Tabs
// also have their titles refreshed.
//
// Returns the name of the active tab view (or "" if none) so the
// caller can hand it to SetCurrentView when appropriate.
func (h *ResultTabsHelper) LayoutPaint(driver types.GuiDriver, x0, y0, x1, y1 int) string {
	if driver == nil {
		return ""
	}
	tabs := h.Tabs()
	activeID := h.activeIDSnapshot()
	var activeName string
	for _, t := range tabs {
		name := t.ViewName()
		view, err := driver.SetView(name, x0, y0, x1, y1, 0)
		if err != nil && !errors.Is(err, gocui.ErrUnknownView) {
			continue
		}
		// Refresh title every frame (state / row count may have changed).
		if view != nil {
			view.Title = t.Title()
			// Render grid contents (no-op for plan / error tabs).
			if g := t.Grid(); g != nil {
				g.Render(view)
			} else if t.State() == StatePlan {
				_ = driver.SetContent(name, t.planRawSnapshot())
			} else if errTab := t.Err(); errTab != nil {
				_ = driver.SetContent(name, errTab.Error())
			}
		}
		if t.id == activeID {
			activeName = name
		}
	}
	if activeName != "" {
		_, _ = driver.SetViewOnTop(activeName)
	}
	return activeName
}

func (h *ResultTabsHelper) activeIDSnapshot() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activeID
}

// planRawSnapshot exposes the cached raw plan text for layout
// rendering. Held under the tab mutex.
func (t *Tab) planRawSnapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.planRaw
}

// dispose cancels the tab's stream, waits for Done, and tears down the
// runner. Safe to call multiple times.
func (t *Tab) dispose() {
	t.disposeOnce.Do(func() {
		t.disposed.Store(true)
		t.mu.Lock()
		state := t.state
		rh := t.rh
		runner := t.runner
		cancelCh := t.queuedCancel
		t.cancelled = true
		t.mu.Unlock()

		switch state {
		case StateQueued:
			if cancelCh != nil {
				select {
				case <-cancelCh:
				default:
					close(cancelCh)
				}
			}
		case StateRunning:
			if rh != nil {
				_ = rh.Cancel()
				// Wait for terminal Done, with a generous cap so a
				// misbehaving driver can't deadlock the UI thread.
				select {
				case <-rh.Done():
				case <-time.After(2 * time.Second):
				}
			}
		}
		if runner != nil {
			runner.Stop()
		}
		t.mu.Lock()
		// Final state pinning: anything still Running gets cancelled.
		if t.state == StateRunning {
			t.state = StateCancelled
		}
		t.mu.Unlock()
		close(t.doneCh)
	})
}

// truncateLabel cleans whitespace and truncates to cap with an
// ellipsis. Mirrors controllers.tabLabel; redeclared here so the
// helper can be tested without importing controllers (cycle).
func truncateLabel(s string, cap int) string {
	clean := strings.Join(strings.Fields(s), " ")
	if len(clean) <= cap {
		return clean
	}
	return clean[:cap] + "…"
}

// NewRBMStreamFactory builds the production StreamRunnerFactory by
// closing over the orchestrator's threading helpers. The returned
// factory allocates one *tasks.ResultBufferManager per tab.
//
// onUI may be either OnUIThread or OnUIThreadContentOnly; for
// high-frequency row deliveries OnUIThreadContentOnly is preferred
// (DESIGN.md §6).
func NewRBMStreamFactory(
	onWorker func(func(gocui.Task) error),
	onUI func(func() error),
	newRBM func(onWorker func(func(gocui.Task) error), onUI func(func() error)) StreamRunner,
) StreamRunnerFactory {
	if newRBM == nil {
		return nil
	}
	return func() StreamRunner {
		return newRBM(onWorker, onUI)
	}
}

// Compile-time: *session.RunHandle satisfies the helper's runHandle
// interface. If session.RunHandle drops Done()/Cancel()/Rows() this
// fails to compile (catches contract drift early).
var _ runHandle = (*session.RunHandle)(nil)
