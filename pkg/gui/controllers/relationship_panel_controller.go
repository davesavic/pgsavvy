package controllers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// relationshipPanelDebounce is the settle window after a row-change motion
// before the panel repaints. Multiple motions inside this window collapse
// to a single repaint of the final row (epoch-gated). Constant for now
// (not user-configurable — frozen decision).
const relationshipPanelDebounce = 200 * time.Millisecond

// relationshipPanelMaxRels caps how many relationships the body lists
// before collapsing the remainder into a "(+N more)" line.
const relationshipPanelMaxRels = 12

// relationshipPanelTree is the narrow focus-stack surface the controller
// uses. *gui.ContextTree satisfies it. Push installs the DISPLAY_CONTEXT;
// PopIfTop closes it; Stack() lets Toggle detect current membership.
type relationshipPanelTree interface {
	Push(c types.IBaseContext) error
	PopIfTop(key types.ContextKey) error
	Stack() []types.IBaseContext
}

// breadcrumbJumpList is the narrow READ-ONLY surface the breadcrumb projects
// from: Snapshot exposes the existing jump entries (oldest→newest) + the
// current cursor (-1 = at most-recent). *ui.ResultJumpList satisfies it. No
// new trail structure — the breadcrumb is derived purely from this + tab
// labels.
type breadcrumbJumpList interface {
	Snapshot() (entries []ui.JumpEntry, cursor int)
}

// breadcrumbTabLabels is the narrow READ-ONLY surface the breadcrumb uses to
// name a jump entry's tab. TabLabelByID returns the tab's label and whether it
// is still open (a closed tab is skipped — no dangling segment).
// *ui.ResultTabsHelper satisfies it.
type breadcrumbTabLabels interface {
	TabLabelByID(tabID string) (string, bool)
}

// relationshipFKLookup resolves foreign keys for (schema, table). The
// orchestrator wires the forward + reverse variants through the active
// SQLSession's FKCache (Get / GetReverse) — NO per-row data queries.
type relationshipFKLookup func(ctx context.Context, schema, table string) ([]models.ForeignKey, error)

// relationshipPreviewLookup resolves the parent row's display-column value
// for one outbound FK. refValues are the focused row's FK cell values,
// paired positionally with fk.RefColumns. The orchestrator wires a closure
// that acquires a FRESH POOLED session and calls pg.ResolveDisplayValue with
// a short statement timeout, so the panel never contends with the user's
// query stream. A nil scalar means "parent row not found"; an error means the
// caller keeps the raw fallback line.
type relationshipPreviewLookup func(ctx context.Context, fk models.ForeignKey, refValues []any) (any, error)

// relationshipEstimateLookup resolves the planner's row estimate for one
// INBOUND FK against the focused parent row. refValues are the parent row's
// referenced-column cell values, paired positionally with fk.RefColumns. The
// orchestrator wires a closure that acquires a FRESH POOLED session and calls
// pg.EstimatePredicatedRows with a short statement timeout (planner-only, no
// execute), so the panel never contends with the user's query stream. An error
// marks that single line degraded; the rest of the panel survives.
type relationshipEstimateLookup func(ctx context.Context, fk models.ForeignKey, refValues []any) (int64, error)

// relationshipExactLookup resolves the EXACT inbound child count (COUNT(*)) for
// one INBOUND FK against the focused parent row, on demand when that inbound
// line is focused. refValues are the parent row's referenced-column cell values,
// paired positionally with fk.RefColumns. The orchestrator wires a closure that
// acquires a FRESH POOLED session and calls pg.CountPredicatedRows with a 750ms
// statement timeout. On timeout the caller keeps the ~estimate; on any other
// error it marks that single line with the exact-count error marker.
type relationshipExactLookup func(ctx context.Context, fk models.ForeignKey, refValues []any) (int64, error)

// outboundRel is the resolved per-row model for a single outbound FK line.
// It carries everything render + selection + Enter need: the FK metadata, the
// raw "col=val" body, the predicate values, the grid column index used to
// retarget FKForwardHelper.Jump, and a NULL marker. previewKey indexes the
// controller's preview cache.
type outboundRel struct {
	fk         models.ForeignKey
	rawPairs   string
	refValues  []any
	gridCol    int
	isNull     bool
	previewKey string
}

// inboundRel is the resolved per-row model for a single inbound (child) FK
// line. fk is an inbound constraint from FKCache.GetReverse: fk.Table is the
// referencing CHILD table, fk.Columns its referencing columns, fk.RefColumns
// the parent's referenced columns (paired positionally with refValues, the
// focused parent row's key values). pkValues binds the reverse-open SELECT
// ($1..$N against fk.RefColumns). estimateKey indexes the estimate cache; a
// composite/PK-less row that can't be bound yields an empty key + empty
// pkValues, so the fill worker refuses it.
type inboundRel struct {
	fk          models.ForeignKey
	pkValues    []any
	estimateKey string
}

// RelationshipPanelController owns the <leader>gr relationship panel:
//
//   - RelationshipPanelToggle (<leader>gr, RESULT_GRID) opens / closes the
//     right-docked DISPLAY_CONTEXT sidebar. The grid keeps input focus.
//   - RelationshipPanelEnter (<cr>, RELATIONSHIP_PANEL) — T1 stub: the
//     focusable lookup popup lands in a later task; registered so the
//     orphan-action invariant passes.
//   - RelationshipPanelExit (<esc>, RELATIONSHIP_PANEL) — T1 stub
//     counterpart of Enter.
//
// Live-follow: NotifyCursorChange is invoked from the grid (under the grid
// mutex) on every row-change motion; it only bumps an epoch + arms a
// debounce timer. On settle the timer schedules a repaint via
// OnUIThreadContentOnly, guarded by the epoch + the active tab/row so a
// superseded or stale repaint is dropped.
type RelationshipPanelController struct {
	baseController

	ctx  *guicontext.RelationshipPanelContext
	tree relationshipPanelTree
	mgr  ResultTabsManager

	forwardFK relationshipFKLookup
	reverseFK relationshipFKLookup
	preview   relationshipPreviewLookup
	estimate  relationshipEstimateLookup
	exact     relationshipExactLookup

	fkForward *helpers.FKForwardHelper

	// runner / tabs / jumps drive the Enter -> reverse-open path for a focused
	// inbound relationship. Reuse the same surfaces the gD reverse picker uses
	// (RunQuery -> RunHandle, OpenResultTab, Push). Nil leaves inbound Enter a
	// no-op; the existing gD picker path is untouched.
	runner FKReverseQueryRunner
	tabs   FKReverseTabsManager
	jumps  FKReverseJumpList

	// breadcrumbJumps / breadcrumbLabels drive the READ-ONLY exploration
	// breadcrumb: a projection of the existing jump list + open-tab labels (no
	// parallel trail). Both nil leaves the breadcrumb line out entirely. Wired
	// via SetBreadcrumb to keep the constructor signature stable.
	breadcrumbJumps  breadcrumbJumpList
	breadcrumbLabels breadcrumbTabLabels

	onUIContentOnly func(fn func() error)
	onWorker        func(fn func())

	mu    sync.Mutex
	timer *time.Timer
	epoch atomic.Uint64

	// focused reports whether the user has entered the panel (Enter). While
	// focused the panel owns input (j/k move the selection, Enter jumps, Esc
	// leaves); the orchestrator's Tier-4 focus guard reads this to decide
	// whether to retain grid focus or hand it to the panel view.
	focused atomic.Bool

	// state is the resolved render state for the current row: the outbound +
	// inbound relationships, the in-panel selection cursor, and the preview /
	// estimate caches. Guarded by mu (touched from the UI thread and the fill
	// worker). The selection cursor (selIdx) spans the OUTBOUND list followed
	// by the INBOUND list: [0, len(outbound)) selects an outbound line,
	// [len(outbound), len(outbound)+len(inbound)) an inbound line.
	outbound         []outboundRel
	outboundOverflow int
	inbound          []inboundRel
	inboundOverflow  int
	selIdx           int
	previews         map[string]string
	// estimates caches resolved inbound estimates per estimateKey. A present
	// entry with degraded=true marks a failed/denied estimate (muted "~?"
	// marker); degraded entries are still cached so a revisit issues no query.
	estimates map[string]estimateResult
	// inboundDegraded is true when the current row has no row identity
	// (join/view/PK-less): the inbound section renders the "needs a primary
	// key" note and ZERO inbound queries are issued.
	inboundDegraded bool
}

