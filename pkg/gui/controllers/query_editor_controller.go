package controllers

import (
	"context"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// queryToastTTL is the lifetime of toasts the QueryEditorController
// surfaces (no-statement, no-session, disabled-by-driver, ...). 4s is
// the project-wide default for transient status messages.
const queryToastTTL = 4 * time.Second

// resultTabLabelMax bounds the length of the tab-title prefix derived
// from the SQL statement. The full title is "<prefix>…" when the SQL
// exceeds the cap.
const resultTabLabelMax = 40

// QueryEditorController owns the QUERY_EDITOR scope keybindings:
// <leader>r, <leader>R, <leader>e, <leader>E, <leader>x, <leader>!.
// Handlers delegate dispatch to QueryRunner and route the launched
// RunHandle / Plan to ResultTabsHelper. The controller itself is
// responsible for:
//
//   - reading the buffer + cursor via EditorBufferReader
//   - splitting statements via pkg/gui/editor.SplitStatements
//   - surfacing toasts (no statement / no session / disabled by driver)
//   - wiring the <leader>x DisabledReasonStatic based on driver caps
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

// GetKeybindings publishes the six query-editor bindings under
// QUERY_EDITOR scope. All six are leader-prefixed; SequenceFromShorthand
// emits a KeyLeader placeholder that Build expands to the configured
// leader rune before trie insert.
func (q *QueryEditorController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := q.tr()
	type bspec struct {
		shorthand   string
		actionID    string
		description string
	}
	specs := []bspec{
		{"<leader>r", commands.QueryRun, tr.Actions.RunQuery},
		{"<leader>R", commands.QueryRunAll, tr.Actions.QueryRunAll},
		{"<leader>e", commands.QueryExplain, tr.Actions.QueryExplain},
		{"<leader>E", commands.QueryExplainAnalyze, tr.Actions.QueryExplainAnalyze},
		{"<leader>x", commands.QueryCancel, tr.Actions.CancelQuery},
		{"<leader>!", commands.QueryRunInNewTx, tr.Actions.QueryRunInNewTx},
	}
	out := make([]*types.ChordBinding, 0, len(specs))
	for _, s := range specs {
		seq, err := keys.SequenceFromShorthand(s.shorthand)
		if err != nil {
			continue
		}
		out = append(out, &types.ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal | types.ModeInsert,
			Scope:       types.QUERY_EDITOR,
			ActionID:    s.actionID,
			Description: s.description,
		})
	}
	return out
}

// RegisterActions registers the six query-editor commands with reg.
// The <leader>x cancel command has its DisabledReasonStatic set when
// the active driver lacks live-cancel support; the field is read once
// at registration time and never re-set (driver swap = reconnect =
// fresh bootstrap = fresh Registry).
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

	cancel := &commands.Command{
		ID:          commands.QueryCancel,
		Description: "Cancel the active query",
		Tag:         "Query",
		Handler:     q.handleCancel,
	}
	if q.helpers.QueryRunner != nil && !q.helpers.QueryRunner.Capabilities().HasLiveCancel {
		cancel.DisabledReasonStatic = tr.DisabledNoLiveCancel
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

func (q *QueryEditorController) runOne(_ commands.ExecCtx, newTx bool) error {
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
	rh, err := runner.Run(context.Background(), stmt, data.RunOptions{NewTx: newTx})
	if err != nil {
		q.surfaceErr(stmt, err)
		return nil
	}
	q.openResultTab(stmt, rh)
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
	for _, raw := range stmts {
		stmt := strings.TrimSpace(raw)
		if stmt == "" {
			continue
		}
		rh, err := runner.Run(context.Background(), stmt, data.RunOptions{})
		if err != nil {
			q.surfaceErr(stmt, err)
			continue
		}
		q.openResultTab(stmt, rh)
	}
	return nil
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
	plan, err := runner.Explain(context.Background(), stmt, analyze)
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
