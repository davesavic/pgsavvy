package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/editor/format"
	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/logs"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
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
// out in Visual mode. Selections wider than this toast
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
type QueryEditorController struct {
	baseController

	// onDMLCommit fires (on the worker, after the statement completes
	// successfully) when a query-editor statement is classified KindDML, so the
	// relationship panel can evict its cached inbound counts. Coarse: the
	// query-editor DML path carries no parsed target table, so the wired closure
	// drops ALL cached counts. Nil-safe (no-op when unwired).
	onDMLCommit func()

	// saveHelper persists a named query to queries.yml (name prompt +
	// overwrite-confirm). savePrompter is the blocking ChainedPrompter the
	// helper drives. Both are wired post-construction via SetSaveQuery so the
	// constructor signature stays stable; handleSave no-ops (one toast) when
	// either is unwired.
	saveHelper   *data.SaveQueryHelper
	savePrompter data.ChainedPrompter

	// onAfterRun fires (on the UI thread, right after a statement is
	// dispatched successfully) so the History tab can mark its list stale — a
	// new history row was appended, so the next History-tab activation reloads.
	// Nil-safe (no-op when unwired).
	onAfterRun func()

	// onAfterSave fires (on the UI thread, after a successful <leader>s save)
	// so the Saved Queries tab can mark its list stale. Nil-safe.
	onAfterSave func()
}

// SetOnAfterRun wires the post-dispatch hook (History-tab staleness). Wired
// post-construction so the constructor signature stays stable. Nil leaves the
// path a no-op.
func (q *QueryEditorController) SetOnAfterRun(fn func()) {
	q.onAfterRun = fn
}

// SetOnAfterSave wires the post-save hook (Saved-Queries-tab staleness). Wired
// post-construction so the constructor signature stays stable. Nil leaves the
// path a no-op.
func (q *QueryEditorController) SetOnAfterSave(fn func()) {
	q.onAfterSave = fn
}

// SetOnDMLCommit wires the post-DML-commit hook (relationship-count eviction).
// Wired post-construction so the constructor signature stays stable. Nil leaves
// the path a no-op.
func (q *QueryEditorController) SetOnDMLCommit(fn func()) {
	q.onDMLCommit = fn
}

// SetSaveQuery wires the <leader>s save flow: the SaveQueryHelper (name
// prompt + overwrite-confirm + persist) and the blocking ChainedPrompter it
// drives. Wired post-construction so the constructor signature stays stable.
// Leaving either nil leaves handleSave a single-toast no-op.
func (q *QueryEditorController) SetSaveQuery(helper *data.SaveQueryHelper, prompter data.ChainedPrompter) {
	q.saveHelper = helper
	q.savePrompter = prompter
}