// estimateResult is a cached inbound count for one (row-identity, relationship).
// It carries BOTH the planner ~estimate and, once a focused line has been
// counted on demand, the EXACT figure — extending the estimate seam rather than
// adding a parallel map (T3 review guidance).
//
//   - rows / degraded: the planner estimate (degraded marks a failed estimate
//     lookup -> muted "~?" marker).
//   - exact / hasExact: the on-demand EXACT count. hasExact wins the render: an
//     inbound line with hasExact shows "<- orders 1187" instead of "~estimate".
//   - exactErr: a hard exact-count failure (permission denied / driver error,
//     NOT a timeout). It renders the exact-error marker. A TIMEOUT does NOT set
//     this — it leaves hasExact false so the line keeps the ~estimate.
type estimateResult struct {
	rows     int64
	degraded bool
	exact    int64
	hasExact bool
	exactErr bool
}

// NewRelationshipPanelController constructs the controller. Any dependency
// may be nil in unit tests; every handler nil-checks before dispatching.
func NewRelationshipPanelController(
	c *common.Common,
	core CoreDeps,
	ctx *guicontext.RelationshipPanelContext,
	tree relationshipPanelTree,
	mgr ResultTabsManager,
	forwardFK relationshipFKLookup,
	reverseFK relationshipFKLookup,
	onUIContentOnly func(fn func() error),
) *RelationshipPanelController {
	return &RelationshipPanelController{
		baseController:  newBase(c, HelperBag{CoreDeps: core}),
		ctx:             ctx,
		tree:            tree,
		mgr:             mgr,
		forwardFK:       forwardFK,
		reverseFK:       reverseFK,
		onUIContentOnly: onUIContentOnly,
		previews:        map[string]string{},
		estimates:       map[string]estimateResult{},
	}
}

// SetPreviewResolver wires the outbound display-value resolver. The
// orchestrator injects a closure that acquires a FRESH POOLED session and
// calls pg.ResolveDisplayValue under a short statement timeout. Nil leaves the
// panel on raw "<col>=<val>" fallback lines (no fill). Wired post-construction
// so the existing constructor signature (and its unit-test call sites) stay
// stable.
func (r *RelationshipPanelController) SetPreviewResolver(fn relationshipPreviewLookup) {
	r.preview = fn
}

// SetEstimateResolver wires the inbound predicated-estimate resolver. The
// orchestrator injects a closure that acquires a FRESH POOLED session and calls
// pg.EstimatePredicatedRows under a short statement timeout. Nil leaves inbound
// lines without estimates (table names only). Wired post-construction so the
// constructor signature (and its unit-test call sites) stay stable.
func (r *RelationshipPanelController) SetEstimateResolver(fn relationshipEstimateLookup) {
	r.estimate = fn
}

// SetExactResolver wires the inbound exact-count resolver invoked on demand when
// an inbound line is FOCUSED. The orchestrator injects a closure that acquires a
// FRESH POOLED session and calls pg.CountPredicatedRows under a 750ms statement
// timeout. Nil leaves inbound lines on the ~estimate (no exact counts). Wired
// post-construction so the constructor signature (and its unit-test call sites)
// stay stable.
func (r *RelationshipPanelController) SetExactResolver(fn relationshipExactLookup) {
	r.exact = fn
}

// SetFKForward wires the gd forward-FK helper reused by the panel's Enter ->
// Jump path. Nil leaves Enter as a no-op.
func (r *RelationshipPanelController) SetFKForward(h *helpers.FKForwardHelper) {
	r.fkForward = h
}

// SetReverseOpen wires the Enter -> reverse-open collaborators for a focused
// inbound relationship: runner (RunQuery -> RunHandle), tabs (OpenResultTab),
// jumps (Push a JumpEntry before opening). Reuses the same surfaces the gD
// reverse picker uses. Any nil leaves inbound Enter a no-op. Wired
// post-construction to keep the constructor signature stable.
func (r *RelationshipPanelController) SetReverseOpen(runner FKReverseQueryRunner, tabs FKReverseTabsManager, jumps FKReverseJumpList) {
	r.runner = runner
	r.tabs = tabs
	r.jumps = jumps
}

// SetBreadcrumb wires the READ-ONLY breadcrumb projection sources: jumps
// (jump-list Snapshot — entries + cursor) and labels (tab label-by-ID lookup).
// The breadcrumb renders the walked path as a header line, marking the current
// jump position so <c-o>/<c-i> navigation stays visibly consistent. Either nil
// drops the breadcrumb line. Reuses the same surfaces SetReverseOpen wires
// (*ui.ResultJumpList / *ui.ResultTabsHelper); no new jump-recording or tab-
// labeling behavior is introduced. Wired post-construction to keep the
// constructor signature stable.
func (r *RelationshipPanelController) SetBreadcrumb(jumps breadcrumbJumpList, labels breadcrumbTabLabels) {
	r.breadcrumbJumps = jumps
	r.breadcrumbLabels = labels
}

// SetOnWorker wires the background-goroutine scheduler used by the sequential
// preview fill. Nil resolves previews synchronously on the caller goroutine
// (unit-test wiring).
func (r *RelationshipPanelController) SetOnWorker(fn func(fn func())) {
	r.onWorker = fn
}

