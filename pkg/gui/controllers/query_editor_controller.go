package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// newRunID mints a short identifier scoping a single
// QueryEditorController action invocation (one <leader>r / <leader>R /
// <leader>!). NoticeHelper uses it as the ShowOrUpdate key so each run
// gets a fresh first-of-run toast. UnixNano is sufficient for keying
// within a single process; no cross-process uniqueness required.
func newRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UnixNano())
}

// queryToastTTL is the lifetime of toasts the QueryEditorController
// surfaces (no-statement, no-session, disabled-by-driver, ...). 4s is
// the project-wide default for transient status messages.
const queryToastTTL = 4 * time.Second

// resultTabLabelMax bounds the length of the tab-title prefix derived
// from the SQL statement. The full title is "<prefix>…" when the SQL
// exceeds the cap.
const resultTabLabelMax = 40

// maxVisualRunBatch caps the number of statements <leader>r will fan
// out in Visual mode (dbsavvy-wwd.7). Selections wider than this toast
// + abort BEFORE any runner.Run fires so the user can narrow the range
// rather than discovering 200 result tabs after the fact.
const maxVisualRunBatch = 32

// QueryEditorController owns the QUERY_EDITOR scope keybindings:
// <leader>r, <leader>R, <leader>e, <leader>E, <leader>x, <leader>!.
// Handlers delegate dispatch to QueryRunner and route the launched
// RunHandle / Plan to ResultTabsHelper. The controller itself is
// responsible for:
//
//   - reading the buffer + cursor via EditorBufferReader
//   - splitting statements via pkg/gui/editor.SplitStatements
//   - surfacing toasts (no statement / no session / disabled by driver)
//   - wiring the <leader>x GetDisabled closure that consults the live
//     QueryRunner.Capabilities at dispatch time (driver caps may not be
//     known until Bind() runs post-Connect)
//
// dbsavvy-66p.11.
type QueryEditorController struct {
	baseController
}

// NewQueryEditorController constructs the controller. c may be nil
// (tests inject without a Common). Helpers fields the controller uses
// (QueryRunner, ResultTabs, EditorBuffer, Toast) may individually be
// nil; every handler nil-checks at call time.
func NewQueryEditorController(c *common.Common, helpers HelperBag) *QueryEditorController {
	return &QueryEditorController{baseController: newBase(c, helpers)}
}

// GetKeybindings publishes the query-editor bindings under QUERY_EDITOR
// scope. All are leader-prefixed; SequenceFromShorthand emits a
// KeyLeader placeholder that Build expands to the configured leader rune
// before trie insert. <leader>r appears twice on purpose — once for
// Normal mode and once for the Visual modes (see the spec block below).
func (q *QueryEditorController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := q.tr()
	type bspec struct {
		shorthand   string
		actionID    string
		description string
		// mode is the Mode mask this binding fires under. Zero means
		// fall back to defaultMode (Normal). INSERT is deliberately
		// EXCLUDED (dbsavvy-1yb): with leader=<space>, an INSERT-mode
		// mask makes the space rune a chord prefix, so the matcher
		// buffers it until tlen — producing the "select*" → "select *"
		// reordering bug. Normal + Visual coverage is published as two
		// specs (never one OR'd mask) because ModeNormal is a zero
		// sentinel that vanishes from a multi-bit mask (dbsavvy-8u4).
		mode types.Mode
	}
	defaultMode := types.ModeNormal
	// <leader>r runs the statement under cursor in Normal mode AND runs
	// the selection in the Visual modes (wwd.7). It MUST be published as
	// two separate specs: ModeNormal is the zero sentinel (types/mode.go),
	// so `ModeNormal | ModeVisual | …` collapses to the Visual bits only
	// (0 | X == X) and fanOutBinding — which only treats Normal specially
	// when cb.Mode == ModeNormal exactly — would silently drop the Normal
	// entry, leaving <leader>r dead in the very mode queries are run from
	// (dbsavvy-8u4).
	visualRunModes := types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock
	specs := []bspec{
		{"<leader>r", commands.QueryRun, tr.Actions.RunQuery, defaultMode},
		{"<leader>r", commands.QueryRun, tr.Actions.RunQuery, visualRunModes},
		{"<leader>R", commands.QueryRunAll, tr.Actions.QueryRunAll, 0},
		{"<leader>e", commands.QueryExplain, tr.Actions.QueryExplain, 0},
		{"<leader>E", commands.QueryExplainAnalyze, tr.Actions.QueryExplainAnalyze, 0},
		{"<leader>x", commands.QueryCancel, tr.Actions.CancelQuery, 0},
		{"<leader>!", commands.QueryRunInNewTx, tr.Actions.QueryRunInNewTx, 0},
	}
	out := make([]*types.ChordBinding, 0, len(specs)+6)
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		mode := s.mode
		if mode == 0 {
			mode = defaultMode
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        mode,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
		})
	}
	// Rail-switch bindings (digits 1-5 + <tab>) so the user can hop out
	// of the editor back to a side rail. Scoped to QUERY_EDITOR; the
	// same set lives under each rail's scope via the rail controllers.
	out = append(out, railSwitchBindings(string(types.QUERY_EDITOR), tr)...)
	return out
}