// NewQueryEditorController constructs the controller. c may be nil
// (tests inject without a Common). Helpers fields the controller uses
// (QueryRunner, ResultTabs, EditorBuffer, Toast) may individually be
// nil; every handler nil-checks at call time. threading supplies OnWorker,
// used to wait on a DDL run's completion for post-run metadata invalidation
// a zero ThreadingDeps disables that path (nil OnWorker).
func NewQueryEditorController(c *common.Common, core CoreDeps, nav NavDeps, ui UIDeps, query QueryDeps, threading ThreadingDeps) *QueryEditorController {
	q := &QueryEditorController{baseController: newBase(c, HelperBag{CoreDeps: core, NavDeps: nav, UIDeps: ui, QueryDeps: query, ThreadingDeps: threading})}
	// Wire the single sort sink both entry points (the <leader>s picker +
	// the grid header double-click) route through. The optional-interface
	// type-assert lets tests with a bare ResultTabs fake skip the wiring
	// (sort stays a no-op there).
	if hooker, ok := q.helpers.ResultTabs.(ResultTabSortHooker); ok {
		hooker.SetOnSortRequest(q.sortActiveResult)
	}
	return q
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
		// EXCLUDED: with leader=<space>, an INSERT-mode
		// mask makes the space rune a chord prefix, so the matcher
		// buffers it until tlen — producing the "select*" → "select *"
		// reordering bug. Normal + Visual coverage is published as two
		// specs (never one OR'd mask) because ModeNormal is a zero
		// sentinel that vanishes from a multi-bit mask.
		mode types.Mode
		// showInBar flags this binding for the status options bar.
		// Only the top run/explain chords are flagged.
		showInBar bool
	}
	defaultMode := types.ModeNormal
	// <leader>r runs the statement under cursor in Normal mode AND runs
	// the selection in the Visual modes. It MUST be published as
	// two separate specs: ModeNormal is the zero sentinel (types/mode.go),
	// so `ModeNormal | ModeVisual | …` collapses to the Visual bits only
	// (0 | X == X) and fanOutBinding — which only treats Normal specially
	// when cb.Mode == ModeNormal exactly — would silently drop the Normal
	// entry, leaving <leader>r dead in the very mode queries are run from.
	visualRunModes := types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock
	specs := []bspec{
		{"<leader>r", commands.QueryRun, tr.Actions.RunQuery, defaultMode, true},
		{"<leader>r", commands.QueryRun, tr.Actions.RunQuery, visualRunModes, false},
		{"<leader>R", commands.QueryRunAll, tr.Actions.QueryRunAll, 0, false},
		{"<leader>e", commands.QueryExplain, tr.Actions.QueryExplain, 0, true},
		{"<leader>E", commands.QueryExplainAnalyze, tr.Actions.QueryExplainAnalyze, 0, false},
		{"<leader>x", commands.QueryCancel, tr.Actions.CancelQuery, 0, false},
		{"<leader>!", commands.QueryRunInNewTx, tr.Actions.QueryRunInNewTx, 0, false},
		{"<leader>f", commands.QueryFormat, tr.Actions.QueryFormat, defaultMode, false},
		{"<leader>f", commands.QueryFormat, tr.Actions.QueryFormat, visualRunModes, false},
		{"<leader>h", commands.HistoryOpen, tr.Actions.HistoryOpen, defaultMode, false},
		{"<leader>o", commands.QuerySavedOpen, tr.Actions.OpenSavedQueries, defaultMode, false},
		{"<leader>s", commands.QuerySave, tr.Actions.SaveQuery, defaultMode, false},
	}
	out := make([]*types.ChordBinding, 0, len(specs)+8)
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
			ShowInBar:   s.showInBar,
		})
	}
	// Rail-switch bindings (digits 1-5 + <tab>) so the user can hop out
	// of the editor back to a side rail. Scoped to QUERY_EDITOR; the
	// same set lives under each rail's scope via the rail controllers.
	out = append(out, railSwitchBindings(string(types.QUERY_EDITOR), tr)...)

	// QUERY_RAIL tab cycle (`]` next / `[` prev, edge-wrapping). Normal mode
	// ONLY — Insert mode must keep literal `[`/`]` for SQL like `int[]`. The
	// handlers live in RegisterQueryRailTabActions; the same pair is published
	// under SAVED_QUERY / HISTORY by their leaf controllers.
	out = append(out,
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: ']'}},
			Mode:        types.ModeNormal,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.QueryRailTabNext,
			Description: tr.Actions.QueryRailTabNext,
			ShowInBar:   true,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: '['}},
			Mode:        types.ModeNormal,
			Scope:       types.QUERY_EDITOR,
			ActionID:    commands.QueryRailTabPrev,
			Description: tr.Actions.QueryRailTabPrev,
			ShowInBar:   true,
		},
	)
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

	_ = reg.Register(&commands.Command{
		ID:          commands.QueryFormat,
		Description: "Format SQL",
		Tag:         "Query",
		Handler:     q.handleFormat,
	})

	_ = reg.Register(&commands.Command{
		ID:          commands.QuerySave,
		Description: "Save query",
		Tag:         "Query",
		Handler:     q.handleSave,
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
	q.confirmRun([]string{stmt}, func() {
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
	})
	return nil
}

// handleSave dispatches <leader>s. It captures the current query text
// (visual selection if a Visual mode is active, else the statement under the
// cursor), trims it, and — when non-empty — hands it to the SaveQueryHelper
// on a worker goroutine. The helper drives the blocking name prompt + the
// overwrite-confirm choice; the success/error toast is marshalled back onto
// the UI thread.
//
// The blocking ChainedPrompter on a worker is the ONLY safe Prompt->Confirm
// shape here: nesting ConfirmHelper.Confirm inside PromptHelper.onSubmit
// double-pops the focus stack (both helpers issue an unconditional
// tree.Pop()).
func (q *QueryEditorController) handleSave(ec commands.ExecCtx) error {
	sql := strings.TrimSpace(q.captureSaveText(ec))
	if sql == "" {
		q.toast("no statement to save")
		return nil
	}
	if q.saveHelper == nil || q.savePrompter == nil {
		q.toast("save unavailable")
		return nil
	}
	if q.helpers.OnWorker == nil {
		// No worker scheduler wired: the blocking prompt would dead-lock the
		// UI goroutine. Toast rather than risk it.
		q.toast("save unavailable")
		return nil
	}
	q.helpers.OnWorker(func(_ gocui.Task) error {
		name, err := q.saveHelper.WalkSaveQuery(context.Background(), q.savePrompter, sql)
		q.toastFromWorker(saveToastMessage(name, err))
		// A successful save (non-empty name, no error) wrote a new/updated
		// entry to queries.yml, so flag the Saved Queries tab stale. Marshalled
		// to the UI thread (this runs on a worker).
		if err == nil && name != "" {
			q.afterSaveFromWorker()
		}
		return nil
	})
	return nil
}

// afterSaveFromWorker fires the onAfterSave hook on the UI thread (this is
// called from the save worker goroutine). Mirrors toastFromWorker's
// marshalling; falls back to a direct call when no OnUIThread scheduler is
// wired (test path: OnWorker runs inline). Nil-safe when the hook is unwired.
func (q *QueryEditorController) afterSaveFromWorker() {
	if q.onAfterSave == nil {
		return
	}
	if q.helpers.OnUIThread == nil {
		q.onAfterSave()
		return
	}
	q.helpers.OnUIThread(func() error {
		q.onAfterSave()
		return nil
	})
}

// captureSaveText returns the text <leader>s will persist: the visual
// selection when a Visual mode is active, otherwise the statement under the
// cursor. Mirrors runOne's mode split. The result is NOT trimmed here (the
// caller trims once); a multi-statement visual selection is returned verbatim
// as one blob (NOT split — a saved query is a single entry).
func (q *QueryEditorController) captureSaveText(ec commands.ExecCtx) string {
	if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
		if q.helpers.EditorBuffer == nil {
			return ""
		}
		text, ok := q.helpers.EditorBuffer.SelectionText()
		if !ok {
			return ""
		}
		return text
	}
	return q.statementUnderCursor()
}