// ClearCaches drops the per-row preview + estimate caches. Called on panel
// close (toggle off) and wired by the orchestrator to disconnect so the
// caches — keyed by (row-identity, relationship) — do not grow unbounded
// across the lifetime of a long session or leak stale values across a
// reconnect. Safe to call concurrently with the fill workers (mu-guarded);
// a fill in flight simply re-resolves the values it was about to cache.
func (r *RelationshipPanelController) ClearCaches() {
	r.mu.Lock()
	r.previews = map[string]string{}
	r.estimates = map[string]estimateResult{}
	r.mu.Unlock()
}

// GetKeybindings publishes the three relationship-panel bindings.
func (r *RelationshipPanelController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := r.tr()
	type bspec struct {
		shorthand   string
		actionID    string
		description string
		scope       types.ContextKey
	}
	specs := []bspec{
		{"<leader>gr", commands.RelationshipPanelToggle, tr.Actions.RelationshipPanelToggle, types.RESULT_GRID},
		// <cr> from the grid (follow mode) enters the panel (focus grab); <cr>
		// in the panel (focused) jumps the selected relationship. Both route
		// through the same handler, branched on IsFocused().
		{"<cr>", commands.RelationshipPanelEnter, tr.Actions.RelationshipPanelEnter, types.RESULT_GRID},
		{"<cr>", commands.RelationshipPanelEnter, tr.Actions.RelationshipPanelEnter, types.RELATIONSHIP_PANEL},
		{"<esc>", commands.RelationshipPanelExit, tr.Actions.RelationshipPanelExit, types.RELATIONSHIP_PANEL},
		{"j", commands.RelationshipPanelDown, tr.Actions.RelationshipPanelDown, types.RELATIONSHIP_PANEL},
		{"k", commands.RelationshipPanelUp, tr.Actions.RelationshipPanelUp, types.RELATIONSHIP_PANEL},
	}
	out := make([]*types.ChordBinding, 0, len(specs))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       s.scope,
			ActionID:    s.actionID,
			Description: s.description,
		})
	}
	return out
}

// RegisterActions registers the three handlers.
func (r *RelationshipPanelController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	tr := r.tr()
	_ = reg.Register(&commands.Command{
		ID:          commands.RelationshipPanelToggle,
		Description: tr.Actions.RelationshipPanelToggle,
		Tag:         "Result",
		Handler:     func(_ commands.ExecCtx) error { return r.toggle() },
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.RelationshipPanelEnter,
		Description: tr.Actions.RelationshipPanelEnter,
		Tag:         "Result",
		Handler:     func(_ commands.ExecCtx) error { return r.enterOrJump() },
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.RelationshipPanelExit,
		Description: tr.Actions.RelationshipPanelExit,
		Tag:         "Result",
		Handler:     func(_ commands.ExecCtx) error { return r.exit() },
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.RelationshipPanelDown,
		Description: tr.Actions.RelationshipPanelDown,
		Tag:         "Result",
		Handler:     func(_ commands.ExecCtx) error { return r.moveSelection(1) },
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.RelationshipPanelUp,
		Description: tr.Actions.RelationshipPanelUp,
		Tag:         "Result",
		Handler:     func(_ commands.ExecCtx) error { return r.moveSelection(-1) },
	})
}

// AttachToContext subscribes GetKeybindings to the panel context so the
// RELATIONSHIP_PANEL-scoped bindings reach the trie.
func (r *RelationshipPanelController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(r.GetKeybindings)
}

// currentEpoch returns the live motion epoch. Used by tests to assert that
// every row-change motion bumps it (debounce + supersede semantics).
func (r *RelationshipPanelController) currentEpoch() uint64 { return r.epoch.Load() }

// IsOpen reports whether the panel DISPLAY_CONTEXT is currently on the
// focus stack.
func (r *RelationshipPanelController) IsOpen() bool {
	if r.tree == nil {
		return false
	}
	for _, c := range r.tree.Stack() {
		if c != nil && c.GetKey() == types.RELATIONSHIP_PANEL {
			return true
		}
	}
	return false
}

// toggle opens the panel (push + immediate repaint) when closed, or closes
// it (pop) when open. Opening with no active result tab is a no-op.
func (r *RelationshipPanelController) toggle() error {
	if r.tree == nil || r.ctx == nil {
		return nil
	}
	if r.IsOpen() {
		r.focused.Store(false) // closing always drops input mode
		r.ClearCaches()        // bound the per-row caches across open/close cycles
		return r.tree.PopIfTop(types.RELATIONSHIP_PANEL)
	}
	if r.mgr == nil || r.mgr.Active() == nil {
		// No active result — opening would render an empty panel with no
		// referent. No-op, no panic (AC).
		return nil
	}
	// Seed the body for the current row before the first paint so the panel
	// opens already populated, then kick off the preview fill.
	r.ctx.SetBody(r.renderBody())
	if err := r.tree.Push(r.ctx); err != nil {
		return err
	}
	r.startPreviewFill(r.epoch.Load())
	r.startEstimateFill(r.epoch.Load())
	return nil
}

// enterOrJump is the dual-purpose <cr> handler. From follow mode (grid
// focused, panel open) it grabs panel focus; once focused it jumps the
// selected relationship. The single key keeps the gesture intuitive: Enter
// drills in, Enter again jumps.
func (r *RelationshipPanelController) enterOrJump() error {
	if r.focused.Load() {
		return r.jump()
	}
	return r.enter()
}

// IsFocused reports whether the user has entered the panel (Enter). The
// orchestrator's Tier-4 focus guard reads this to decide whether to keep the
// grid focused (follow mode) or point gocui at the panel view (input mode).
func (r *RelationshipPanelController) IsFocused() bool { return r.focused.Load() }

// enter switches the panel into input mode: the panel view becomes the gocui
// current view (driven by the Tier-4 guard reading focused), j/k move the
// in-panel selection, Enter jumps the selected relationship, Esc leaves. The
// selection seeds at the first outbound relationship. A no-op when the panel
// is not open.
func (r *RelationshipPanelController) enter() error {
	if !r.IsOpen() {
		return nil
	}
	r.focused.Store(true)
	r.mu.Lock()
	if r.selIdx < 0 || r.selIdx >= len(r.outbound) {
		r.selIdx = 0
	}
	r.mu.Unlock()
	r.maybeStartExactFill(r.epoch.Load())
	return r.repaintNow()
}

// exit leaves input mode (focused=false) and returns input to the grid with
// the panel still open (follow mode). The selection highlight is cleared on
// the next repaint.
func (r *RelationshipPanelController) exit() error {
	if !r.focused.Load() {
		return nil
	}
	r.focused.Store(false)
	return r.repaintNow()
}