// RegisterActions registers the six query-editor commands with reg.
// The <leader>x cancel command uses a GetDisabled closure that reads
// q.helpers.QueryRunner.Capabilities() at dispatch time, so a runner
// bootstrapped before Connect (caps={}) is upgraded transparently
// once Bind() lands the real driver caps.
func (q *QueryEditorController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	tr := q.tr()

	_ = reg.Register(&commands.Command{
		ID:          commands.QueryRun,
		Description: "Run statement under cursor",
		Tag:         "Query",
		Handler:     q.handleRun,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.QueryRunAll,
		Description: "Run every statement in the buffer",
		Tag:         "Query",
		Handler:     q.handleRunAll,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.QueryExplain,
		Description: "Explain statement under cursor",
		Tag:         "Query",
		Handler:     q.handleExplain,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.QueryExplainAnalyze,
		Description: "Explain (analyze) statement under cursor",
		Tag:         "Query",
		Handler:     q.handleExplainAnalyze,
	})

	// Capture the runner pointer; QueryRunner.Capabilities() reads
	// through an atomic.Pointer so post-Bind caps are observed here.
	runner := q.helpers.QueryRunner
	cancel := &commands.Command{
		ID:          commands.QueryCancel,
		Description: "Cancel the active query",
		Tag:         "Query",
		Handler:     q.handleCancel,
		GetDisabled: func(commands.ExecCtx) (string, bool) {
			if runner == nil {
				return "", false
			}
			if !runner.Capabilities().HasLiveCancel {
				return tr.DisabledNoLiveCancel, true
			}
			return "", false
		},
	}
	_ = reg.Register(cancel)

	_ = reg.Register(&commands.Command{
		ID:          commands.QueryRunInNewTx,
		Description: "Run statement in a new transaction",
		Tag:         "Query",
		Handler:     q.handleRunInNewTx,
	})
}

// AttachToContext registers GetKeybindings on the supplied context.
// In v1 the QUERY_EDITOR context is still a StubContext, whose
// AddKeybindingsFn is a no-op — the bindings reach the trie via
// AllDefaultBindings instead. Once the live QUERY_EDITOR context
// ships, AttachToContext begins contributing here too.
func (q *QueryEditorController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(q.GetKeybindings)
}

// --- Handlers ---

func (q *QueryEditorController) handleRun(ec commands.ExecCtx) error {
	return q.runOne(ec, false)
}

func (q *QueryEditorController) handleRunInNewTx(ec commands.ExecCtx) error {
	return q.runOne(ec, true)
}

// runOne dispatches <leader>r / <leader>!. In Visual mode it fans the
// selection out through SplitStatements (capped at maxVisualRunBatch);
// otherwise it falls through to the statement-under-cursor path.
func (q *QueryEditorController) runOne(ec commands.ExecCtx, newTx bool) error {
	if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
		return q.runVisualSelection(newTx)
	}
	stmt := q.statementUnderCursor()
	if stmt == "" {
		q.toast("no statement under cursor")
		return nil
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return nil
	}
	runID := newRunID()
	if q.helpers.Notice != nil {
		q.helpers.Notice.OnRunStart(runID)
	}
	attached := q.runStatement(stmt, data.RunOptions{NewTx: newTx})
	if q.helpers.Notice != nil {
		if attached {
			q.helpers.Notice.Finish(runID)
		} else {
			q.helpers.Notice.OnRunEnd(runID)
		}
	}
	return nil
}