// toastFromWorker surfaces msg on the UI thread when called from a worker
// goroutine, falling back to a direct toast when no OnUIThread scheduler is
// wired (test path: OnWorker runs inline so a direct toast is already on the
// caller goroutine). Empty msg is a no-op (a clean cancel surfaces nothing).
func (q *QueryEditorController) toastFromWorker(msg string) {
	if msg == "" {
		return
	}
	if q.helpers.OnUIThread == nil {
		q.toast(msg)
		return
	}
	q.helpers.OnUIThread(func() error {
		q.toast(msg)
		return nil
	})
}

// saveToastMessage maps a WalkSaveQuery result to the toast text. A clean
// cancel (name=="" && err==nil) returns "" (no toast). The empty-name
// sentinel maps to a single name-required toast; any other error surfaces its
// message; success confirms with the saved name.
func saveToastMessage(name string, err error) string {
	if err != nil {
		if errors.Is(err, data.SaveQueryEmptyNameErr()) {
			return "query name must not be empty"
		}
		return "save failed: " + err.Error()
	}
	if name == "" {
		return ""
	}
	return "saved query \"" + name + "\""
}

// RunSQL executes a single externally-supplied statement through the
// same path as <leader>r — open a run scope, dispatch via runStatement
// (which streams rows into a result tab), close the scope — without
// reading the editor buffer. Lets callers outside the QUERY_EDITOR
// context (e.g. the TABLES <CR> "open table data" path)
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
	q.confirmRun([]string{stmt}, func() {
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
	})
	return true
}

// runVisualSelection handles <leader>r when one of the Visual modes is
// active. SelectionText -> SplitStatements -> per-statement runStatement
// fan-out. The cap check fires BEFORE any runner.Run so over-cap
// selections are rejected wholesale; partial runs are intentionally
// avoided.
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
	// Collect non-empty statements once: the cap check, the confirm gate,
	// and the run loop all consume the same cleaned list (a leading ';'
	// would otherwise inflate the count).
	cleaned := nonEmptyStatements(editor.SplitStatements(text))
	if len(cleaned) == 0 {
		q.toast("no statements found")
		return nil
	}
	if len(cleaned) > maxVisualRunBatch {
		q.toast(fmt.Sprintf("visual run: %d statements exceeds cap %d; narrow selection", len(cleaned), maxVisualRunBatch))
		return nil
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return nil
	}
	q.confirmRun(cleaned, func() {
		runID := newRunID()
		if q.helpers.Notice != nil {
			q.helpers.Notice.OnRunStart(runID)
		}
		attached := 0
		for _, stmt := range cleaned {
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
	})
	return nil
}