// moveSelection shifts the in-panel selection cursor by delta over the
// combined outbound+inbound relationships, clamped to [0, total). No-op unless
// the panel is focused.
func (r *RelationshipPanelController) moveSelection(delta int) error {
	if !r.focused.Load() {
		return nil
	}
	r.mu.Lock()
	n := len(r.outbound) + len(r.inbound)
	if n == 0 {
		r.mu.Unlock()
		return nil
	}
	r.selIdx += delta
	if r.selIdx < 0 {
		r.selIdx = 0
	}
	if r.selIdx >= n {
		r.selIdx = n - 1
	}
	r.mu.Unlock()
	r.maybeStartExactFill(r.epoch.Load())
	return r.repaintNow()
}

// jump acts on the selected relationship. An outbound selection fires
// FKForwardHelper.Jump (gd machinery); an inbound selection opens the child
// rows in a new tab via the reverse-open path. A NULL / unbound relationship is
// not actionable (no-op).
func (r *RelationshipPanelController) jump() error {
	if !r.focused.Load() || r.mgr == nil {
		return nil
	}
	r.mu.Lock()
	in, isInbound := r.selectedInbound()
	rel, isOutbound := r.selectedRel()
	r.mu.Unlock()
	if isInbound {
		return r.openInbound(in)
	}
	if isOutbound {
		return r.jumpOutbound(rel)
	}
	return nil
}

// jumpOutbound fires FKForwardHelper.Jump for the selected outbound
// relationship. A NULL-FK relationship is not jumpable (no-op). Reuses the gd
// machinery by retargeting Jump at the grid column of the selected FK's first
// referencing column. When two FKs share that column, Jump degrades exactly as
// gd does (first-by-name + a toast).
func (r *RelationshipPanelController) jumpOutbound(rel outboundRel) error {
	if r.fkForward == nil || rel.isNull || rel.gridCol < 0 {
		return nil
	}
	tab := r.mgr.Active()
	if tab == nil {
		return nil
	}
	cursorRow := -1
	if g := tab.Grid(); g != nil {
		cursorRow, _ = g.CursorPosition()
	}
	if err := r.fkForward.Jump(context.Background(), &fkCurrentTabAdapter{tab: tab}, cursorRow, rel.gridCol); err != nil {
		r.logErr("relationship_panel.jump", err)
	}
	return nil
}

// openInbound opens the child rows of the selected inbound relationship in a
// new result tab. It reuses the gD reverse picker's machinery verbatim:
// buildFKReverseSQL parameterizes the SELECT, the jump entry is pushed BEFORE
// opening (so <C-o> back works even if open fails), and buildFKReverseLabel
// names the tab. An unbound relationship (no pk values) is not actionable.
func (r *RelationshipPanelController) openInbound(in inboundRel) error {
	if r.runner == nil || r.tabs == nil || r.jumps == nil {
		return nil
	}
	if len(in.pkValues) == 0 || len(in.pkValues) != len(in.fk.RefColumns) {
		return nil
	}
	tab := r.mgr.Active()
	if tab == nil {
		return nil
	}

	sql := buildFKReverseSQL(in.fk)
	q := models.Query{SQL: sql, Args: append([]any(nil), in.pkValues...)}

	cursorRow, cursorCol := -1, -1
	if g := tab.Grid(); g != nil {
		cursorRow, cursorCol = g.CursorPosition()
	}
	je := ui.JumpEntry{
		Row:     cursorRow,
		Col:     cursorCol,
		TabSlot: tab.Slot(),
		TabID:   fmt.Sprintf("%d", tab.ID()),
		At:      time.Now(),
	}
	r.jumps.Push(je)

	rh, err := r.runner.RunQuery(context.Background(), q)
	if err != nil {
		r.logErr("relationship_panel.open_inbound", err)
		return nil
	}
	label := buildFKReverseLabel(ReverseEntry{FK: in.fk, PKValues: in.pkValues})
	if openErr := r.tabs.OpenResultTab(label, rh); openErr != nil {
		r.logErr("relationship_panel.open_inbound", openErr)
		return nil
	}
	// Stamp the child tab's ResultIdentity from the reverse SQL so the panel can
	// keep exploring from it (splitBaseTable reads BaseTable). The reverse-open
	// path never attached identity, so a drilled-into child rendered
	// "(no relationships)" and the exploration dead-ended after one hop. connID
	// is the parent tab's connection (the child opens on the same session).
	connID, _ := tab.Identity()
	r.attachChildIdentity(connID, sql)
	// OpenResultTab already fired an activation repaint, but that happened BEFORE
	// the identity was attached (so it rendered the identity-less child as
	// "(no relationships)"). Repaint again now that the child carries its
	// identity so the panel immediately follows into the child.
	r.NotifyActiveTabChanged()
	return nil
}

// attachChildIdentity stamps the now-active (just-opened) child tab with the
// ResultIdentity parsed from its SQL, so the panel can derive its base table
// and continue iterative exploration. No-op when the tabs manager does not
// expose the identity-attach surface (unit-test fakes).
func (r *RelationshipPanelController) attachChildIdentity(connID, sql string) {
	attacher, ok := r.tabs.(ResultTabIdentityAttacher)
	if !ok {
		return
	}
	attacher.AttachActiveTabIdentity(connID, query.DetectFromQuery(sql))
}

// selectedRel returns the currently-selected outbound relationship. Caller
// holds mu.
func (r *RelationshipPanelController) selectedRel() (outboundRel, bool) {
	if r.selIdx < 0 || r.selIdx >= len(r.outbound) {
		return outboundRel{}, false
	}
	return r.outbound[r.selIdx], true
}

// selectedInbound returns the currently-selected inbound relationship when the
// selection cursor falls past the outbound list. Caller holds mu.
func (r *RelationshipPanelController) selectedInbound() (inboundRel, bool) {
	idx := r.selIdx - len(r.outbound)
	if idx < 0 || idx >= len(r.inbound) {
		return inboundRel{}, false
	}
	return r.inbound[idx], true
}

// repaintNow re-renders the body synchronously (when no UI hook is wired, e.g.
// unit tests) or marshals a content-only repaint onto the UI thread. Unlike
// the debounced repaint it does NOT re-resolve the row; it re-renders the
// existing state (selection moved / focus toggled).
func (r *RelationshipPanelController) repaintNow() error {
	if r.ctx == nil {
		return nil
	}
	render := func() error {
		r.ctx.SetBody(r.renderBodyFromState())
		return r.ctx.HandleRender()
	}
	if r.onUIContentOnly == nil {
		r.ctx.SetBody(r.renderBodyFromState())
		return nil
	}
	r.onUIContentOnly(render)
	return nil
}

