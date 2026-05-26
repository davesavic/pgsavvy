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

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/env"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/exporter"
	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
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
//
// dbsavvy-uv0.3 extends this interface with ReadRows / ReadToEnd / an
// EstimatedRows accessor so the helper can drive pagination + the
// G-with-warn flow without importing pkg/tasks (cycle).
type StreamRunner interface {
	NewQueryTask(
		taskKey string,
		streamFn func(ctx context.Context) (drivers.RowStream, error),
		appendRows func([]models.Row),
		initialRows int,
		onDone func(),
	) error
	Stop()

	// ReadRows enqueues a non-blocking request to drain up to n more
	// rows from the active stream. No-op when idle.
	ReadRows(n int)

	// ReadToEnd enqueues a request to drain the stream to completion,
	// invoking then exactly once on completion. When idle, then fires
	// synchronously so callers can rely on the callback in either case.
	ReadToEnd(then func())

	// EstimatedRows returns the optimiser's row-count estimate for the
	// active stream, or 0 when unknown.
	EstimatedRows() int64
}

// StreamRunnerFactory builds a StreamRunner per tab. Real production
// wraps tasks.New(OnWorker, OnUIThread).
type StreamRunnerFactory func() StreamRunner

// toastShower is the narrow surface the helper uses for surface-level
// toasts. *ui.ToastHelper satisfies it; tests inject a recorder.
type toastShower interface {
	Show(message string, ttl time.Duration)
}

// confirmer is the narrow surface ResultTabsHelper uses to prompt the
// user before kicking off a potentially-expensive ReadToEnd. The concrete
// satisfier is *ui.ConfirmHelper; nil disables the warning path
// (G fires unconditionally).
type confirmer interface {
	Confirm(title, body string, onYes, onNo func() error) error
}

// onUIThreader is the narrow surface used to marshal off-thread state
// flips (e.g. the complete flag) onto the gocui MainLoop. Mirrors
// orchestrator.Gui.OnUIThread.
type onUIThreader func(func() error)

// prompter is the narrow surface ResultTabsHelper uses to open the
// /regex prompt. The concrete satisfier is *ui.PromptHelper; tests may
// inject a fake. nil disables the filter-prompt path (chord becomes
// a no-op). dbsavvy-uv0.4.
type prompter interface {
	Prompt(label, initial string, onSubmit func(value string) error, onCancel func() error) error
}

// chooser is the narrow surface ResultTabsHelper uses to open the
// <leader>s sort picker. The concrete satisfier is *ui.ChoiceHelper;
// tests inject a fake. nil disables the sort-picker path. dbsavvy-uv0.5.
type chooser interface {
	Choose(label string, choices []string, onSubmit func(idx int) error, onCancel func() error) error
}

// toastUpdater extends toastShower with the once-per-tab caveat key
// surface. *ui.ToastHelper satisfies it; tests inject a recorder. The
// FilterPrompt handler reaches for ShowOrUpdate when present (so a
// repeated caveat replaces in place); otherwise it falls back to Show.
// dbsavvy-uv0.4.
type toastUpdater interface {
	ShowOrUpdate(key, message string, ttl time.Duration)
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

	// Confirm pushes a confirmation popup. Used by ReadToEnd above
	// ReadToEndWarnThreshold to make the user explicitly opt in to a
	// large drain. nil disables the warning path. dbsavvy-uv0.3.
	Confirm confirmer

	// OnUIThread marshals a closure onto the gocui MainLoop. Used by
	// the helper to flip Tab.complete from a worker goroutine (the
	// onDone / then callbacks of ReadToEnd fire off-thread). nil means
	// "run synchronously" — fine for the unit-test path; production
	// wires Gui.OnUIThread. dbsavvy-uv0.3.
	OnUIThread onUIThreader

	// ResultPageSize is the page size for explicit ]p / [p chord
	// requests. Falls back to grid.ResultPageSize (200) when 0.
	// dbsavvy-uv0.3.
	ResultPageSize int

	// ReadToEndWarnThreshold is the estimated-rows ceiling above which
	// G first shows a confirmation prompt. 0 means "use the shipped
	// default (1_000_000)". dbsavvy-uv0.3.
	ReadToEndWarnThreshold int64

	// Prompt pushes the single-line prompt for the /regex chord. nil
	// disables the filter-prompt path. dbsavvy-uv0.4.
	Prompt prompter

	// FilterMaxRegexBytes caps the byte length of /regex sources accepted
	// by SetFilter. 0 means "use grid's default cap (4096)". dbsavvy-uv0.4.
	FilterMaxRegexBytes int

	// Choice pushes the column-picker overlay used by <leader>s. nil
	// disables the sort-picker path (chord becomes a no-op). dbsavvy-uv0.5.
	Choice chooser

	// SortPickLabel is the picker label rendered above the column list.
	// "" falls back to "sort by column". dbsavvy-uv0.5.
	SortPickLabel string

	// MouseDoubleClickMs is the maximum gap (in milliseconds) that still
	// counts as a double-click on a grid header. 0 falls back to grid's
	// default (400ms). dbsavvy-uv0.5.
	MouseDoubleClickMs int

	// Store is the *common.AppStateStore used to seed/persist the
	// per-(connID, baseTable) hidden-column set. nil disables persistence
	// (overlay still works session-only). dbsavvy-uv0.6.
	Store *common.AppStateStore

	// PushHideOverlay pushes the HIDE_OVERLAY context onto the focus
	// stack. Invoked by HideOverlay() after the helper has stashed the
	// overlay state object. nil disables the modal push (overlay state
	// is built but the popup never appears) — production wires a closure
	// over (registry.HideOverlay.SetState(adapter); tree.Push(registry.HideOverlay)).
	// dbsavvy-uv0.6.
	PushHideOverlay func() error

	// PopHideOverlay pops the HIDE_OVERLAY context off the focus stack.
	// Invoked by HideOverlayClose() after the helper has committed the
	// final hidden set + persisted it. nil disables the pop — production
	// wires a closure over tree.Pop(). dbsavvy-uv0.6.
	PopHideOverlay func() error

	// PushExportMenu pushes the EXPORT_MENU context onto the focus stack.
	// Invoked by PromptExport(). dbsavvy-uv0.9.
	PushExportMenu func() error
	// PopExportMenu pops the EXPORT_MENU context off the focus stack.
	PopExportMenu func() error

	// OnWorker dispatches a closure onto a background worker goroutine
	// (mirrors orchestrator.Gui.OnWorker). The <leader>oe export pipeline
	// uses this to run exporter.Run off the UI thread. nil disables the
	// worker path — ExportMenuConfirm will toast a failure. dbsavvy-uv0.9.
	OnWorker func(func(gocui.Task) error)

	// ExportBufferedRowWarnThreshold is the row-count ceiling above which
	// the export menu's "buffered" formats (Markdown, JSON Array) gate
	// behind a typed-YES confirmation. 0 means "use the shipped default
	// (100_000)". dbsavvy-uv0.9.
	ExportBufferedRowWarnThreshold int64

	// ExportClipboardMaxBytes caps the payload size pushed to the system
	// clipboard. 0 means "use the shipped default (16 MiB)". dbsavvy-uv0.9.
	ExportClipboardMaxBytes int64

	// IntrospectEditability decides whether a completed result is inline-
	// editable. Wired by the orchestrator to a closure that acquires a
	// session and runs the driver-specific introspection
	// (pg.EditabilityIntrospect + pg.ApplyConnectionGate). Returns
	// (editable, SELECT-order row-identity indexes, disabledReason). nil
	// keeps editability off (unit-test default). dbsavvy-2b6.
	IntrospectEditability func(ctx context.Context, cols []models.ColumnMeta) (bool, []int, string)
}

// defaultReadToEndWarnThreshold is the shipped ceiling above which G
// first prompts for confirmation. Mirrors the config default
// (ui.read_to_end_warn_threshold = 1_000_000). dbsavvy-uv0.3.
const defaultReadToEndWarnThreshold int64 = 1_000_000