// nonEmptyStatements trims each split statement and drops the blanks (a
// leading ';' or trailing newline yields empty fragments).
func nonEmptyStatements(stmts []string) []string {
	out := make([]string, 0, len(stmts))
	for _, raw := range stmts {
		if s := strings.TrimSpace(raw); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// confirmRun gates proceed behind a confirmation popup when the active
// connection requires it for any statement in stmts (ConfirmWrites for DML,
// ConfirmDDL for DDL). When no confirmation is needed proceed runs
// immediately; otherwise the popup's onYes invokes it, so each caller's
// synchronous run-scope bookkeeping still runs — just after the user
// confirms. Cancelling drops the run entirely.
func (q *QueryEditorController) confirmRun(stmts []string, proceed func()) {
	needsConfirm := q.runNeedsConfirm(stmts)
	if q.helpers.Confirm == nil || !needsConfirm {
		proceed()
		return
	}

	conn := q.helpers.ConnProfile()
	if conn != nil && q.helpers.Prompt != nil && strings.TrimSpace(conn.Name) != "" && needsConfirm {
		label := "type " + strconv.Quote(conn.Name) + " to confirm"
		onSubmit := func(value string) error {
			if strings.TrimSpace(value) == strings.TrimSpace(conn.Name) {
				return q.helpers.Confirm.Confirm("Confirm execution", confirmRunBody(stmts), func() error {
					proceed()
					return nil
				}, nil)
			}
			q.helpers.Toast.Show("connection name doesn't match", queryToastTTL)
			return nil
		}
		onCancel := func() error {
			q.helpers.Toast.Show("run cancelled", queryToastTTL)
			return nil
		}
		_ = q.helpers.Prompt.Prompt(label, "", onSubmit, onCancel)
		return
	}

	_ = q.helpers.Confirm.Confirm("Confirm execution", confirmRunBody(stmts), func() error {
		proceed()
		return nil
	}, nil)
}

// connReadOnly reports whether the active connection profile is read-only.
// Returns false when no profile is wired (test path), which is the safe
// default for the EffectiveAnalyze gate (writes are gated rather than waved
// through on a read-only assumption).
func (q *QueryEditorController) connReadOnly() bool {
	if q.helpers.ConnProfile == nil {
		return false
	}
	conn := q.helpers.ConnProfile()
	return conn != nil && conn.ReadOnly
}

// runNeedsConfirm reports whether any statement in stmts requires a
// confirmation prompt under the active connection's ConfirmWrites /
// ConfirmDDL flags.
func (q *QueryEditorController) runNeedsConfirm(stmts []string) bool {
	if q.helpers.ConnProfile == nil {
		return false
	}
	conn := q.helpers.ConnProfile()
	if conn == nil || (!conn.ConfirmWrites && !conn.ConfirmDDL) {
		return false
	}
	for _, s := range stmts {
		kind := query.EffectiveKind(s)
		if kind == query.KindDML && conn.ConfirmWrites {
			return true
		}
		if kind == query.KindDDL && conn.ConfirmDDL {
			return true
		}
	}
	return false
}

// connNeedsTypedNameGate reports whether the confirmRun typed-name gate
// should apply: a non-nil connection with a non-empty (TrimSpace) name and
// a true needsConfirm result.
func connNeedsTypedNameGate(conn *models.Connection, needsConfirm bool) bool {
	if conn == nil || !needsConfirm {
		return false
	}
	return strings.TrimSpace(conn.Name) != ""
}

// typedNameMatches compares the user input against the connection name
// after trimming whitespace from both sides.
func typedNameMatches(connName, input string) bool {
	return strings.TrimSpace(input) == strings.TrimSpace(connName)
}

// confirmRunBody builds the popup body: a single highlighted statement
// under an action header, or a numbered (capped) list for a multi-statement
// run so the user sees what actually executes — not just a count.
func confirmRunBody(stmts []string) string {
	if len(stmts) == 1 {
		return confirmSingleBody(stmts[0])
	}
	return confirmMultiBody(stmts)
}

// confirmSingleBody renders one statement: an action header (verb +
// effect, e.g. "UPDATE · writes data") above the syntax-highlighted SQL.
func confirmSingleBody(stmt string) string {
	header := confirmActionHeader(stmt)
	sql := highlight.Highlight(confirmSQLPreview(stmt))
	return fmt.Sprintf("%s\n\n%s", header, sql)
}

// confirmMultiBody renders a count line followed by a numbered, one-line,
// syntax-highlighted preview of each statement. The list is capped so the
// popup and its [y]es/[n]o hint stay on screen; overflow collapses to a
// "… +N more" tail.
func confirmMultiBody(stmts []string) string {
	const maxShown = 8
	shown := min(len(stmts), maxShown)

	var b strings.Builder
	fmt.Fprintf(&b, "Execute %d statements?\n", len(stmts))
	for i := range shown {
		fmt.Fprintf(&b, "\n  %d. %s", i+1, highlight.Highlight(confirmLinePreview(stmts[i])))
	}
	if len(stmts) > shown {
		fmt.Fprintf(&b, "\n  … +%d more", len(stmts)-shown)
	}
	return b.String()
}

// confirmActionHeader summarises a statement as "<VERB> · <effect>",
// e.g. "DELETE · writes data" or "ALTER · changes schema". The effect is
// dropped when the statement is neither DML nor DDL.
func confirmActionHeader(stmt string) string {
	verb := confirmVerb(stmt)
	effect := confirmEffectLabel(query.EffectiveKind(stmt))
	if effect == "" {
		return verb
	}
	return verb + " · " + effect
}

// confirmVerb returns the upper-cased leading keyword of stmt, or
// "STATEMENT" when stmt has no words.
func confirmVerb(stmt string) string {
	fields := strings.Fields(stmt)
	if len(fields) == 0 {
		return "STATEMENT"
	}
	return strings.ToUpper(fields[0])
}

// confirmEffectLabel maps a statement kind to a short, human effect.
func confirmEffectLabel(kind query.StatementKind) string {
	switch kind {
	case query.KindDML:
		return "writes data"
	case query.KindDDL:
		return "changes schema"
	default:
		return ""
	}
}

// confirmLinePreview collapses whitespace and caps stmt to a single short
// line for the multi-statement list (distinct from confirmSQLPreview's
// 400-char single-statement budget).
func confirmLinePreview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 72
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// confirmSQLPreview prepares a single statement for the confirmation
// popup. Whitespace runs collapse to single spaces so the popup view's
// word-wrap can reflow the statement to the box width (unlike the dense
// dry-run table, which uses truncateSQL's hard 64-char cap). An outsized
// statement is capped so the [y]es/[n]o prompt below it stays on screen.
func confirmSQLPreview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 400
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func (q *QueryEditorController) handleRunAll(_ commands.ExecCtx) error {
	cleaned := nonEmptyStatements(editor.SplitStatements(q.bufferText()))
	if len(cleaned) == 0 {
		q.toast("no statements found")
		return nil
	}
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return nil
	}
	q.confirmRun(cleaned, func() {
		runID := newRunID()
		if q.helpers.Notice != nil {
			q.helpers.Notice.OnRunStart(runID)
		}
		attached := 0
		for _, stmt := range cleaned {
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
	})
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
	// the session default.
	if q.helpers.Schemas != nil {
		opts.DefaultSchema = q.helpers.Schemas.SelectedSchemaName()
	}
	// Apply the configurable default statement-timeout ceiling unless the
	// caller already set a per-run override. 0 = off (no ceiling). The pg
	// driver realises a non-zero Timeout as a context.WithTimeout deadline
	// whose CancelFunc the row stream owns, so a runaway query is bounded
	// without leaking a timer past the stream.
	if opts.Timeout == 0 {
		opts.Timeout = q.defaultStatementTimeout()
	}
	// Last-wins preemption of any in-flight stream is centralized in the
	// QueryRunner chokepoint (QueryRunner.Run preempts before acquiring the
	// per-session queue lock), covering run / RunQuery / Explain uniformly.
	rh, err := runner.Run(context.Background(), stmt, opts)
	if err != nil {
		q.surfaceErr(stmt, err)
		return false
	}
	// A dispatched run appends a history row, so flag the History tab stale
	// (it reloads lazily on its next activation). Nil-safe.
	if q.onAfterRun != nil {
		q.onAfterRun()
	}
	attached := false
	if q.helpers.Notice != nil {
		q.helpers.Notice.AttachStream(rh)
		attached = true
	}
	q.openResultTab(stmt, rh)
	if rh != nil {
		q.scheduleDDLInvalidation(stmt, rh.Done(), rh.Err)
		q.scheduleDMLCacheEvict(stmt, rh.Done(), rh.Err)
	}
	return attached
}

// scheduleDDLInvalidation drops the warmed completion metadata for the active
// schema AFTER a local DDL statement completes SUCCESSFULLY (decision B). It
// fires from the POST-run signal (RunHandle.Done — passed
// as done), not the pre-run confirm gate, and is strictly gated on:
//
//   - EffectiveKind(stmt) == KindDDL — a plain SELECT/DML or a writable-CTE DML
//     (which EffectiveKind classifies KindDML) is excluded, so it never
//     invalidates.
//   - errFn() == nil after done closes — a failed DDL (syntax error, conflict)
//     leaves the metadata intact. errFn is RunHandle.Err, which is race-safe to
//     read only after done has closed (the contract this method honours).
//
// done + errFn are passed (rather than the *session.RunHandle) so the gate is
// unit-testable with fakes — no live session or RunHandle is needed to drive
// the completion signal.
//
// Per decision B the invalidation is WHOLE-SCHEMA of the ACTIVE schema rather
// than parsing the DDL's target table: the next WarmTable for any affected
// table reloads fresh columns/FKs. This is also the fail-safe for an
// unparseable target (we never parse, so the active-schema scope is always the
// unit of invalidation) — noted in the AC. A no-op when the invalidator is
// unwired, no schema is selected, done is nil, or no OnWorker is wired.
func (q *QueryEditorController) scheduleDDLInvalidation(stmt string, done <-chan struct{}, errFn func() error) {
	if done == nil || q.helpers.MetadataInvalidator == nil || q.helpers.OnWorker == nil {
		return
	}
	if query.EffectiveKind(stmt) != query.KindDDL {
		return
	}
	schema := ""
	if q.helpers.Schemas != nil {
		schema = q.helpers.Schemas.SelectedSchemaName()
	}
	if schema == "" {
		return
	}
	inv := q.helpers.MetadataInvalidator
	q.helpers.OnWorker(func(_ gocui.Task) error {
		<-done
		// Success-gate: a failed DDL must NOT invalidate (the on-disk shape did
		// not change, and a stale-but-correct entry beats a spurious reload).
		if errFn != nil && errFn() != nil {
			return nil
		}
		logs.Event(q.Log(), "completion", "ddl_invalidate_schema",
			slog.String("schema", schema))
		inv.InvalidateSchema(schema)
		return nil
	})
}

// scheduleDMLCacheEvict fires onDMLCommit AFTER a query-editor statement
// classified KindDML completes SUCCESSFULLY, so the relationship panel evicts
// its cached inbound counts (a committed INSERT/UPDATE/DELETE can change child
// references). Mirrors scheduleDDLInvalidation's POST-run, success-gated shape
// (done = RunHandle.Done, errFn = RunHandle.Err read only after done closes).
//
// Coarse by necessity: the query-editor DML path has no parsed target table
// (parsing raw DML for the target is fragile), so the wired closure drops ALL
// cached counts rather than a single table's. A no-op when the hook is unwired,
// done is nil, or no OnWorker is wired.
func (q *QueryEditorController) scheduleDMLCacheEvict(stmt string, done <-chan struct{}, errFn func() error) {
	if done == nil || q.onDMLCommit == nil || q.helpers.OnWorker == nil {
		return
	}
	if query.EffectiveKind(stmt) != query.KindDML {
		return
	}
	evict := q.onDMLCommit
	q.helpers.OnWorker(func(_ gocui.Task) error {
		<-done
		// Success-gate: a failed DML did not change the data, so the cached
		// counts are still valid — leave them.
		if errFn != nil && errFn() != nil {
			return nil
		}
		evict()
		return nil
	})
}

// reRunActiveTab re-runs runSQL into the active result tab, reusing the same
// tab + grid (sort cycle / clear). runSQL is supplied by the
// caller: a wrapSorted(...) string for a sort, or the tab's
// original SQL for a clear. The tab's origin (origSQL, origArgs,
// origDefaultSchema) is the write-once capture from .1 — origArgs +
// origDefaultSchema rebuild the exact query; origSQL is handed to the helper
// so it can re-seed hide-cols against the ORIGINAL identity. runSQL must NEVER
// be written back into the tab's origin (else clear would re-run a wrapped
// statement).
//
// RunQuery preempts the prior in-flight stream for the tab (its first action
// fires the preempt hook -> ResultTabsHelper.PreemptInFlight -> runner.Stop()),
// so the new "result_tab_<id>" task is NOT deduped by the ResultBufferManager.
// Returns true when the re-run was launched; false when no runner/session, no
// reattacher surface, or RunQuery errored.
//
// Contract entry point for the sort-cycle driver; reached via
// sortActiveResult, which the constructor wires to the sort entry points.
func (q *QueryEditorController) reRunActiveTab(runSQL string) bool {
	runner := q.helpers.QueryRunner
	if runner == nil || !runner.HasSession() {
		q.toast("no active connection")
		return false
	}
	reattacher, ok := q.helpers.ResultTabs.(ResultTabReattacher)
	if !ok {
		return false
	}
	origSQL, origArgs, origDefaultSchema := reattacher.ActiveTabOrigin()

	// RunQuery preempts any in-flight stream for the tab before acquiring the
	// per-session queue lock, so the prior stream is stopped/discarded and the
	// new task is not deduped.
	rh, err := runner.RunQuery(context.Background(), models.Query{
		SQL:           runSQL,
		Args:          origArgs,
		DefaultSchema: origDefaultSchema,
		Timeout:       q.defaultStatementTimeout(),
	})
	if err != nil {
		// A failed re-run surfaces as a normal query error on the active tab.
		// surfaceErr -> ShowError -> SetErrorSQL writes the canonical origSQL
		// field with runSQL, which on a wrapped sort would clobber the
		// original needed by a later clear. Pass origSQL (not runSQL) so the
		// tab's write-once origin survives a failed re-run.
		q.surfaceErr(origSQL, err)
		return false
	}
	if q.helpers.Notice != nil {
		q.helpers.Notice.AttachStream(rh)
	}
	reattacher.ReattachActiveTab(rh, runSQL, origSQL)
	return true
}

// sortActiveResult drives the database-side sort FLOW for the active result
// tab: it runs the helper's guards + asc→desc→clear cycle (ResultTabSorter),
// surfaces any toast it returns (e.g. the pending-edits block), and on a
// runnable result hands the built SQL to reRunActiveTab. col
// is a RAW 0-based grid column index supplied by the entry points.
//
// The single sort sink wired in NewQueryEditorController: both the <leader>s
// picker submit and the grid header double-click route through it via
// ResultTabsHelper.SetOnSortRequest. The active client-side
// filter is dropped as a side effect — the re-run reset in ReattachActiveTab
// rebuilds the grid from scratch, so no explicit filter-clear
// is needed here.
func (q *QueryEditorController) sortActiveResult(col int) {
	sorter, ok := q.helpers.ResultTabs.(ResultTabSorter)
	if !ok {
		return
	}
	runSQL, run, toast := sorter.SortActiveTab(col)
	if toast != "" {
		q.toast(toast)
	}
	if !run {
		return
	}
	q.reRunActiveTab(runSQL)
}

// defaultStatementTimeout returns the configured default statement-timeout
// ceiling (config.query.default_statement_timeout), or 0 (off) when no
// Common / config is wired (test path) or the key is unset.
func (q *QueryEditorController) defaultStatementTimeout() time.Duration {
	if q.c == nil {
		return 0
	}
	cfg := q.c.Cfg()
	if cfg == nil {
		return 0
	}
	return cfg.Query.DefaultStatementTimeout
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
	// Fail-closed gate: ANALYZE executes the statement, so downgrade it to an
	// estimate-only plan when the statement may write on a writable connection
	// (covers writable CTEs that classify as KindOther).
	effectiveAnalyze := query.EffectiveAnalyze(stmt, q.connReadOnly(), analyze)
	if analyze && !effectiveAnalyze {
		q.toast("ANALYZE skipped — statement may execute writes/side effects")
	}
	// Resolve unqualified names against the selected schema, mirroring the run
	// path so EXPLAIN reflects what Run would execute.
	defaultSchema := ""
	if q.helpers.Schemas != nil {
		defaultSchema = q.helpers.Schemas.SelectedSchemaName()
	}
	plan, err := runner.Explain(context.Background(), stmt, effectiveAnalyze, defaultSchema)
	if err != nil {
		q.surfaceErr(stmt, err)
		return nil
	}
	if plan.Notice != "" {
		q.toast(plan.Notice)
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

// --- Format handlers ---

func (q *QueryEditorController) handleFormat(ec commands.ExecCtx) error {
	if q.helpers.EditorBuffer == nil {
		return nil
	}
	if ec.Mode.Has(types.ModeVisual | types.ModeVisualLine | types.ModeVisualBlock) {
		return q.formatSelection()
	}
	return q.formatAll()
}

func (q *QueryEditorController) formatAll() error {
	text := q.helpers.EditorBuffer.BufferText()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	formatted, err := format.Format(text)
	if err != nil {
		q.toast("format: " + err.Error())
		return nil
	}
	return q.helpers.EditorBuffer.ReplaceAll(formatted)
}

func (q *QueryEditorController) formatSelection() error {
	text, ok := q.helpers.EditorBuffer.SelectionText()
	if !ok || strings.TrimSpace(text) == "" {
		q.toast("no selection")
		return nil
	}
	formatted, err := format.Format(text)
	if err != nil {
		q.toast("format: " + err.Error())
		return nil
	}
	return q.helpers.EditorBuffer.ReplaceSelection(formatted)
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
	// A preempt fence is transient — the prior query is still
	// terminating. Surface a retry toast rather than a sticky error tab.
	if errors.Is(err, session.ErrPreemptPending) {
		q.toast("Previous query is still terminating — please retry in a moment.")
		return
	}

	// Detect connection-dead errors and trigger disconnect flow.
	if drivers.IsConnectionDead(err) {
		q.handleConnectionDead(err)
	}

	if q.helpers.ResultTabs != nil {
		q.helpers.ResultTabs.ShowError(tabLabel(stmt), err)
		// record the full SQL on the now-active error tab so
		// the error panel can draw a position caret under the offending
		// token. ShowError sets the error tab active; the attach is a no-op
		// when the helper doesn't implement the optional surface.
		if attacher, ok := q.helpers.ResultTabs.(ResultTabErrorSQLAttacher); ok {
			attacher.AttachActiveTabErrorSQL(stmt)
		}
		return
	}
	q.toast(err.Error())
}

// handleConnectionDead marks the session disconnected, emits a toast
// (deduplicated — only fires once per session), and logs the event.
func (q *QueryEditorController) handleConnectionDead(err error) {
	runner := q.helpers.QueryRunner
	if runner == nil {
		return
	}
	// Dedup: only fire once. MarkDisconnected returns false if no session
	// is wired. IsDisconnected was already true → skip toast.
	if runner.IsDisconnected() {
		return
	}
	runner.MarkDisconnected()

	// Mark in-flight result tabs as connection-lost so the title reads
	// "(error: connection terminated, N rows received)".
	if marker, ok := q.helpers.ResultTabs.(ResultTabConnectionLostMarker); ok {
		marker.MarkConnectionLost()
	}

	// Toast — redacted via ToastHelper.Show's internal RedactDSN.
	q.toast("connection lost")

	// Log at Debug level through the DebugLogger surface with structured
	// fields (evt, connection_id, err). The DebugLogger interface only
	// exposes Debug; the underlying *slog.Logger interprets the key-value
	// args as structured attrs.
	if q.helpers.Logger != nil {
		connID := ""
		if q.helpers.ActiveConnection != nil {
			connID = q.helpers.ActiveConnection.ActiveConnectionID()
		}
		q.helpers.Logger.Debug("connection_lost",
			"evt", "connection_lost",
			"connection_id", connID,
			"err", session.RedactDSN(err.Error()),
		)
	}
}

func (q *QueryEditorController) openResultTab(stmt string, rh *session.RunHandle) {
	if q.helpers.ResultTabs == nil || rh == nil {
		return
	}
	_ = q.helpers.ResultTabs.OpenResultTab(tabLabel(stmt), rh)
	// record (connID, ResultIdentity) on the now-active
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
	// record the originating statement, its bound args, and
	// the DefaultSchema (search_path) on the now-active result tab so a
	// later sort re-run can reissue the exact query. The editor run path
	// carries no args (runner.Run takes none), so origArgs is nil here;
	// the accessor still round-trips args when a future caller supplies
	// them. DefaultSchema mirrors the value the run path passed to
	// runner.Run via opts.DefaultSchema (Schemas.SelectedSchemaName()).
	// Optional surface — gated like the identity/error-SQL attachers.
	if attacher, ok := q.helpers.ResultTabs.(ResultTabOriginAttacher); ok {
		defaultSchema := ""
		if q.helpers.Schemas != nil {
			defaultSchema = q.helpers.Schemas.SelectedSchemaName()
		}
		attacher.AttachActiveTabOrigin(stmt, nil, defaultSchema)
	}
	// move focus to the results pane now that a tab is open,
	// so the user can navigate results without a manual pane switch. Fires
	// for every run path (run-one / run-all / visual-run) since they all
	// converge here. Nil-safe — unwired in unit tests that don't exercise
	// the focus stack.
	if q.helpers.FocusResults != nil {
		q.logErr("focus_results", q.helpers.FocusResults())
	}
}

func (q *QueryEditorController) openPlanTab(stmt string, plan models.Plan) {
	if q.helpers.ResultTabs == nil {
		return
	}
	_ = q.helpers.ResultTabs.OpenPlanTab(tabLabel(stmt), plan)
	// move focus to the results pane (now the plan tab) so the
	// user can navigate the plan tree without a manual pane switch, matching
	// the grid run path. Nil-safe — unwired in unit tests.
	if q.helpers.FocusResults != nil {
		q.logErr("focus_results", q.helpers.FocusResults())
	}
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