// NotifyActiveTabChanged is the active-tab-change callback. A new tab opening
// (jump-open via OpenResultTab) or an active-tab switch (gt/gT cycle,
// <leader>1..9 jump, <c-o>/<c-i> jump-list navigation) changes which result is
// the panel's referent, but — unlike a grid cursor motion — fires no
// NotifyCursorChange. Without this hook the panel keeps showing the prior tab's
// relationships + breadcrumb until the cursor is nudged. Like NotifyCursorChange
// it bumps the epoch + arms the debounce timer so the body recomputes against
// the now-active tab; a no-op when the panel is closed.
func (r *RelationshipPanelController) NotifyActiveTabChanged() {
	if !r.IsOpen() {
		return
	}
	// Bump the epoch so any armed cursor-motion repaint + in-flight fills for the
	// prior tab are superseded, then repaint immediately against the now-active
	// tab (no debounce: a tab switch is a deliberate, single event, not a motion
	// burst). The repaint re-reads the live active tab, recomputes outbound +
	// inbound, and re-renders the breadcrumb.
	epoch := r.epoch.Add(1)
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()
	r.repaint(epoch)
}

// NotifyCursorChange is the grid cursor-change callback. It runs UNDER the
// grid mutex, so it only captures + schedules: bump the epoch, then arm a
// debounce timer that fires the repaint after the settle window. Cheap and
// re-entrancy-safe. Assumes grid cursor motions are driven on the UI thread:
// the IsOpen() open-state check walks the unsynchronized ContextTree stack.
func (r *RelationshipPanelController) NotifyCursorChange(_, _ int) {
	if !r.IsOpen() {
		return
	}
	epoch := r.epoch.Add(1)
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(relationshipPanelDebounce, func() {
		r.repaint(epoch)
	})
	r.mu.Unlock()
}

// repaint runs after the debounce settle. It drops itself when a newer
// motion superseded this epoch, then marshals the body render onto the UI
// thread (content-only). The body render re-reads the live active tab, so
// a tab switch/close between arm + fire degrades gracefully (empty body,
// no stale render, no panic).
func (r *RelationshipPanelController) repaint(epoch uint64) {
	if epoch != r.epoch.Load() {
		return // superseded by a later motion
	}
	schedule := r.onUIContentOnly
	if schedule == nil {
		// No threading hook (unit-test wiring): render synchronously so
		// tests can assert the body without a UI loop.
		if r.IsOpen() && r.ctx != nil {
			r.ctx.SetBody(r.renderBody())
			r.startPreviewFill(epoch)
			r.startEstimateFill(epoch)
		}
		return
	}
	schedule(func() error {
		// Re-check on the UI thread: the panel may have closed, or a newer
		// motion may have armed after this one.
		if epoch != r.epoch.Load() || !r.IsOpen() || r.ctx == nil {
			return nil
		}
		r.ctx.SetBody(r.renderBody())
		r.startPreviewFill(epoch)
		r.startEstimateFill(epoch)
		return r.ctx.HandleRender()
	})
}

// renderBody rebuilds the per-row outbound relationship state for the active
// tab's focused row, then renders the body. Rebuilding resets the in-panel
// selection to the top (a fresh row) and is the supersede point for the
// preview fill (the caller bumps the epoch). It reads FK metadata + the row's
// own cell values only — no data queries (the preview values arrive
// asynchronously via the fill worker and are merged in renderBodyFromState).
func (r *RelationshipPanelController) renderBody() string {
	r.rebuildOutbound()
	return r.renderBodyFromState()
}

// rebuildOutbound recomputes r.outbound + r.inbound from the active tab's
// focused row and resets the selection cursor. Each outboundRel carries the FK,
// its raw "col=val" body, the predicate values, the grid column used to
// retarget Jump, a NULL marker, and the preview-cache key. Inbound rebuilding
// is gated on ResultIdentity.HasRowIdentity: without it the inbound section
// degrades (note only, zero queries). Caps both lists at
// relationshipPanelMaxRels.
func (r *RelationshipPanelController) rebuildOutbound() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outbound = nil
	r.outboundOverflow = 0
	r.inbound = nil
	r.inboundOverflow = 0
	r.inboundDegraded = false
	r.selIdx = 0

	if r.mgr == nil {
		return
	}
	tab := r.mgr.Active()
	if tab == nil {
		return
	}
	schema, table := splitBaseTable(tab)
	if table == "" {
		return
	}
	cols, values := rowSnapshot(tab)

	r.rebuildOutboundLocked(schema, table, cols, values)
	r.rebuildInboundLocked(tab, schema, table, cols, values)
}

// rebuildOutboundLocked fills r.outbound from the forward FK lookup. Caller
// holds mu.
func (r *RelationshipPanelController) rebuildOutboundLocked(schema, table string, cols []string, values []any) {
	if r.forwardFK == nil {
		return
	}
	fks, err := r.forwardFK(context.Background(), schema, table)
	if err != nil || len(fks) == 0 {
		return
	}
	for _, fk := range fks {
		if len(r.outbound) >= relationshipPanelMaxRels {
			break
		}
		r.outbound = append(r.outbound, buildOutboundRel(fk, cols, values))
	}
	if extra := len(fks) - len(r.outbound); extra > 0 {
		r.outboundOverflow = extra
	}
}

// rebuildInboundLocked fills r.inbound from the reverse FK lookup, gated on the
// active tab's row identity. Without row identity (join/view/PK-less) it sets
// inboundDegraded and issues ZERO inbound queries (not even the FK lookup).
// Caller holds mu.
func (r *RelationshipPanelController) rebuildInboundLocked(tab *ui.Tab, schema, table string, cols []string, values []any) {
	_, ri := tab.Identity()
	if !ri.HasRowIdentity {
		r.inboundDegraded = true
		return
	}
	if r.reverseFK == nil {
		return
	}
	fks, err := r.reverseFK(context.Background(), schema, table)
	if err != nil || len(fks) == 0 {
		return
	}
	for _, fk := range fks {
		if len(r.inbound) >= relationshipPanelMaxRels {
			break
		}
		r.inbound = append(r.inbound, buildInboundRel(fk, cols, values))
	}
	if extra := len(fks) - len(r.inbound); extra > 0 {
		r.inboundOverflow = extra
	}
}

// renderBody returns the full body string from the current state (outbound +
// previews + selection + inbound). Safe to call from any path; reads state
// under mu.
func (r *RelationshipPanelController) renderBodyFromState() string {
	tr := r.tr()
	if r.mgr == nil {
		return tr.RelationshipPanelNoTab
	}
	tab := r.mgr.Active()
	if tab == nil {
		return tr.RelationshipPanelNoTab
	}
	_, table := splitBaseTable(tab)
	if table == "" {
		return tr.RelationshipPanelNoRelationships
	}

	var b strings.Builder
	if crumb := r.breadcrumbLine(tab); crumb != "" {
		b.WriteString(crumb)
		b.WriteByte('\n')
	}
	b.WriteString(tr.RelationshipPanelOutboundHeader)
	b.WriteByte('\n')
	r.writeOutbound(&b)
	b.WriteByte('\n')
	b.WriteString(tr.RelationshipPanelInboundHeader)
	b.WriteByte('\n')
	r.writeInbound(&b)
	return strings.TrimRight(b.String(), "\n")
}