// RunSQL executes a single externally-supplied statement through the
// same path as <leader>r — open a run scope, dispatch via runStatement
// (which streams rows into a result tab), close the scope — without
// reading the editor buffer. Lets callers outside the QUERY_EDITOR
// context (e.g. the TABLES <CR> "open table data" path, dbsavvy-gj8)
// reuse the full run/stream/tab machinery. Returns true when a run was
// launched; false when stmt is blank or there is no active session, so
// the caller can skip focusing an empty results panel.
func (q *QueryEditorController) RunSQL(stmt string) bool {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return false
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return false
	}
	runID := newRunID()
	if q.helpers.Notice != nil {
		q.helpers.Notice.OnRunStart(runID)
	}
	attached := q.runStatement(stmt, data.RunOptions{})
	if q.helpers.Notice != nil {
		if attached {
			q.helpers.Notice.Finish(runID)
		} else {
			q.helpers.Notice.OnRunEnd(runID)
		}
	}
	return true
}

// runVisualSelection handles <leader>r when one of the Visual modes is
// active. SelectionText -> SplitStatements -> per-statement runStatement
// fan-out. The cap check fires BEFORE any runner.Run so over-cap
// selections are rejected wholesale; partial runs are intentionally
// avoided (dbsavvy-wwd.7).
func (q *QueryEditorController) runVisualSelection(newTx bool) error {
	if q.helpers.EditorBuffer == nil {
		q.toast("no selection")
		return nil
	}
	text, ok := q.helpers.EditorBuffer.SelectionText()
	if !ok || strings.TrimSpace(text) == "" {
		q.toast("no selection")
		return nil
	}
	stmts := editor.SplitStatements(text)
	// Count non-empty statements for the cap check so a leading ';'
	// doesn't artificially inflate the count.
	nonEmpty := 0
	for _, s := range stmts {
		if strings.TrimSpace(s) != "" {
			nonEmpty++
		}
	}
	if nonEmpty == 0 {
		q.toast("no statements found")
		return nil
	}
	if nonEmpty > maxVisualRunBatch {
		q.toast(fmt.Sprintf("visual run: %d statements exceeds cap %d; narrow selection", nonEmpty, maxVisualRunBatch))
		return nil
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return nil
	}
	runID := newRunID()
	if q.helpers.Notice != nil {
		q.helpers.Notice.OnRunStart(runID)
	}
	attached := 0
	for _, raw := range stmts {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		if q.runStatement(stmt, data.RunOptions{NewTx: newTx}) {
			attached++
		}
	}
	if q.helpers.Notice != nil {
		if attached == 0 {
			q.helpers.Notice.OnRunEnd(runID)
		} else {
			q.helpers.Notice.Finish(runID)
		}
	}
	return nil
}

func (q *QueryEditorController) handleRunAll(_ commands.ExecCtx) error {
	buf := q.bufferText()
	stmts := editor.SplitStatements(buf)
	if len(stmts) == 0 {
		q.toast("no statements found")
		return nil
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return nil
	}
	runID := newRunID()
	if q.helpers.Notice != nil {
		q.helpers.Notice.OnRunStart(runID)
	}
	attached := 0
	for _, raw := range stmts {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		if q.runStatement(stmt, data.RunOptions{}) {
			attached++
		}
	}
	if q.helpers.Notice != nil {
		if attached == 0 {
			// Nothing attached → no drain worker will fire OnRunEnd.
			// Tear down the run scope directly to avoid stranding state.
			q.helpers.Notice.OnRunEnd(runID)
		} else {
			q.helpers.Notice.Finish(runID)
		}
	}
	return nil
}

// runStatement dispatches a single SQL statement through QueryRunner.
// Returns true when a NoticeReporter stream was attached (i.e. the run
// is in-flight); false when the runner errored before launch or when
// no NoticeReporter is wired. Used by runOne / runVisualSelection /
// handleRunAll to tally the attached count their run-scope teardown
// depends on. Pre-conditions (no session, no statement) are the
// caller's responsibility — runStatement assumes runner is non-nil and
// stmt is non-empty.
func (q *QueryEditorController) runStatement(stmt string, opts data.RunOptions) bool {
	runner := q.helpers.QueryRunner
	// Resolve unqualified object names against the currently selected schema
	// (SCHEMAS rail). Empty when no schema is selected, leaving resolution to
	// the session default (dbsavvy-u1n).
	if q.helpers.Schemas != nil {
		opts.DefaultSchema = q.helpers.Schemas.SelectedSchemaName()
	}
	// Last-wins preemption of any in-flight stream is centralized in the
	// QueryRunner chokepoint (QueryRunner.Run preempts before acquiring the
	// per-session queue lock), covering run / RunQuery / Explain uniformly
	// (dbsavvy-lxn.1).
	rh, err := runner.Run(context.Background(), stmt, opts)
	if err != nil {
		q.surfaceErr(stmt, err)
		return false
	}
	attached := false
	if q.helpers.Notice != nil {
		q.helpers.Notice.AttachStream(rh)
		attached = true
	}
	q.openResultTab(stmt, rh)
	return attached
}