// ResultTabsHelper owns the multi-result-tab pane in the orchestrator's
// "secondary" window slot. It is the concrete satisfier of
// controllers.ResultTabsHelper.
//
// dbsavvy-66p.12.
type ResultTabsHelper struct {
	deps            ResultTabsHelperDeps
	maxTabs         int
	nextID          atomic.Int64
	now             func() time.Time
	pageSize        int
	warnThreshold   int64
	filterByteLimit int
	sortPickLabel   string
	doubleClickMs   int

	mu       sync.Mutex
	tabs     []*Tab // ordered by Slot (0..max-1)
	activeID int64  // 0 when no tab is active

	// hideOverlay tracks the currently-open <leader>gH overlay, if any.
	// nil when no overlay is active. dbsavvy-uv0.6.
	hideOverlay *activeHideOverlay

	// exportMenu tracks the currently-open <leader>oe export menu.
	// nil when no menu is active. Accessed under h.mu. dbsavvy-uv0.9.
	exportMenu *activeExportMenu

	// onTabRemoved fires after a tab is removed via Close (which is also
	// the eviction path: allocTab calls Close(victim) when at cap). The
	// callback receives the closed tab's stringified ID so collaborators
	// (e.g. ResultJumpList.PruneByTab) can drop stale references. Default
	// no-op; wired via SetOnTabRemoved. dbsavvy-bwq.15.
	onTabRemoved func(tabID string)
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
	pageSize := deps.ResultPageSize
	if pageSize <= 0 {
		pageSize = grid.ResultPageSize
	}
	warn := deps.ReadToEndWarnThreshold
	if warn <= 0 {
		warn = defaultReadToEndWarnThreshold
	}
	filterCap := deps.FilterMaxRegexBytes
	if filterCap <= 0 {
		filterCap = 4096
	}
	sortLabel := deps.SortPickLabel
	if sortLabel == "" {
		sortLabel = "sort by column"
	}
	dblClick := deps.MouseDoubleClickMs
	if dblClick <= 0 {
		dblClick = 400
	}
	return &ResultTabsHelper{
		deps:            deps,
		maxTabs:         max,
		now:             now,
		pageSize:        pageSize,
		warnThreshold:   warn,
		filterByteLimit: filterCap,
		sortPickLabel:   sortLabel,
		doubleClickMs:   dblClick,
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
	planCtx   *guicontext.PlanContext // non-nil for plan tabs (dbsavvy-uv0.8)
	cancelled bool

	// complete flips true when the stream has been drained to EOF
	// (either via clean stream end in onDone, or via the then-callback
	// of an explicit ReadToEnd request). Surfaced in Title() as a
	// "(complete)" suffix and used to drop the "~" approximate prefix
	// from the row count. The flip is marshalled through the
	// ResultBufferManager's onUIThread callback so off-thread writers
	// don't race with rendering. dbsavvy-uv0.3.
	complete bool

	// caveatShown gates the once-per-tab "/regex filter loaded rows only"
	// toast. Flipped true by the filter chord handler the first time a
	// filter is applied to an incomplete tab; reset to false whenever
	// the helper attaches a fresh schema to the tab's grid (re-run in
	// the same tab fires a fresh caveat). dbsavvy-uv0.4.
	caveatShown bool

	rh           runHandle
	grid         *grid.View
	runner       StreamRunner
	queuedCancel chan struct{} // closed to abort the queued-wait goroutine
	disposeOnce  sync.Once
	disposed     atomic.Bool

	// baseCtx is the IBaseContext attached to this tab so the focus stack
	// can route to result_tab_<slot> through rail-switch bindings
	// (dbsavvy-usj). For plan tabs the active context surfaced through
	// Context() is planCtx instead — plan tabs carry their own PLAN-keyed
	// context for the plan controller's bindings.
	baseCtx guicontext.BaseContext

	doneCh chan struct{} // closed when the tab is fully torn down

	// connID + resultIdentity carry the per-tab persistence key for the
	// hide-cols set. connID is "" and resultIdentity is the zero value
	// until the caller invokes SetIdentity (typically right after
	// OpenResultTab when both the connection and SQL are in hand).
	// dbsavvy-uv0.6.
	connID         string
	resultIdentity query.ResultIdentity
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

// Runner returns the per-tab StreamRunner, or nil when the tab has no
// stream attached (plan / error tabs, or test wiring). Read-only
// accessor used by the <leader>oe export pipeline. dbsavvy-uv0.9.
func (t *Tab) Runner() StreamRunner {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.runner
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

// Title builds the rendered title:
//
//	"result N: <label> (<state>, ~M rows)"          (in-flight)
//	"result N: <label> (<state>, M rows) (complete)" (after EOF)
//
// The "~" prefix on the row count marks an approximate (still-streaming)
// count; it is dropped once the tab has been marked complete. Label is
// truncated to resultTabLabelMax characters. dbsavvy-uv0.3.
func (t *Tab) Title() string {
	t.mu.Lock()
	state := t.state
	rows := t.rowCount
	complete := t.complete
	t.mu.Unlock()
	label := truncateLabel(t.label, resultTabLabelMax)
	rowsSegment := fmt.Sprintf("~%d rows", rows)
	suffix := ""
	if complete {
		rowsSegment = fmt.Sprintf("%d rows", rows)
		suffix = " (complete)"
	}
	return fmt.Sprintf("result %d: %s (%s, %s)%s", t.slot+1, label, state, rowsSegment, suffix)
}

// Complete reports whether the tab's stream has been drained to EOF.
// dbsavvy-uv0.3.
func (t *Tab) Complete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.complete
}

// CaveatShown reports whether the once-per-tab /regex caveat toast has
// already fired for this tab. dbsavvy-uv0.4.
func (t *Tab) CaveatShown() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.caveatShown
}

// SetCaveatShown flips the caveat-shown gate. The /regex chord handler
// sets it true after firing the once-per-tab toast; startStreaming
// resets it to false on a fresh schema attach. dbsavvy-uv0.4.
func (t *Tab) SetCaveatShown(v bool) {
	t.mu.Lock()
	t.caveatShown = v
	t.mu.Unlock()
}

// SetIdentity records the (connID, ResultIdentity) pair used by
// dbsavvy-uv0.6 to gate hide-col persistence. The caller (typically
// QueryEditorController right after OpenResultTab) supplies the active
// connection ID and the heuristic result from
// query.DetectFromQuery(sql). A zero ResultIdentity is valid — the
// hide-cols overlay falls back to session-only mode in that case.
func (t *Tab) SetIdentity(connID string, ri query.ResultIdentity) {
	t.mu.Lock()
	t.connID = connID
	t.resultIdentity = ri
	t.mu.Unlock()
}

// Identity returns the (connID, ResultIdentity) pair previously recorded
// via SetIdentity. dbsavvy-uv0.6.
func (t *Tab) Identity() (string, query.ResultIdentity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connID, t.resultIdentity
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
//
// dbsavvy-uv0.8: each plan tab gets its own *context.PlanContext bound
// to the tab's view name. The context owns the per-tab tree state
// (collapse map, cursor, raw toggle) — discarded when the tab is closed.
// PlanController handlers look up the active plan context through the
// orchestrator-supplied resolver (see controllers.PlanContextResolver).
func (h *ResultTabsHelper) OpenPlanTab(label string, plan models.Plan) error {
	tab, err := h.allocTab(label)
	if err != nil {
		return err
	}
	planCtx := guicontext.NewPlanContext(
		guicontext.NewBaseContext(guicontext.BaseContextOpts{
			Key:      types.PLAN,
			ViewName: tab.ViewName(),
			Kind:     types.MAIN_CONTEXT,
			Title:    label,
		}),
		guicontext.Deps{}, // GuiDriver is nil-safe; LayoutPaint uses the driver directly via RenderBody
		plan,
	)
	tab.mu.Lock()
	tab.state = StatePlan
	tab.plan = plan
	tab.planRaw = plan.RawText
	tab.planCtx = planCtx
	// allocTab eagerly creates a grid for every tab; a plan tab has no
	// stream so it must drop the grid. Otherwise LayoutPaint's
	// "if g := t.Grid(); g != nil { g.Render }" branch wins over the
	// StatePlan branch and paints the empty grid's "(0 rows)"
	// EmptyResultIndicator over the plan tree (dbsavvy-6pb).
	tab.grid = nil
	tab.mu.Unlock()
	h.setActive(tab.id)
	h.materialiseView(tab)
	return nil
}

// Context returns the IBaseContext the focus stack should push to land
// on this tab. Plan tabs surface their PlanContext (PLAN key, PLAN
// bindings); every other tab surfaces a result_tab_<slot>-keyed
// BaseContext built at allocTab. dbsavvy-usj.
func (t *Tab) Context() types.IBaseContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.planCtx != nil {
		return t.planCtx
	}
	return &t.baseCtx
}

// ActiveContext returns the IBaseContext of the currently-active tab,
// or nil when no tab exists. The rail-switch-to-results handler calls
// this when pushing focus onto the result pane. dbsavvy-usj.
func (h *ResultTabsHelper) ActiveContext() types.IBaseContext {
	t := h.Active()
	if t == nil {
		return nil
	}
	return t.Context()
}

// ActivePlanContext returns the *context.PlanContext attached to the
// currently-active tab, or nil when no plan tab is active. Wired into
// the controllers.PlanController via a closure during bootstrap so
// PLAN-scoped keybindings can mutate the live plan state without
// touching the helper's internals. dbsavvy-uv0.8.
func (h *ResultTabsHelper) ActivePlanContext() *guicontext.PlanContext {
	t := h.Active()
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.planCtx
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
	// Drop the eagerly-created grid so LayoutPaint reaches the Err()
	// branch instead of painting the empty grid's "(0 rows)" over the
	// error message (dbsavvy-6pb).
	tab.grid = nil
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
	closedID := h.tabs[idx].id
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
	cb := h.onTabRemoved
	h.mu.Unlock()
	if h.deps.Driver != nil {
		_ = h.deps.Driver.DeleteView(t.ViewName())
	}
	if cb != nil {
		cb(fmt.Sprintf("%d", closedID))
	}
	return nil
}

// SetOnTabRemoved registers a callback fired after a tab is removed
// (via Close OR the eviction path in allocTab, which itself calls Close
// on the victim). The callback receives the closed tab's stringified ID
// so collaborators (e.g. ResultJumpList.PruneByTab) can drop stale
// references. Passing nil unhooks. dbsavvy-bwq.15.
func (h *ResultTabsHelper) SetOnTabRemoved(fn func(tabID string)) {
	h.mu.Lock()
	h.onTabRemoved = fn
	h.mu.Unlock()
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

// SwitchToTabByID activates the tab whose ID stringifies to tabID and
// returns it. Returns nil when the tab no longer exists (closed since
// the JumpEntry was pushed) — callers (jump-back/forward) treat nil as
// a stale entry and surface a toast. The active grid is NOT moved here;
// the caller positions the cursor via grid.View.SetCursor after the
// switch. dbsavvy-8oo stub #4.
func (h *ResultTabsHelper) SwitchToTabByID(tabID string) *Tab {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.tabs {
		if fmt.Sprintf("%d", t.id) == tabID {
			h.activeID = t.id
			return t
		}
	}
	return nil
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

// Page advances (dir > 0) or rewinds (dir < 0) the active tab's grid
// by one page (helper.pageSize rows). Forward paging requests more rows
// from the active stream via runner.ReadRows; backward paging just
// repositions the cursor at the top of the visible window. No-op when
// no tab is active. dbsavvy-uv0.3.
//
// When the stream is already complete, forward paging is a no-op
// (the rule "[]p when stream is already complete: no-op") to avoid
// firing a needless ReadRows that would hit EOF.
func (h *ResultTabsHelper) Page(dir int) {
	if dir == 0 {
		return
	}
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	pageSize := h.pageSize
	if pageSize <= 0 {
		pageSize = grid.ResultPageSize
	}
	if dir > 0 {
		// Forward: only request more rows when the stream isn't
		// already complete. After the request lands, jump cursor to
		// the new tail so the user sees the freshly-fetched page.
		if !t.Complete() {
			t.mu.Lock()
			runner := t.runner
			t.mu.Unlock()
			if runner != nil {
				runner.ReadRows(pageSize)
			}
		}
		g.JumpLast()
		return
	}
	// Backward: rewind the cursor by one page; the next Render clamps
	// the viewport so the cursor lands at the top of the visible
	// window. Implementation: step HalfPageUp twice for now (mirrors
	// the existing scroll verbs). Note: this is the minimum-viable
	// surface; a dedicated PageUp verb is a follow-up.
	for i := 0; i < 2; i++ {
		g.HalfPageUp()
	}
}

// ReadToEnd drains the active tab's stream to completion. Above
// helper.warnThreshold (or when EstimatedRows is unknown == 0 AND the
// stream isn't already complete with zero rows), it first shows a
// confirmation prompt; the drain only fires after the user accepts.
// dbsavvy-uv0.3.
//
// Semantics (see dbsavvy-uv0.3 AC "G with >1M warn"):
//   - complete && rowsLoaded==0 → no-op
//   - !complete && EstimatedRows()==0 → prompt (unknown = conservative)
//   - !complete && EstimatedRows()>warnThreshold → prompt
//   - !complete && 0 < EstimatedRows() ≤ warnThreshold → fire without prompt
func (h *ResultTabsHelper) ReadToEnd() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	t.mu.Lock()
	complete := t.complete
	rows := t.rowCount
	runner := t.runner
	t.mu.Unlock()
	if complete && rows == 0 {
		// Empty + already complete: nothing to drain. No-op.
		return
	}
	if runner == nil {
		// No stream attached (test / plan / error tab); no-op.
		return
	}
	if complete {
		// Already complete with rows loaded: nothing more to drain.
		return
	}
	est := runner.EstimatedRows()
	shouldPrompt := est == 0 || est > h.warnThreshold
	if shouldPrompt && h.deps.Confirm != nil {
		title := "Drain result to end?"
		body := h.readToEndPromptBody(est)
		_ = h.deps.Confirm.Confirm(title, body, func() error {
			h.fireReadToEnd(t, runner)
			return nil
		}, func() error {
			// User declined; nothing to do.
			return nil
		})
		return
	}
	h.fireReadToEnd(t, runner)
}

// fireReadToEnd issues the drain request and registers the
// completion-flip callback. dbsavvy-uv0.3.
func (h *ResultTabsHelper) fireReadToEnd(tab *Tab, runner StreamRunner) {
	if runner == nil {
		return
	}
	runner.ReadToEnd(func() {
		h.markCompleteOnUI(tab)
	})
}

// readToEndPromptBody builds the confirmation popup body. dbsavvy-uv0.3.
func (h *ResultTabsHelper) readToEndPromptBody(est int64) string {
	if est <= 0 {
		return fmt.Sprintf("Estimated row count is unknown. Draining could be slow or consume a lot of memory.\nPress <CR> to proceed, <esc> to cancel. (warn threshold: %d)", h.warnThreshold)
	}
	return fmt.Sprintf("Estimated %d rows (above warn threshold of %d). Draining may be slow.\nPress <CR> to proceed, <esc> to cancel.", est, h.warnThreshold)
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

// PreemptInFlight stops every in-flight result-tab stream so the
// per-session queue serializer (SQLSession.streamMu) is released before a
// new run tries to acquire it. dbsavvy-dk6: a streamed result larger than
// the initial-fill window parks its RBM worker on the chan loop while
// still holding streamMu — the worker never reaches EOF, so RunHandle's
// finish() (which unlocks the queue) never runs. A subsequent synchronous
// Stream on the UI goroutine would then block on streamMu forever and
// freeze the TUI. rh.Cancel() does not help (a parked worker never calls
// Next to observe the driver cancel); only Stop() makes the worker return,
// close its stream, and release the lock — and it does so on the worker's
// own goroutine, so the caller's next Stream.Lock proceeds rather than
// deadlocking.
//
// Running tabs keep the rows already rendered; their state flips to
// Cancelled. Queued tabs' waiters are aborted (their queuedCancel is
// closed) without a driver round-trip — and before Running tabs are
// stopped, so a queued waiter cannot auto-start when the prior stream's
// Done closes.
func (h *ResultTabsHelper) PreemptInFlight() {
	h.mu.Lock()
	var queuedChans []chan struct{}
	var runners []StreamRunner
	for _, t := range h.tabs {
		t.mu.Lock()
		switch t.state {
		case StateQueued:
			t.state = StateCancelled
			t.cancelled = true
			if t.queuedCancel != nil {
				queuedChans = append(queuedChans, t.queuedCancel)
			}
		case StateRunning:
			t.state = StateCancelled
			t.cancelled = true
			if t.runner != nil {
				runners = append(runners, t.runner)
			}
		}
		t.mu.Unlock()
	}
	h.mu.Unlock()

	// Abort queued waiters first so stopping a Running tab (which closes
	// its RunHandle Done) cannot wake a waiter into starting its stream.
	for _, ch := range queuedChans {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	for _, r := range runners {
		r.Stop()
	}
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
		runner := t.runner
		t.mu.Unlock()
		var cancelErr error
		if rh != nil {
			cancelErr = rh.Cancel()
		}
		// rh.Cancel() alone does not release the per-session streamMu: a
		// worker parked past the initial-fill window never calls Next to
		// observe the driver cancel, so RunHandle.finish() (which unlocks
		// the queue) never runs and the lock leaks under this now-Cancelled
		// tab — a subsequent run then deadlocks the UI thread on
		// Stream.Lock() (dbsavvy-dk6). Stop() forces the worker to return,
		// close its stream, and release the lock (on the worker goroutine,
		// before close(doneCh)), mirroring dispose()/Close on a running tab.
		if runner != nil {
			runner.Stop()
		}
		return cancelErr
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
		baseCtx: guicontext.NewBaseContext(guicontext.BaseContextOpts{
			// Key=RESULT_GRID so all tabs share the scope the
			// ResultTabsController and master editor publish bindings
			// under (gd, gD, <c-o>, <c-i>, <leader>c*, gt/gT, …).
			// ViewName stays per-slot so gocui SetCurrentView targets
			// the right dynamic view.
			Key:      types.RESULT_GRID,
			ViewName: string(types.ResultTabKey(slot)),
			Kind:     types.MAIN_CONTEXT,
			Title:    label,
		}),
	}
	// Propagate the configured /regex byte cap into the grid view so a
	// hot-reloaded config value takes effect on the next tab's filter.
	// dbsavvy-uv0.4.
	t.grid.SetFilterMaxRegexBytes(h.filterByteLimit)
	// Propagate the configured double-click window onto the grid so the
	// header mouse-debounce uses the user's tuned value. dbsavvy-uv0.5.
	t.grid.SetMouseDoubleClickMs(h.doubleClickMs)
	// Seed the grid's viewMode from AppState.LastResultViewMode so a
	// new tab opens in the user's last-chosen mode (dbsavvy-uv0.7). An
	// empty string normalises to "grid" inside SetViewMode.
	if h.deps.Store != nil {
		t.grid.SetViewMode(h.deps.Store.LastResultViewModeSnapshot())
	}
	if h.deps.StreamFactory != nil {
		t.runner = h.deps.StreamFactory()
	}
	// Wire the EstimatedRows loader so expanded-mode renders the
	// "~total" separator with the optimiser estimate (dbsavvy-uv0.7).
	if t.runner != nil {
		runner := t.runner
		t.grid.SetEstimatedRowsLoader(runner.EstimatedRows)
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
//
// dbsavvy-uv0.3: wires grid.View.SetOnNearTail to runner.ReadRows so the
// auto-prefetch path fires when the cursor crosses PrefetchThreshold,
// and flips Tab.complete in the onDone closure (marshalled through
// deps.OnUIThread so the rendering thread is the one that observes the
// state transition).
func (h *ResultTabsHelper) startStreaming(tab *Tab) {
	tab.mu.Lock()
	tab.state = StateRunning
	rh := tab.rh
	runner := tab.runner
	// Fresh schema attach: reset the once-per-tab /regex caveat gate so
	// a re-run in the same tab re-fires the caveat. dbsavvy-uv0.4.
	tab.caveatShown = false
	tab.mu.Unlock()

	gridView := tab.grid

	// Install the result-set schema on the grid BEFORE any rows are
	// appended. RowStream.Columns() is safe to call before the first
	// Next — drivers capture FieldDescriptions at Stream() time. Without
	// this, the grid stays at zero columns and renders the
	// EmptyResultIndicator "(0 rows)" regardless of how many rows the
	// stream actually produces (dbsavvy-dqp).
	var cols []models.ColumnMeta
	if gridView != nil && rh != nil {
		if rs := rh.Rows(); rs != nil {
			cols = rs.Columns()
			gridView.SetColumns(cols)
		}
	}

	if rh == nil || runner == nil {
		return
	}
	if gridView != nil && runner != nil {
		gridView.SetOnNearTail(func(n int) {
			runner.ReadRows(n)
		})
	}
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
		// Finalise tab state from the worker goroutine. The state +
		// complete flip is marshalled onto the UI thread so the next
		// Render reads a consistent snapshot. Idempotent — dispose()
		// may have already set Cancelled / etc.
		h.markCompleteOnUI(tab)
		h.scheduleEditabilityIntrospect(tab, cols)
	}
	_ = runner.NewQueryTask(taskKey, streamFn, appendRows, resultTabInitialRows, onDone)
}

// markCompleteOnUI schedules the Tab.complete + state finalisation flip
// onto the UI thread. When deps.OnUIThread is nil the flip runs
// synchronously (test path). dbsavvy-uv0.3.
func (h *ResultTabsHelper) markCompleteOnUI(tab *Tab) {
	flip := func() error {
		tab.mu.Lock()
		if tab.state == StateRunning {
			if tab.cancelled {
				tab.state = StateCancelled
			} else {
				tab.state = StateComplete
			}
		}
		tab.complete = true
		tab.mu.Unlock()
		return nil
	}
	if h.deps.OnUIThread != nil {
		h.deps.OnUIThread(flip)
		return
	}
	_ = flip()
}

// scheduleEditabilityIntrospect runs the (driver-agnostic) editability
// introspection for a completed tab off the UI thread, then marshals the
// SetEditability flip back onto the UI thread. No-op when the hook is
// unwired or the tab has no grid. dbsavvy-2b6.
func (h *ResultTabsHelper) scheduleEditabilityIntrospect(tab *Tab, cols []models.ColumnMeta) {
	if h.deps.IntrospectEditability == nil || tab == nil || tab.grid == nil {
		return
	}
	gridView := tab.grid
	run := func() {
		editable, rowID, reason := h.deps.IntrospectEditability(context.Background(), cols)
		flip := func() error {
			gridView.SetEditability(editable, rowID, reason)
			return nil
		}
		if h.deps.OnUIThread != nil {
			h.deps.OnUIThread(flip)
			return
		}
		_ = flip()
	}
	if h.deps.OnWorker != nil {
		h.deps.OnWorker(func(gocui.Task) error { run(); return nil })
		return
	}
	run() // test path: synchronous
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
//
// dbsavvy-uv0.5: also registers the per-view left-click mouse binding
// used by the grid header double-click → SetSort flow. The binding is
// best-effort (errors are swallowed by keys.RegisterMouseBinding) so a
// terminal without mouse support degrades cleanly.
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
	h.wireGridMouseClick(tab)
}

// wireGridMouseClick registers the grid-header left-click binding on the
// tab's view. The handler maps the click X/Y onto the grid's column
// layout and forwards to grid.View.HandleHeaderClick, which owns the
// debounce + SetSort cycle. Plan / error tabs have a nil grid so the
// handler becomes a no-op for them. dbsavvy-uv0.5.
func (h *ResultTabsHelper) wireGridMouseClick(tab *Tab) {
	g := tab.Grid()
	if g == nil {
		return
	}
	view := tab.ViewName()
	now := h.now
	handler := func(opts types.ViewMouseBindingOpts) error {
		g.HandleHeaderClick(opts.X, opts.Y, now())
		return nil
	}
	binding := &types.ViewMouseBinding{
		ViewName: view,
		Key:      types.MouseLeft,
		Modifier: types.ModNone,
		Handler:  handler,
	}
	// SetViewClickBinding errors are swallowed: the TUI must remain
	// usable when the terminal refuses mouse mode (dbsavvy-zro AC).
	_ = h.deps.Driver.SetViewClickBinding(binding)
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
			title := t.Title()
			view.Title = title // stands for plan/error/empty tabs (which skip Grid.Render)
			// Render grid contents (no-op for plan / error tabs).
			if g := t.Grid(); g != nil {
				// Propagate the tab title into the grid so Render's
				// snapshot (v.title + sortIndicator) carries it; otherwise
				// Render's `target.Title = snap.title` clobbers the line
				// above with an empty title. dbsavvy-tzi.4.
				g.SetTitle(title)
				g.Render(view)
			} else if t.State() == StatePlan {
				// dbsavvy-uv0.8: prefer the PlanContext-rendered tree
				// body. Falls back to raw text when planCtx is missing
				// (defensive: should not happen post-OpenPlanTab, but
				// keeps the layout pass nil-safe).
				if pc := t.planContextSnapshot(); pc != nil {
					_ = driver.SetContent(name, pc.RenderBody())
				} else {
					_ = driver.SetContent(name, t.planRawSnapshot())
				}
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

// planContextSnapshot exposes the tab's *context.PlanContext (or nil)
// under the tab mutex. dbsavvy-uv0.8.
func (t *Tab) planContextSnapshot() *guicontext.PlanContext {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.planCtx
}

// PlanContext returns the *context.PlanContext attached to this tab, or
// nil for non-plan tabs. Exported so callers outside the helper (e.g.
// the orchestrator's PlanController resolver) can reach it without
// going through ActivePlanContext when they already hold a *Tab
// reference. dbsavvy-uv0.8.
func (t *Tab) PlanContext() *guicontext.PlanContext {
	return t.planContextSnapshot()
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

// --- /regex filter surface (dbsavvy-uv0.4) -------------------------------

// filterCaveatKey tags the once-per-tab "filtering loaded rows only"
// toast so ShowOrUpdate replaces in place instead of stacking.
const filterCaveatKey = "result.filter.caveat"

// filterCaveatTTL is the visibility window for the caveat toast.
const filterCaveatTTL = 5 * time.Second

// filterCaveatMessage is the once-per-tab caveat surfaced when /regex
// is applied to an incomplete tab.
const filterCaveatMessage = "filtering loaded rows only — press G to load all then re-filter"

// FilterPrompt opens the /regex prompt against the active tab. On
// submit the regex is applied to the tab's grid; on incomplete tabs
// the once-per-tab caveat toast fires. No-op when no tab is active or
// the prompt helper is unwired. dbsavvy-uv0.4.
func (h *ResultTabsHelper) FilterPrompt() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	if h.deps.Prompt == nil {
		return
	}
	_ = h.deps.Prompt.Prompt("/", "", func(value string) error {
		if value == "" {
			// Empty regex is treated as cancel per AC.
			return nil
		}
		if err := g.SetFilter(value, false); err != nil {
			h.toast(fmt.Sprintf("filter error: %v", err))
			return nil
		}
		// Filter successfully applied: fire the once-per-tab caveat when
		// the underlying buffer is still streaming.
		if !t.Complete() && !t.CaveatShown() {
			h.showFilterCaveat()
			t.SetCaveatShown(true)
		}
		return nil
	}, func() error { return nil })
}

// showFilterCaveat surfaces the filter caveat toast, preferring
// ShowOrUpdate when the toast surface supports it so re-fires replace
// in place instead of stacking. dbsavvy-uv0.4.
func (h *ResultTabsHelper) showFilterCaveat() {
	if h.deps.Toast == nil {
		return
	}
	if upd, ok := h.deps.Toast.(toastUpdater); ok {
		upd.ShowOrUpdate(filterCaveatKey, filterCaveatMessage, filterCaveatTTL)
		return
	}
	h.deps.Toast.Show(filterCaveatMessage, filterCaveatTTL)
}

// FilterToggleAllCols flips the allCols flag of the active filter on
// the active tab's grid. No-op when no tab is active or no filter is
// installed. dbsavvy-uv0.4.
func (h *ResultTabsHelper) FilterToggleAllCols() {
	g := h.activeGrid()
	if g == nil {
		return
	}
	g.ToggleFilterAllCols()
}

// FilterJumpNext advances the cursor on the active tab's grid to the
// next filter match. dbsavvy-uv0.4.
func (h *ResultTabsHelper) FilterJumpNext() {
	g := h.activeGrid()
	if g == nil {
		return
	}
	g.JumpNextMatch()
}

// FilterJumpPrev rewinds the cursor on the active tab's grid to the
// previous filter match. dbsavvy-uv0.4.
func (h *ResultTabsHelper) FilterJumpPrev() {
	g := h.activeGrid()
	if g == nil {
		return
	}
	g.JumpPrevMatch()
}

// FilterClear drops the active filter on the active tab's grid.
// dbsavvy-uv0.4.
func (h *ResultTabsHelper) FilterClear() {
	g := h.activeGrid()
	if g == nil {
		return
	}
	g.ClearFilter()
}

// FilterActive reports whether the active tab's grid has an active
// filter. Used by the shared <esc> chord to avoid shadowing other esc
// handlers when no filter is installed. dbsavvy-uv0.4.
func (h *ResultTabsHelper) FilterActive() bool {
	g := h.activeGrid()
	if g == nil {
		return false
	}
	return g.FilterActive()
}

// activeGrid returns the *grid.View attached to the currently-active
// tab, or nil when no tab is active / the active tab is a plan/error
// tab. dbsavvy-uv0.4.
func (h *ResultTabsHelper) activeGrid() *grid.View {
	t := h.Active()
	if t == nil {
		return nil
	}
	return t.Grid()
}

// --- <leader>s sort picker (dbsavvy-uv0.5) -------------------------------

// SortPick opens the column picker against the active tab. On submit
// SetSort(idx) fires on the tab's grid, cycling asc → desc → clear per
// the AC. No-op when no tab is active, no grid is attached, the choice
// dep is unwired, or the buffer has no columns yet. dbsavvy-uv0.5.
func (h *ResultTabsHelper) SortPick() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	if h.deps.Choice == nil {
		return
	}
	cols := h.gridColumnNames(g)
	if len(cols) == 0 {
		return
	}
	_ = h.deps.Choice.Choose(h.sortPickLabel, cols, func(idx int) error {
		g.SetSort(idx)
		return nil
	}, func() error { return nil })
}

// --- <leader>gH hide-cols overlay (dbsavvy-uv0.6) ------------------------

// activeHideOverlay holds the currently-open hide overlay (if any). nil
// when no overlay is active. Accessed under h.mu. dbsavvy-uv0.6.
type activeHideOverlay struct {
	tab *Tab
	ov  *popup.HideOverlay
}

// HideOverlay opens the <leader>gH hide-cols overlay against the active
// tab. Seeds the overlay's hidden set from the tab's current
// grid.HiddenCols() (which itself was seeded from AppState on identity
// attach when HasRowIdentity). Persistence on close is gated by the
// tab's recorded ResultIdentity.HasRowIdentity flag — when false, the
// overlay runs session-only and the footer notes it. dbsavvy-uv0.6.
func (h *ResultTabsHelper) HideOverlay() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	names := h.gridColumnNames(g)
	if len(names) == 0 {
		return
	}
	hidden := g.HiddenCols()
	connID, ri := t.Identity()
	persistEnabled := ri.HasRowIdentity && connID != "" && ri.BaseTable != "" && h.deps.Store != nil
	ov := popup.NewHideOverlay(names, hidden, persistEnabled)

	h.mu.Lock()
	h.hideOverlay = &activeHideOverlay{tab: t, ov: ov}
	h.mu.Unlock()
	if h.deps.PushHideOverlay != nil {
		_ = h.deps.PushHideOverlay()
	}
}

// HideOverlayBody returns the current overlay body for rendering, or
// the empty string when no overlay is active. Mirrors the Active+Body
// shape the context's HideOverlayState interface requires. dbsavvy-uv0.6.
func (h *ResultTabsHelper) HideOverlayBody() string {
	h.mu.Lock()
	ov := h.hideOverlay
	h.mu.Unlock()
	if ov == nil || ov.ov == nil {
		return ""
	}
	return ov.ov.Body()
}

// HideOverlayActive reports whether the <leader>gH hide overlay is
// currently waiting for input.
func (h *ResultTabsHelper) HideOverlayActive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hideOverlay != nil && h.hideOverlay.ov != nil
}

// HideOverlayState returns the overlay state for rendering. nil when no
// overlay is active. Test + render accessor.
func (h *ResultTabsHelper) HideOverlayState() *popup.HideOverlay {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hideOverlay == nil {
		return nil
	}
	return h.hideOverlay.ov
}

// HideOverlayMove advances the overlay's cursor by d (+1 / -1). No-op
// when no overlay is active.
func (h *ResultTabsHelper) HideOverlayMove(d int) {
	h.mu.Lock()
	ov := h.hideOverlay
	h.mu.Unlock()
	if ov == nil || ov.ov == nil {
		return
	}
	ov.ov.MoveCursor(d)
}

// HideOverlayToggle flips the visibility of the column under the
// overlay's cursor. Rejects the toggle (with a toast) when it would
// leave zero visible columns. No-op when no overlay is active.
func (h *ResultTabsHelper) HideOverlayToggle() {
	h.mu.Lock()
	ov := h.hideOverlay
	h.mu.Unlock()
	if ov == nil || ov.ov == nil {
		return
	}
	if err := ov.ov.Toggle(); err != nil {
		h.toast(err.Error())
	}
}

// HideOverlayClose applies the overlay's hidden set to the tab's grid,
// persists the column-name list when persistence is enabled, and clears
// the overlay state. <esc> handler. dbsavvy-uv0.6.
//
// Pop ordering: state is committed FIRST (SetHiddenCols + optional
// MutateAndSave) so the popped popup snaps back to a grid that already
// reflects the new hidden set. Then the focus-stack pop fires so the
// next render frame sees the new top context.
func (h *ResultTabsHelper) HideOverlayClose() {
	h.mu.Lock()
	ov := h.hideOverlay
	h.hideOverlay = nil
	h.mu.Unlock()
	// Pop the popup off the focus stack on every Close path (including
	// early returns) — callers may invoke HideOverlayClose defensively.
	if popFn := h.deps.PopHideOverlay; popFn != nil {
		defer func() { _ = popFn() }()
	}
	if ov == nil || ov.ov == nil || ov.tab == nil {
		return
	}
	g := ov.tab.Grid()
	if g == nil {
		return
	}
	hiddenSet := ov.ov.HiddenSet()
	g.SetHiddenCols(hiddenSet)
	if !ov.ov.PersistEnabled() {
		return
	}
	if h.deps.Store == nil {
		return
	}
	connID, ri := ov.tab.Identity()
	if !ri.HasRowIdentity || connID == "" || ri.BaseTable == "" {
		return
	}
	colNames := g.HiddenColumnNames()
	h.deps.Store.MutateAndSave(func(s *common.AppState) {
		if len(colNames) == 0 {
			// Empty set: prune the entry to keep the YAML clean.
			if s.HiddenColumns == nil {
				return
			}
			inner, ok := s.HiddenColumns[connID]
			if !ok {
				return
			}
			delete(inner, ri.BaseTable)
			if len(inner) == 0 {
				delete(s.HiddenColumns, connID)
			}
			return
		}
		if s.HiddenColumns == nil {
			s.HiddenColumns = make(map[string]map[string][]string)
		}
		if s.HiddenColumns[connID] == nil {
			s.HiddenColumns[connID] = make(map[string][]string)
		}
		dup := make([]string, len(colNames))
		copy(dup, colNames)
		s.HiddenColumns[connID][ri.BaseTable] = dup
	})
}

// AttachActiveTabIdentity records (connID, ri) on the currently-active
// tab and seeds its grid's hidden-col set from AppState when
// ri.HasRowIdentity. The caller (QueryEditorController) invokes this
// right after OpenResultTab so the per-(connID, baseTable) persisted
// hidden columns reapply on tab attach. No-op when no tab is active.
// dbsavvy-uv0.6.
func (h *ResultTabsHelper) AttachActiveTabIdentity(connID string, ri query.ResultIdentity) {
	t := h.Active()
	if t == nil {
		return
	}
	t.SetIdentity(connID, ri)
	h.SeedHiddenColsFromAppState(t)
}

// SeedHiddenColsFromAppState looks up the persisted hidden-col name set
// for the active tab's (connID, baseTable) pair and re-installs it on
// the tab's grid as an index set. Called by the controller right after
// the grid's SetColumns is invoked (and after SetIdentity has been
// called). No-op when persistence is disabled, the store is unwired, or
// no entry exists. dbsavvy-uv0.6.
func (h *ResultTabsHelper) SeedHiddenColsFromAppState(t *Tab) {
	if t == nil || h.deps.Store == nil {
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	connID, ri := t.Identity()
	if !ri.HasRowIdentity || connID == "" || ri.BaseTable == "" {
		return
	}
	names := h.deps.Store.HiddenColumnsSnapshot(connID, ri.BaseTable)
	if len(names) == 0 {
		return
	}
	// Translate names → indices against the CURRENT cols slice. Names
	// missing from the new query are silently dropped from runtime.
	idx := make(map[int]bool, len(names))
	nameSet := make(map[string]struct{}, len(names))
	for _, n := range names {
		nameSet[n] = struct{}{}
	}
	n := g.ColumnCount()
	for i := 0; i < n; i++ {
		if _, ok := nameSet[g.ColumnName(i)]; ok {
			idx[i] = true
		}
	}
	g.SetHiddenCols(idx)
}

// --- <leader>oe export menu (dbsavvy-uv0.9) ------------------------------

// activeExportMenu holds the currently-open export menu plus the
// in-flight export's cancel func (when one is running). At most one menu
// and one in-flight export per helper. Accessed under h.mu. dbsavvy-uv0.9.
type activeExportMenu struct {
	tab    *Tab
	menu   *popup.ExportMenu
	cancel context.CancelFunc // non-nil only while an export is running
}

// exportFormatLabels returns the menu's Format options in render order.
// SQL-INSERTs is appended only when the source tab carries row identity.
func exportFormatLabels(hasRowIdentity bool) []string {
	base := []string{"CSV", "TSV", "NDJSON", "JSON Array", "Markdown"}
	if hasRowIdentity {
		return append(base, "SQL INSERTs")
	}
	return base
}

const (
	exportScopeVisible = 0
	exportScopeLoaded  = 1
	exportScopeFull    = 2
)

const (
	defaultExportBufferedRowWarnThreshold int64 = 100_000
	defaultExportClipboardMaxBytes        int64 = 16 * 1024 * 1024
)

// PromptExport opens the <leader>oe export menu for the active tab.
// Resolves SQL-INSERTs availability from the tab's ResultIdentity.
// dbsavvy-uv0.9.
func (h *ResultTabsHelper) PromptExport() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	_, ri := t.Identity()
	formats := exportFormatLabels(ri.HasRowIdentity)

	filterActive := g.FilterActive()

	var estimated int64
	if r := t.Runner(); r != nil {
		estimated = r.EstimatedRows()
	}
	threshold := h.deps.ExportBufferedRowWarnThreshold
	if threshold <= 0 {
		threshold = defaultExportBufferedRowWarnThreshold
	}
	bufferedThresholdExceeded := estimated > threshold

	destinations := []string{"File", "Clipboard", "stdout"}
	scopes := []string{"Visible", "Loaded", "Full"}

	// dbsavvy-bwq.11 (A8): when SQL-INSERTs is in the format list, gate
	// it on the GridView's editability decision (F2's single source of
	// truth). When the grid says not editable AND provides a reason,
	// surface the row as shown-but-disabled with that reason inline.
	// Pre-Z1 defaults (editable=false, reason="") preserve current UX:
	// SQL-INSERTs stays enabled when ri.HasRowIdentity placed it in the
	// list.
	sqlInsertsIdx := -1
	sqlInsertsReason := ""
	if pos := indexOf(formats, "SQL INSERTs"); pos >= 0 {
		if !g.Editable() && g.DisabledReason() != "" {
			sqlInsertsIdx = pos
			sqlInsertsReason = g.DisabledReason()
		}
	}

	m := popup.NewExportMenu(formats, destinations, scopes, sqlInsertsIdx, bufferedThresholdExceeded, filterActive)
	m.SetBufferedFormatIndexes(indexOf(formats, "Markdown"), indexOf(formats, "JSON Array"))
	m.SetBufferedThresholdLabel(fmt.Sprintf("≥ %d rows", threshold))
	if sqlInsertsReason != "" {
		m.SetSQLInsertsDisabledReason(sqlInsertsReason)
	}

	h.mu.Lock()
	h.exportMenu = &activeExportMenu{tab: t, menu: m}
	h.mu.Unlock()

	if h.deps.PushExportMenu != nil {
		_ = h.deps.PushExportMenu()
	}
}

// indexOf returns the position of s in ss, or -1 when absent.
func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// ExportMenuBody returns the current menu body for rendering, "" when no
// menu is active. dbsavvy-uv0.9.
func (h *ResultTabsHelper) ExportMenuBody() string {
	h.mu.Lock()
	m := h.exportMenu
	h.mu.Unlock()
	if m == nil || m.menu == nil {
		return ""
	}
	return m.menu.Body()
}

// ExportMenuActive reports whether the export menu is currently waiting
// for input. dbsavvy-uv0.9.
func (h *ResultTabsHelper) ExportMenuActive() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exportMenu != nil && h.exportMenu.menu != nil
}

// ExportMenuMoveField dispatches into popup.ExportMenu.
func (h *ResultTabsHelper) ExportMenuMoveField(d int) {
	h.mu.Lock()
	m := h.exportMenu
	h.mu.Unlock()
	if m == nil || m.menu == nil {
		return
	}
	m.menu.MoveField(d)
}

// ExportMenuMoveValue dispatches into popup.ExportMenu.
func (h *ResultTabsHelper) ExportMenuMoveValue(d int) {
	h.mu.Lock()
	m := h.exportMenu
	h.mu.Unlock()
	if m == nil || m.menu == nil {
		return
	}
	m.menu.MoveValue(d)
}

// ExportMenuCancel pops the menu. If an export is in flight, also
// cancels it. dbsavvy-uv0.9.
func (h *ResultTabsHelper) ExportMenuCancel() {
	h.mu.Lock()
	m := h.exportMenu
	h.exportMenu = nil
	h.mu.Unlock()
	if m != nil && m.cancel != nil {
		m.cancel()
	}
	if popFn := h.deps.PopExportMenu; popFn != nil {
		_ = popFn()
	}
}

// ExportMenuConfirmFullScopeWithFilter sets the menu's typed-YES flag
// when the warning is showing. Bound to `y`. dbsavvy-uv0.9.
func (h *ResultTabsHelper) ExportMenuConfirmFullScopeWithFilter() {
	h.mu.Lock()
	m := h.exportMenu
	h.mu.Unlock()
	if m == nil || m.menu == nil {
		return
	}
	if !m.menu.RequiresFullWithFilterConfirmation() {
		return
	}
	m.menu.SetConfirmedFullWithFilter(true)
}

// ExportMenuConfirm kicks off the export based on the menu's current
// selection. Pops the menu first (so the user sees the toast), then
// runs the export on a worker goroutine. dbsavvy-uv0.9.
func (h *ResultTabsHelper) ExportMenuConfirm() {
	h.mu.Lock()
	m := h.exportMenu
	h.mu.Unlock()
	if m == nil || m.menu == nil {
		return
	}
	if reason := m.menu.ConfirmBlockedReason(); reason != "" {
		h.toast(reason)
		return
	}
	if m.menu.RequiresFullWithFilterConfirmation() {
		h.toast("press y to confirm Full scope ignoring filter, or move Scope")
		return
	}

	tab := m.tab
	formatLabel := m.menu.FormatLabel()
	destLabel := m.menu.DestinationLabel()
	scopeIdx := m.menu.ScopeIdx()

	format, ferr := h.buildFormat(tab, formatLabel)
	if ferr != nil {
		h.toast(ferr.Error())
		return
	}
	dest, derr := h.buildDestination(tab, destLabel, formatLabel)
	if derr != nil {
		h.toast(derr.Error())
		return
	}

	// Pop the menu off the focus stack now.
	h.mu.Lock()
	h.exportMenu = nil
	h.mu.Unlock()
	if popFn := h.deps.PopExportMenu; popFn != nil {
		_ = popFn()
	}

	if h.deps.OnWorker == nil {
		h.toast("export: no worker available")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	// Keep cancel reachable so ExportMenuCancel-on-shutdown still aborts
	// the in-flight export. menu is nil since the UI is closed.
	h.exportMenu = &activeExportMenu{tab: tab, menu: nil, cancel: cancel}
	h.mu.Unlock()

	h.deps.OnWorker(func(_ gocui.Task) error {
		src := h.buildRowSource(tab, scopeIdx)
		descriptor, err := exporter.Run(ctx, format, dest, src, h.progressFn())
		h.mu.Lock()
		h.exportMenu = nil
		h.mu.Unlock()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				h.toast("export cancelled")
			} else {
				h.toast("export failed: " + err.Error())
			}
			return nil
		}
		h.toast("export complete: " + descriptor)
		return nil
	})
}

// progressFn returns a callback that updates a "export-progress" toast.
// Returns nil when the helper has no toast-updater wired (toast still
// fires once at completion via toast()).
func (h *ResultTabsHelper) progressFn() exporter.ProgressFn {
	if h.deps.Toast == nil {
		return nil
	}
	upd, ok := h.deps.Toast.(toastUpdater)
	if !ok {
		return nil
	}
	return func(rows int64) {
		upd.ShowOrUpdate("export.progress", fmt.Sprintf("exporting… %d rows", rows), 5*time.Second)
	}
}

// buildFormat resolves the menu's selected format label to an
// exporter.Format. Returns an error for unknown labels or when
// SQL-INSERTs is selected but no encoder is reachable.
func (h *ResultTabsHelper) buildFormat(t *Tab, label string) (exporter.Format, error) {
	switch label {
	case "CSV":
		return exporter.NewCSV(), nil
	case "TSV":
		return exporter.NewTSV(), nil
	case "NDJSON":
		return exporter.NewNDJSON(), nil
	case "JSON Array":
		return exporter.NewJSONArray(), nil
	case "Markdown":
		return exporter.NewMarkdown(), nil
	case "SQL INSERTs":
		_, ri := t.Identity()
		if !ri.HasRowIdentity {
			return nil, fmt.Errorf("SQL INSERTs unavailable")
		}
		enc := h.tabEncoder(t)
		if enc == nil {
			return nil, fmt.Errorf("SQL INSERTs: no encoder")
		}
		return exporter.NewSQLInserts(ri.BaseTable, enc), nil
	}
	return nil, fmt.Errorf("unknown format: %s", label)
}

// buildDestination resolves the menu's selected destination label to an
// exporter.Destination.
func (h *ResultTabsHelper) buildDestination(t *Tab, destLabel, formatLabel string) (exporter.Destination, error) {
	switch destLabel {
	case "File":
		downloadDir := env.GetDownloadDir()
		connID, ri := t.Identity()
		base := ri.BaseTable
		if base == "" {
			base = "result"
		}
		ext := extFor(formatLabel)
		filename := exporter.DefaultFilename(connID, base, ext, h.now())
		return exporter.NewFileDest(downloadDir, filename), nil
	case "Clipboard":
		maxBytes := h.deps.ExportClipboardMaxBytes
		if maxBytes <= 0 {
			maxBytes = defaultExportClipboardMaxBytes
		}
		// ClipboardWriter is intentionally nil for v1 — clipboard payloads
		// are buffered and discarded on Close. A full wiring lands in a
		// follow-up that surfaces the grid clipboard adapter through Deps.
		return exporter.NewClipboardDest(nil, maxBytes), nil
	case "stdout":
		return exporter.NewStdoutDest(), nil
	}
	return nil, fmt.Errorf("unknown destination: %s", destLabel)
}

// extFor maps the menu's format label to a filesystem extension.
func extFor(formatLabel string) string {
	switch formatLabel {
	case "CSV":
		return "csv"
	case "TSV":
		return "tsv"
	case "NDJSON":
		return "ndjson"
	case "JSON Array":
		return "json"
	case "Markdown":
		return "md"
	case "SQL INSERTs":
		return "sql"
	}
	return "txt"
}

// buildRowSource builds a RowSource for the given scope. Visible scope
// snapshots grid.VisibleRows; Loaded snapshots grid.AllRows; Full
// triggers ReadToEnd and blocks the worker goroutine until it
// completes, then snapshots grid.AllRows. dbsavvy-uv0.9.
func (h *ResultTabsHelper) buildRowSource(t *Tab, scopeIdx int) exporter.RowSource {
	g := t.Grid()
	if g == nil {
		return &staticRowSource{}
	}
	cols := g.Columns()
	switch scopeIdx {
	case exportScopeVisible:
		return &staticRowSource{cols: cols, rows: g.VisibleRows()}
	case exportScopeLoaded:
		return &staticRowSource{cols: cols, rows: g.AllRows()}
	case exportScopeFull:
		done := make(chan struct{})
		if r := t.Runner(); r != nil {
			r.ReadToEnd(func() { close(done) })
		} else {
			close(done)
		}
		<-done
		return &staticRowSource{cols: cols, rows: g.AllRows()}
	}
	return &staticRowSource{cols: cols}
}

// staticRowSource is a snapshot-backed RowSource for the export
// pipeline. Iterate walks the captured slice in order.
type staticRowSource struct {
	cols []models.ColumnMeta
	rows []models.Row
}

func (s *staticRowSource) Cols() []models.ColumnMeta { return s.cols }
func (s *staticRowSource) Iterate(fn func(models.Row) error) error {
	for _, r := range s.rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// tabEncoder returns the driver Encoder for the tab's session, or nil
// when none is reachable. SQL INSERTs surfaces a "no encoder" error in
// that case. v1 returns nil unless the StreamRunner exposes Encoder()
// via interface assertion; the full Encoder wiring lands as a follow-up.
func (h *ResultTabsHelper) tabEncoder(t *Tab) drivers.Encoder {
	if t == nil {
		return nil
	}
	r := t.Runner()
	if r == nil {
		return nil
	}
	if er, ok := any(r).(interface{ Encoder() drivers.Encoder }); ok {
		return er.Encoder()
	}
	return nil
}

// --- Expanded view mode + result-grid motion (dbsavvy-uv0.7) -------------

// ToggleViewMode flips the active tab's grid between ViewModeGrid and
// ViewModeExpanded and persists the new value globally via
// AppState.LastResultViewMode. No-op when no tab is active or the
// active tab has no grid (plan / error tabs). dbsavvy-uv0.7.
func (h *ResultTabsHelper) ToggleViewMode() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	next := grid.ViewModeExpanded
	if g.ViewMode() == grid.ViewModeExpanded {
		next = grid.ViewModeGrid
	}
	g.SetViewMode(next)
	if h.deps.Store != nil {
		h.deps.Store.SetLastResultViewMode(next)
	}
}

// JumpLastOrReadToEnd dispatches the G chord: expanded mode -> jump to
// the last loaded record (no drain); grid mode -> ReadToEnd with the
// existing >1M warn. dbsavvy-uv0.7 (AD-14).
func (h *ResultTabsHelper) JumpLastOrReadToEnd() {
	t := h.Active()
	if t == nil {
		h.toast("no result tabs")
		return
	}
	g := t.Grid()
	if g != nil && g.ViewMode() == grid.ViewModeExpanded {
		g.JumpLast()
		return
	}
	h.ReadToEnd()
}

// CursorDown / CursorUp / CursorLeft / CursorRight / JumpFirst /
// HalfPageDown / HalfPageUp / WrappedLineDown / WrappedLineUp /
// SelectRow / SelectBlock delegate to the active grid. No-op when no
// tab is active or the active tab has no grid. dbsavvy-uv0.7.
func (h *ResultTabsHelper) CursorDown() { h.withActiveGrid(func(g *grid.View) { g.MoveCursorDown() }) }
func (h *ResultTabsHelper) CursorUp()   { h.withActiveGrid(func(g *grid.View) { g.MoveCursorUp() }) }
func (h *ResultTabsHelper) CursorLeft() { h.withActiveGrid(func(g *grid.View) { g.HorizScrollLeft() }) }
func (h *ResultTabsHelper) CursorRight() {
	h.withActiveGrid(func(g *grid.View) { g.HorizScrollRight() })
}
func (h *ResultTabsHelper) JumpFirst()    { h.withActiveGrid(func(g *grid.View) { g.JumpFirst() }) }
func (h *ResultTabsHelper) HalfPageDown() { h.withActiveGrid(func(g *grid.View) { g.HalfPageDown() }) }
func (h *ResultTabsHelper) HalfPageUp()   { h.withActiveGrid(func(g *grid.View) { g.HalfPageUp() }) }
func (h *ResultTabsHelper) WrappedLineDown() {
	h.withActiveGrid(func(g *grid.View) { g.WrappedLineDown() })
}

func (h *ResultTabsHelper) WrappedLineUp() {
	h.withActiveGrid(func(g *grid.View) { g.WrappedLineUp() })
}
func (h *ResultTabsHelper) SelectRow()   { h.withActiveGrid(func(g *grid.View) { g.EnterRowMode() }) }
func (h *ResultTabsHelper) SelectBlock() { h.withActiveGrid(func(g *grid.View) { g.EnterBlockMode() }) }

// withActiveGrid resolves the active tab's grid and invokes fn. No-op
// when no tab is active or the active tab has no grid. dbsavvy-uv0.7.
func (h *ResultTabsHelper) withActiveGrid(fn func(*grid.View)) {
	t := h.Active()
	if t == nil {
		return
	}
	g := t.Grid()
	if g == nil {
		return
	}
	fn(g)
}

// gridColumnNames snapshots the column-name list off the active grid.
// Used to build the SortPick overlay. Returns an empty slice when the
// grid has no columns installed yet.
func (h *ResultTabsHelper) gridColumnNames(g *grid.View) []string {
	if g == nil {
		return nil
	}
	n := g.ColumnCount()
	if n == 0 {
		return nil
	}
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		names = append(names, g.ColumnName(i))
	}
	return names
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