// breadcrumbLine renders the exploration breadcrumb: a READ-ONLY projection of
// the jump list + open-tab labels. Segments are the tabs the user jumped FROM
// (jump entries, oldest→newest) followed by the CURRENT (active) tab, joined by
// the separator, with the current jump position marked with "[…]". The current
// position is derived from the jump-list cursor: -1 (at most-recent) marks the
// active tab; cursor i marks the i-th entry's segment — consistent with
// <c-o>/<c-i> navigation, which moves that cursor.
//
// Entries whose tab has since closed are skipped (PruneByTab already removes
// pruned entries; this also tolerates a stale id with no open tab — no dangling
// segment, no panic). An empty/all-skipped jump list renders a muted empty
// state. Returns "" when the breadcrumb sources are not wired.
func (r *RelationshipPanelController) breadcrumbLine(active *ui.Tab) string {
	if r.breadcrumbJumps == nil || r.breadcrumbLabels == nil || active == nil {
		return ""
	}
	tr := r.tr()
	entries, cursor := r.breadcrumbJumps.Snapshot()

	segments := make([]string, 0, len(entries)+1)
	marked := -1 // index into segments of the current jump position
	for i, e := range entries {
		label, open := r.breadcrumbLabels.TabLabelByID(e.TabID)
		if !open {
			continue // pruned / closed tab: no dangling segment
		}
		if cursor == i {
			marked = len(segments)
		}
		segments = append(segments, label)
	}

	if len(segments) == 0 {
		return tr.RelationshipPanelBreadcrumbEmpty
	}

	// The current (active) tab is always the most-recent segment. cursor == -1
	// (at most-recent) marks it; a cursor pointing at a since-pruned entry also
	// falls through to here (marked stays -1).
	segments = append(segments, active.Label())
	if marked < 0 {
		marked = len(segments) - 1
	}

	for i := range segments {
		if i == marked {
			segments[i] = "[" + segments[i] + "]"
		}
	}
	return strings.Join(segments, tr.RelationshipPanelBreadcrumbSep)
}

// writeOutbound renders the per-row outbound lines from r.outbound. A resolved
// preview renders "-> <table>: <preview>"; an unresolved/failed line keeps the
// raw "-> <table> (<col>=<val>)" fallback; a NULL FK renders
// "-> <table>: (null)". When focused, the selected line is marked with a "> "
// gutter (the rest get "  ").
func (r *RelationshipPanelController) writeOutbound(b *strings.Builder) {
	tr := r.tr()
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.outbound) == 0 {
		b.WriteString("  " + tr.RelationshipPanelNoRelationships + "\n")
		return
	}
	focused := r.focused.Load()
	for i, rel := range r.outbound {
		gutter := "  "
		if focused && i == r.selIdx {
			gutter = "> "
		}
		b.WriteString(gutter + r.outboundLine(rel) + "\n")
	}
	if r.outboundOverflow > 0 {
		b.WriteString("  " + fmt.Sprintf(tr.RelationshipPanelMoreFmt, r.outboundOverflow) + "\n")
	}
}

// outboundLine renders the body for one relationship: preview when resolved,
// "(null)" for a NULL FK, else the raw "<table> (<col>=<val>)" fallback.
// Caller holds mu.
func (r *RelationshipPanelController) outboundLine(rel outboundRel) string {
	tr := r.tr()
	if rel.isNull {
		return "-> " + rel.fk.RefTable + ": " + tr.RelationshipPanelNull
	}
	if preview, ok := r.previews[rel.previewKey]; ok {
		return "-> " + rel.fk.RefTable + ": " + preview
	}
	return "-> " + rel.fk.RefTable + " (" + rel.rawPairs + ")"
}

// writeInbound lists the inbound child tables from r.inbound:
// "<- <table> ~<estimate>". A resolved estimate renders the humanized count; an
// unresolved one shows the pending marker; a degraded (failed/denied) one the
// muted error marker. Without row identity it renders the "needs a primary key"
// note instead (no lines, no queries). When focused, the selected line gets a
// "> " gutter. Reads state under mu.
func (r *RelationshipPanelController) writeInbound(b *strings.Builder) {
	tr := r.tr()
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.inboundDegraded {
		b.WriteString("  " + tr.RelationshipPanelInboundNeedsPK + "\n")
		return
	}
	if len(r.inbound) == 0 {
		b.WriteString("  " + tr.RelationshipPanelNoRelationships + "\n")
		return
	}
	focused := r.focused.Load()
	base := len(r.outbound)
	for i, in := range r.inbound {
		gutter := "  "
		if focused && base+i == r.selIdx {
			gutter = "> "
		}
		b.WriteString(gutter + r.inboundLine(in) + "\n")
	}
	if r.inboundOverflow > 0 {
		b.WriteString("  " + fmt.Sprintf(tr.RelationshipPanelMoreFmt, r.inboundOverflow) + "\n")
	}
}

// inboundLine renders one inbound child line. An on-demand EXACT count wins:
// "<- orders 1187". Otherwise it falls back to the planner ~estimate
// ("<- orders ~1.2k"): a resolved estimate humanizes; a degraded estimate or a
// hard exact-count error shows a muted marker; an unbound or not-yet-resolved
// line shows the pending marker. Caller holds mu.
func (r *RelationshipPanelController) inboundLine(in inboundRel) string {
	tr := r.tr()
	marker := tr.RelationshipPanelEstimatePending
	if in.estimateKey != "" {
		if res, ok := r.estimates[in.estimateKey]; ok {
			switch {
			case res.hasExact:
				marker = formatExactCount(res.exact) // exact: full integer, no "~" prefix
			case res.exactErr:
				marker = tr.RelationshipPanelExactError
			case res.degraded:
				marker = tr.RelationshipPanelEstimateError
			default:
				marker = "~" + humanizeEstimate(res.rows)
			}
		}
	}
	return "<- " + in.fk.Table + " " + marker
}