func (q *QueryEditorController) handleExplain(ec commands.ExecCtx) error {
	return q.explain(ec, false)
}

func (q *QueryEditorController) handleExplainAnalyze(ec commands.ExecCtx) error {
	return q.explain(ec, true)
}

func (q *QueryEditorController) explain(_ commands.ExecCtx, analyze bool) error {
	stmt := q.statementUnderCursor()
	if stmt == "" {
		q.toast("no statement under cursor")
		return nil
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return nil
	}
	// Resolve unqualified names against the selected schema, mirroring the run
	// path so EXPLAIN reflects what Run would execute (dbsavvy-u1n).
	defaultSchema := ""
	if q.helpers.Schemas != nil {
		defaultSchema = q.helpers.Schemas.SelectedSchemaName()
	}
	plan, err := runner.Explain(context.Background(), stmt, analyze, defaultSchema)
	if err != nil {
		q.surfaceErr(stmt, err)
		return nil
	}
	q.openPlanTab(stmt, plan)
	return nil
}

func (q *QueryEditorController) handleCancel(_ commands.ExecCtx) error {
	runner := q.helpers.QueryRunner
	if runner == nil {
		return nil
	}
	// DisabledReasonStatic gates this binding when caps say no — if a
	// caller reaches the handler anyway (e.g. direct registry dispatch
	// in a test), surface the same toast for symmetry.
	if !runner.Capabilities().HasLiveCancel {
		q.toast(q.tr().DisabledNoLiveCancel)
		return nil
	}
	_ = runner.Cancel()
	return nil
}

// --- Internal helpers ---

func (q *QueryEditorController) statementUnderCursor() string {
	if q.helpers.EditorBuffer == nil {
		return ""
	}
	buf := q.helpers.EditorBuffer.BufferText()
	off := q.helpers.EditorBuffer.CursorOffset()
	return strings.TrimSpace(editor.StatementAt(buf, off))
}

func (q *QueryEditorController) bufferText() string {
	if q.helpers.EditorBuffer == nil {
		return ""
	}
	return q.helpers.EditorBuffer.BufferText()
}

func (q *QueryEditorController) toast(msg string) {
	if q.helpers.Toast == nil {
		return
	}
	q.helpers.Toast.Show(msg, queryToastTTL)
}

func (q *QueryEditorController) surfaceErr(stmt string, err error) {
	if q.helpers.ResultTabs != nil {
		q.helpers.ResultTabs.ShowError(tabLabel(stmt), err)
		return
	}
	q.toast(err.Error())
}

func (q *QueryEditorController) openResultTab(stmt string, rh *session.RunHandle) {
	if q.helpers.ResultTabs == nil || rh == nil {
		return
	}
	_ = q.helpers.ResultTabs.OpenResultTab(tabLabel(stmt), rh)
	// dbsavvy-uv0.6: record (connID, ResultIdentity) on the now-active
	// tab so the <leader>gH overlay can gate persistence and seed the
	// grid's hidden-col set from AppState. Optional surface — fake test
	// helpers don't implement it, so the type-assertion gates this off
	// in unit tests while production *ui.ResultTabsHelper satisfies it.
	// connID falls back to "" when no connection is open (overlay then
	// runs session-only).
	if attacher, ok := q.helpers.ResultTabs.(ResultTabIdentityAttacher); ok {
		connID := ""
		if q.helpers.ActiveConnection != nil {
			connID = q.helpers.ActiveConnection.ActiveConnectionID()
		}
		attacher.AttachActiveTabIdentity(connID, query.DetectFromQuery(stmt))
	}
}

func (q *QueryEditorController) openPlanTab(stmt string, plan models.Plan) {
	if q.helpers.ResultTabs == nil {
		return
	}
	_ = q.helpers.ResultTabs.OpenPlanTab(tabLabel(stmt), plan)
}

// tabLabel produces the result-tab title from sql. Whitespace is
// collapsed to a single space; the result is truncated to
// resultTabLabelMax with a trailing ellipsis when it would exceed the
// cap.
func tabLabel(sql string) string {
	clean := strings.Join(strings.Fields(sql), " ")
	if len(clean) <= resultTabLabelMax {
		return clean
	}
	return clean[:resultTabLabelMax] + "…"
}