// buildOutboundRel assembles the per-row model for one outbound FK: the raw
// "col=val" body, the predicate values (paired with fk.RefColumns), the grid
// column index of the FK's first referencing column (used to retarget Jump),
// the NULL marker, and the preview-cache key. A composite FK whose referenced
// columns can't all be bound from the row keeps isNull=false but yields an
// empty refValues, so the fill worker refuses it (composite mismatch -> raw
// fallback).
func buildOutboundRel(fk models.ForeignKey, cols []string, values []any) outboundRel {
	rel := outboundRel{
		fk:       fk,
		rawPairs: fkValuePairs(fk, cols, values),
		gridCol:  -1,
	}
	// Map each referencing column to its grid value; the first referencing
	// column's grid index is the Jump retarget. A NULL in ANY FK column makes
	// the whole relationship non-jumpable + previews as "(null)".
	refValues := make([]any, 0, len(fk.Columns))
	allBound := len(fk.Columns) == len(fk.RefColumns) && len(fk.Columns) > 0
	for i, col := range fk.Columns {
		idx := indexOf(cols, col)
		if i == 0 {
			rel.gridCol = idx
		}
		if idx < 0 || idx >= len(values) {
			allBound = false
			break
		}
		v := values[idx]
		if v == nil {
			rel.isNull = true
		}
		refValues = append(refValues, v)
	}
	if allBound && !rel.isNull {
		rel.refValues = refValues
		rel.previewKey = previewCacheKey(fk, refValues)
	}
	return rel
}

// buildInboundRel assembles the per-row model for one inbound (child) FK. The
// predicate binds the parent row's REFERENCED-column values (fk.RefColumns,
// matched against the focused row's grid cells) to the child's referencing
// columns ($1..$N). A NULL in any referenced value, an unbound column, or a
// composite-length mismatch yields empty pkValues + an empty estimateKey, so
// the fill worker refuses it (no query) and Enter is a no-op for that line.
func buildInboundRel(fk models.ForeignKey, cols []string, values []any) inboundRel {
	in := inboundRel{fk: fk}
	if len(fk.RefColumns) == 0 || len(fk.RefColumns) != len(fk.Columns) {
		return in
	}
	pkValues := make([]any, 0, len(fk.RefColumns))
	for _, col := range fk.RefColumns {
		idx := indexOf(cols, col)
		if idx < 0 || idx >= len(values) || values[idx] == nil {
			return inboundRel{fk: fk}
		}
		pkValues = append(pkValues, values[idx])
	}
	in.pkValues = pkValues
	in.estimateKey = estimateCacheKey(fk, pkValues)
	return in
}

// estimateCacheKey keys a resolved inbound estimate by the child table + the
// parent-key value tuple: "schema.table|v1,v2". A revisited row hits the cache
// and issues no query. Values only flow into this in-process key (never logged).
func estimateCacheKey(fk models.ForeignKey, pkValues []any) string {
	var b strings.Builder
	b.WriteString(fk.Schema)
	b.WriteByte('.')
	b.WriteString(fk.Table)
	b.WriteByte('|')
	for i, v := range pkValues {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%v", v)
	}
	return b.String()
}

// humanizeEstimate renders an estimate count compactly: 0 -> "0", < 1000 exact,
// >= 1000 -> "1.2k", >= 1e6 -> "1.2M". Negatives clamp to "0".
func humanizeEstimate(n int64) string {
	if n < 1000 {
		if n < 0 {
			return "0"
		}
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return trimHumanized(float64(n)/1000) + "k"
	}
	return trimHumanized(float64(n)/1_000_000) + "M"
}

// formatExactCount renders an EXACT inbound count as a full integer (no "~"
// prefix, no humanized rounding): 1187 -> "1187", 0 -> "0". A negative count
// (impossible from COUNT(*), defensive) clamps to "0".
func formatExactCount(n int64) string {
	if n < 0 {
		return "0"
	}
	return strconv.FormatInt(n, 10)
}

// trimHumanized renders v to one decimal place, dropping a trailing ".0" so
// 1200 -> "1.2", 2000 -> "2".
func trimHumanized(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// previewCacheKey keys a resolved preview by the parent table + the FK value
// tuple: "refschema.reftable|v1,v2". A revisited row hits the cache and issues
// no query. Values only flow into this in-process key (never logged).
func previewCacheKey(fk models.ForeignKey, refValues []any) string {
	var b strings.Builder
	b.WriteString(fk.RefSchema)
	b.WriteByte('.')
	b.WriteString(fk.RefTable)
	b.WriteByte('|')
	for i, v := range refValues {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%v", v)
	}
	return b.String()
}

// startPreviewFill kicks off the sequential preview fill for the focused
// row's outbound relationships. It runs ONE worker; inside it resolves
// previews one at a time (cap already applied by rebuildOutbound), skipping
// cached / NULL / unbound relationships. The fill is superseded when the
// motion epoch advances (row-identity change) — checked before each resolve
// and before each repaint, never per-query-cancel mid-row so a paused row
// fills fully. Repaints are content-only + epoch/open-guarded.
func (r *RelationshipPanelController) startPreviewFill(epoch uint64) {
	if r.preview == nil {
		return
	}
	r.mu.Lock()
	// Snapshot the relationships to resolve (those with a cache key not yet
	// filled). Copy out under the lock so the worker doesn't race rebuild.
	pending := make([]outboundRel, 0, len(r.outbound))
	for _, rel := range r.outbound {
		if rel.previewKey == "" {
			continue
		}
		if _, ok := r.previews[rel.previewKey]; ok {
			continue
		}
		pending = append(pending, rel)
	}
	r.mu.Unlock()
	if len(pending) == 0 {
		return
	}

	run := func() {
		ctx := context.Background()
		for _, rel := range pending {
			if epoch != r.epoch.Load() || !r.IsOpen() {
				return // row changed or panel closed: drop the rest
			}
			val, err := r.preview(ctx, rel.fk, rel.refValues)
			if err != nil {
				// No display column / lookup error / timeout: keep the raw
				// fallback line (don't cache), panel stays alive.
				r.logErr("relationship_panel.preview", err)
				continue
			}
			r.mu.Lock()
			r.previews[rel.previewKey] = formatPreviewValue(val)
			r.mu.Unlock()
			r.repaintPreview(epoch)
		}
	}

	if r.onWorker == nil {
		run() // unit-test wiring: resolve synchronously
		return
	}
	r.onWorker(run)
}

// startEstimateFill kicks off the sequential inbound-estimate fill for the
// focused row. Mirrors startPreviewFill: ONE worker resolving estimates one at
// a time (cap already applied by rebuildInboundLocked), skipping cached /
// unbound relationships, superseded when the motion epoch advances. A failed
// estimate is cached as degraded (muted "~?" marker) so the rest of the panel
// survives and a revisit issues no query. Repaints are content-only +
// epoch/open-guarded.
func (r *RelationshipPanelController) startEstimateFill(epoch uint64) {
	if r.estimate == nil {
		return
	}
	r.mu.Lock()
	pending := make([]inboundRel, 0, len(r.inbound))
	for _, in := range r.inbound {
		if in.estimateKey == "" {
			continue
		}
		if _, ok := r.estimates[in.estimateKey]; ok {
			continue
		}
		pending = append(pending, in)
	}
	r.mu.Unlock()
	if len(pending) == 0 {
		return
	}

	run := func() {
		ctx := context.Background()
		for _, in := range pending {
			if epoch != r.epoch.Load() || !r.IsOpen() {
				return // row changed or panel closed: drop the rest
			}
			rows, err := r.estimate(ctx, in.fk, in.pkValues)
			res := estimateResult{rows: rows}
			if err != nil {
				// Permission denied / timeout / driver error: cache degraded so
				// the line shows the muted error marker; panel stays alive.
				r.logErr("relationship_panel.estimate", err)
				res = estimateResult{degraded: true}
			}
			r.mu.Lock()
			r.estimates[in.estimateKey] = res
			r.mu.Unlock()
			r.repaintPreview(epoch)
		}
	}

	if r.onWorker == nil {
		run() // unit-test wiring: resolve synchronously
		return
	}
	r.onWorker(run)
}

// maybeStartExactFill resolves the EXACT inbound count on demand for the
// CURRENTLY-SELECTED inbound line, when the panel is focused. Unlike the
// estimate fill (which fills every inbound line on settle), this fires only for
// the focused row -> focused relationship, replacing that single line's
// ~estimate with the exact COUNT(*). A cache hit (already counted, or a cached
// hard error) issues no query; a timeout-fallback is NOT cached so a re-focus
// retries. Runs on the worker, epoch + open + focus guarded so a count
// returning after the cursor moved / tab switched / panel closed is discarded.
func (r *RelationshipPanelController) maybeStartExactFill(epoch uint64) {
	if r.exact == nil || !r.focused.Load() {
		return
	}
	r.mu.Lock()
	in, ok := r.selectedInbound()
	if !ok || in.estimateKey == "" || len(in.pkValues) == 0 {
		r.mu.Unlock()
		return
	}
	// Cache hit: an exact count already resolved, or a hard exact error already
	// cached. Either way no new query (refocus is free).
	if res, present := r.estimates[in.estimateKey]; present && (res.hasExact || res.exactErr) {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	run := func() {
		if epoch != r.epoch.Load() || !r.IsOpen() || !r.focused.Load() {
			return // row changed, panel closed, or focus left: drop
		}
		n, err := r.exact(context.Background(), in.fk, in.pkValues)
		// A timeout keeps the ~estimate: do NOT mark hasExact/exactErr, and do
		// NOT cache, so a subsequent re-focus retries the count.
		if errors.Is(err, context.DeadlineExceeded) {
			r.logErr("relationship_panel.exact_count_timeout", err)
			return
		}
		r.mu.Lock()
		res := r.estimates[in.estimateKey] // preserve the existing ~estimate fields
		if err != nil {
			// Permission denied / driver error: muted exact-error marker; the
			// panel + other lines stay alive. Cached so a refocus issues no query.
			r.logErr("relationship_panel.exact_count", err)
			res.exactErr = true
		} else {
			res.exact = n
			res.hasExact = true
		}
		r.estimates[in.estimateKey] = res
		r.mu.Unlock()
		r.repaintPreview(epoch)
	}

	if r.onWorker == nil {
		run() // unit-test wiring: resolve synchronously
		return
	}
	r.onWorker(run)
}

// EvictExactCounts drops every cached count (exact + estimate) for child table
// (schema, table) — every cache entry whose key is prefixed "schema.table|".
// Called on a DML commit affecting that table so the next focus recomputes
// against the committed data. Works even when the panel is CLOSED: the cache
// survives close, so a committed-then-revisited row must not serve a stale
// count. An empty schema matches keys built from an unqualified base table.
func (r *RelationshipPanelController) EvictExactCounts(schema, table string) {
	if table == "" {
		return
	}
	prefix := schema + "." + table + "|"
	r.mu.Lock()
	for k := range r.estimates {
		if strings.HasPrefix(k, prefix) {
			delete(r.estimates, k)
		}
	}
	r.mu.Unlock()
}

// EvictAllExactCounts drops every cached count (exact + estimate) for every
// relationship. The coarse counterpart of EvictExactCounts, used on a DML
// commit whose target table is not reliably known (query-editor DML, which
// carries no parsed target table). Correct but broad: the next focus on any row
// recomputes. Works even when the panel is closed.
func (r *RelationshipPanelController) EvictAllExactCounts() {
	r.mu.Lock()
	r.estimates = map[string]estimateResult{}
	r.mu.Unlock()
}

// repaintPreview marshals a content-only repaint after a preview lands. It is
// epoch + open guarded so a superseded fill never paints a stale row.
func (r *RelationshipPanelController) repaintPreview(epoch uint64) {
	if r.ctx == nil {
		return
	}
	render := func() error {
		if epoch != r.epoch.Load() || !r.IsOpen() {
			return nil
		}
		r.ctx.SetBody(r.renderBodyFromState())
		return r.ctx.HandleRender()
	}
	if r.onUIContentOnly == nil {
		if epoch == r.epoch.Load() && r.IsOpen() {
			r.ctx.SetBody(r.renderBodyFromState())
		}
		return
	}
	r.onUIContentOnly(render)
}

// formatPreviewValue renders a resolved display value for the panel line. A
// nil scalar (parent row absent) renders the raw-fallback marker so the line
// still reads sensibly; otherwise the value's default string form.
func formatPreviewValue(v any) string {
	if v == nil {
		return "(missing)"
	}
	return fmt.Sprintf("%v", v)
}

// fkValuePairs renders the FK's referencing columns paired with the
// focused row's cell values as "col=value, col2=value2". A column not
// present in the result (or a not-yet-loaded row) renders "col=?".
func fkValuePairs(fk models.ForeignKey, cols []string, values []any) string {
	parts := make([]string, 0, len(fk.Columns))
	for _, col := range fk.Columns {
		val := "?"
		if idx := indexOf(cols, col); idx >= 0 && idx < len(values) {
			val = fmt.Sprintf("%v", values[idx])
		}
		parts = append(parts, col+"="+val)
	}
	return strings.Join(parts, ", ")
}

// rowSnapshot returns the active tab's column names + the focused row's
// values. Returns nils when the grid / row is unavailable.
func rowSnapshot(tab *ui.Tab) (cols []string, values []any) {
	if tab == nil {
		return nil, nil
	}
	g := tab.Grid()
	if g == nil {
		return nil, nil
	}
	for _, c := range g.Columns() {
		cols = append(cols, c.Name)
	}
	row, _ := g.CursorPosition()
	rows := g.AllRows()
	if row >= 0 && row < len(rows) {
		values = rows[row].Values
	}
	return cols, values
}

// splitBaseTable extracts (schema, table) from the active tab's
// ResultIdentity.BaseTable. A bare identifier returns ("", table).
func splitBaseTable(tab *ui.Tab) (schema, table string) {
	_, ri := tab.Identity()
	bt := ri.BaseTable
	if before, after, ok := strings.Cut(bt, "."); ok {
		return before, after
	}
	return "", bt
}

// indexOf returns the position of s in xs, or -1.
func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}
