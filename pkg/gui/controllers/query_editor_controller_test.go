package controllers_test

import (
	stdcontext "context"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// fakeEditorBuffer is the EditorBufferReader test double; tests set
// Text + Off in the case body and feed the bag into the controller.
// Sel + HasSel mirror Visual-mode selection state for the
// <leader>r-in-Visual fan-out tests; default empty values report "no
// selection" so legacy non-visual tests need no changes.
type fakeEditorBuffer struct {
	Text   string
	Off    int
	Sel    string
	HasSel bool
	// Inserted records every InsertAtCursor argument in call order so
	// T4's history-replay tests can assert what text was inserted.
	Inserted []string
}

func (f *fakeEditorBuffer) BufferText() string              { return f.Text }
func (f *fakeEditorBuffer) CursorOffset() int               { return f.Off }
func (f *fakeEditorBuffer) SelectionText() (string, bool)   { return f.Sel, f.HasSel }
func (f *fakeEditorBuffer) ReplaceAll(_ string) error       { return nil }
func (f *fakeEditorBuffer) ReplaceSelection(_ string) error { return nil }
func (f *fakeEditorBuffer) InsertAtCursor(text string) error {
	f.Inserted = append(f.Inserted, text)
	return nil
}

type fakeResultTabs struct {
	resultCalls []resultCall
	planCalls   []planCall
	errorCalls  []errorCall
	resultErr   error
	planErr     error
}

type resultCall struct {
	Label string
	RH    *session.RunHandle
}
type planCall struct {
	Label string
	Plan  models.Plan
}
type errorCall struct {
	Label string
	Err   error
}

func (f *fakeResultTabs) OpenResultTab(label string, rh *session.RunHandle) error {
	f.resultCalls = append(f.resultCalls, resultCall{label, rh})
	return f.resultErr
}

func (f *fakeResultTabs) OpenPlanTab(label string, plan models.Plan) error {
	f.planCalls = append(f.planCalls, planCall{label, plan})
	return f.planErr
}

func (f *fakeResultTabs) ShowError(label string, err error) {
	f.errorCalls = append(f.errorCalls, errorCall{label, err})
}

// queryBag extends the base test bag with the query-editor wiring.
type queryBag struct {
	*bag
	Tabs    *fakeResultTabs
	Buffer  *fakeEditorBuffer
	Runner  *data.QueryRunner
	NoCaps  bool // set true to leave caps.HasLiveCancel=false
	HasCaps bool
}

func newQueryBag(t *testing.T, caps drivers.Capabilities) *queryBag {
	t.Helper()
	base := newBag()
	tabs := &fakeResultTabs{}
	buf := &fakeEditorBuffer{}
	// nil session is fine — controller checks HasSession() before
	// dispatching. Tests that need a real runner exercise that surface.
	runner := data.NewQueryRunner(nil, caps)

	base.HelperBag.ResultTabs = tabs
	base.HelperBag.EditorBuffer = buf
	base.HelperBag.QueryRunner = runner

	return &queryBag{bag: base, Tabs: tabs, Buffer: buf, Runner: runner}
}

func TestQueryEditorControllerPublishesSixBindings(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	wantActions := map[string]bool{
		commands.QueryRun:            false,
		commands.QueryRunAll:         false,
		commands.QueryExplain:        false,
		commands.QueryExplainAnalyze: false,
		commands.QueryCancel:         false,
		commands.QueryRunInNewTx:     false,
	}
	for _, kb := range kbs {
		if _, ok := wantActions[kb.ActionID]; ok {
			wantActions[kb.ActionID] = true
		}
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("kb %s scope = %s, want QUERY_EDITOR", kb.ActionID, kb.Scope)
		}
	}
	for id, seen := range wantActions {
		if !seen {
			t.Errorf("action %q not published in QueryEditor bindings", id)
		}
	}
}

// Leader chords MUST NOT be active in INSERT mode in the
// ResultTabsController either; <leader>1..9 / <leader>X / <leader>= /
// <leader>x / <leader>s / <leader>gH leak into the editor's INSERT
// mode via the (mode, GLOBAL) trie lookup performed by the matcher's
// fast-path passthrough gate, so any INSERT-mode mask on these
// bindings re-triggers the "select*" reordering bug from a different
// scope.
func TestResultTabsControllerLeaderBindingsExcludeInsertMode(t *testing.T) {
	ctrl := controllers.NewResultTabsController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})
	leaderCount := 0
	for _, kb := range kbs {
		if len(kb.Sequence) == 0 {
			continue
		}
		first := kb.Sequence[0]
		if first.Special != types.KeyLeader && first.Special != types.KeyLocalLeader {
			continue
		}
		leaderCount++
		if kb.Mode&types.ModeInsert != 0 {
			t.Errorf("leader binding %q has ModeInsert set in Mode mask = %b; INSERT must be cleared to keep <space> literal in INSERT mode", kb.ActionID, kb.Mode)
		}
	}
	if leaderCount == 0 {
		t.Fatalf("expected at least one leader-prefixed binding from ResultTabsController, got 0")
	}
}

// Leader chords MUST NOT be active in INSERT mode. When
// <leader> is <space>, an INSERT-mode mask makes the space rune a
// chord-prefix and the matcher buffers it until tlen expires, producing
// the user-visible "select*" reordering bug. Regression guard: every
// binding whose first key is KeyLeader (or KeyLocalLeader) must have
// ModeInsert cleared from its Mode mask.
func TestQueryEditorControllerLeaderBindingsExcludeInsertMode(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})
	leaderCount := 0
	for _, kb := range kbs {
		if len(kb.Sequence) == 0 {
			continue
		}
		first := kb.Sequence[0]
		if first.Special != types.KeyLeader && first.Special != types.KeyLocalLeader {
			continue
		}
		leaderCount++
		if kb.Mode&types.ModeInsert != 0 {
			t.Errorf("leader binding %q has ModeInsert set in Mode mask = %b; INSERT must be cleared to keep <space> literal in INSERT mode", kb.ActionID, kb.Mode)
		}
	}
	if leaderCount == 0 {
		t.Fatalf("expected at least one leader-prefixed binding from QueryEditorController, got 0")
	}
}

// <leader>r must fire in Normal mode. ModeNormal is the
// zero sentinel (types/mode.go), so it CANNOT be OR'd into a multi-mode
// mask without vanishing (0 | X == X). A run binding built as
// `ModeNormal | ModeVisual | ...` therefore fans out to the Visual modes
// ONLY, leaving <leader>r dead in Normal mode — the mode you are in when
// you run a query — so no statement executes and no result tab ever
// opens. Guard: GetKeybindings must publish a QueryRun binding that
// fires in Normal mode AND one that fires in Visual mode (the
// visual-selection fan-out must be preserved).
func TestQueryEditorControllerRunBindingFiresInNormalMode(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})

	normalRun := false
	visualRun := false
	for _, kb := range kbs {
		if kb.ActionID != commands.QueryRun {
			continue
		}
		if kb.Mode.Is(types.ModeNormal) {
			normalRun = true
		}
		if kb.Mode&(types.ModeVisual|types.ModeVisualLine|types.ModeVisualBlock) != 0 {
			visualRun = true
		}
	}
	if !normalRun {
		t.Errorf("no QueryRun binding fires in Normal mode; <leader>r is dead where queries are run")
	}
	if !visualRun {
		t.Errorf("no QueryRun binding fires in Visual mode; visual-selection run regressed")
	}
}

func TestQueryEditorControllerRegisterActionsRegistersAllSix(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, id := range []string{
		commands.QueryRun,
		commands.QueryRunAll,
		commands.QueryExplain,
		commands.QueryExplainAnalyze,
		commands.QueryCancel,
		commands.QueryRunInNewTx,
	} {
		cmd, ok := reg.Get(id)
		if !ok || cmd == nil {
			t.Fatalf("action %q not registered", id)
		}
		if cmd.Handler == nil {
			t.Fatalf("action %q has nil Handler", id)
		}
	}
}

func TestQueryEditorCancelDisabledWhenDriverLacksLiveCancel(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: false})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.QueryCancel)
	if !ok {
		t.Fatal("QueryCancel not registered")
	}
	reason, disabled := cmd.Disabled(commands.ExecCtx{})
	if !disabled {
		t.Fatal("QueryCancel should be disabled when caps.HasLiveCancel=false")
	}
	want := i18n.EnglishTranslationSet().DisabledNoLiveCancel
	if reason != want {
		t.Fatalf("QueryCancel disabled reason = %q, want %q", reason, want)
	}
}

func TestQueryEditorCancelEnabledWhenDriverSupportsLiveCancel(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryCancel)
	if _, disabled := cmd.Disabled(commands.ExecCtx{}); disabled {
		t.Fatal("QueryCancel should be enabled when caps.HasLiveCancel=true")
	}
}

// TestQueryEditorController_CancelDisabled_RecomputesAfterBind locks in
// the contract that the <leader>x disabled state must be evaluated against
// the LIVE QueryRunner.Capabilities() at dispatch time, not captured at
// RegisterActions time. Production bootstrap wires the runner with
// caps={} (HasLiveCancel=false) before Connect; once Bind() lands the
// real driver caps, the same registered Command must flip to enabled
// without re-registration.
func TestQueryEditorController_CancelDisabled_RecomputesAfterBind(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: false})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.QueryCancel)
	if !ok || cmd == nil {
		t.Fatal("QueryCancel not registered")
	}

	// Baseline: bootstrap runner reports HasLiveCancel=false; cancel
	// should be disabled with the localised reason.
	want := i18n.EnglishTranslationSet().DisabledNoLiveCancel
	reason, disabled := cmd.Disabled(commands.ExecCtx{Scope: types.QUERY_EDITOR})
	if !disabled {
		t.Fatalf("pre-Bind: cancel should be disabled when caps.HasLiveCancel=false")
	}
	if reason != want {
		t.Fatalf("pre-Bind: reason = %q, want %q", reason, want)
	}

	// Simulate the post-Connect Bind that publishes real driver caps.
	b.Runner.Bind(nil, drivers.Capabilities{HasLiveCancel: true})

	// Same Command pointer (no re-registration): must now report
	// enabled and an empty reason.
	cmd2, _ := reg.Get(commands.QueryCancel)
	if cmd2 != cmd {
		t.Fatalf("registry returned a different Command after Bind; pre=%p post=%p", cmd, cmd2)
	}
	reason, disabled = cmd2.Disabled(commands.ExecCtx{Scope: types.QUERY_EDITOR})
	if disabled {
		t.Fatalf("post-Bind: cancel should be enabled when caps.HasLiveCancel=true; got disabled with reason %q", reason)
	}
	if reason != "" {
		t.Fatalf("post-Bind: reason = %q, want empty", reason)
	}
}

func TestQueryEditorHandleCancelToastsWhenDisabled(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: false})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryCancel)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Cancel handler err = %v", err)
	}
	if len(b.Toast.msgs) != 1 {
		t.Fatalf("Toast msgs = %#v, want one disabled-reason message", b.Toast.msgs)
	}
	want := i18n.EnglishTranslationSet().DisabledNoLiveCancel
	if !strings.Contains(b.Toast.msgs[0].Msg, want) {
		t.Fatalf("toast = %q, want substring %q", b.Toast.msgs[0].Msg, want)
	}
}

func TestQueryEditorRunWithEmptyBufferToasts(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	b.Buffer.Text = ""
	b.Buffer.Off = 0
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if len(b.Toast.msgs) != 1 {
		t.Fatalf("Toast msgs = %#v, want one no-statement message", b.Toast.msgs)
	}
	if !strings.Contains(b.Toast.msgs[0].Msg, "no statement") {
		t.Fatalf("toast = %q, want 'no statement' substring", b.Toast.msgs[0].Msg)
	}
}

func TestQueryEditorRunAllWithOnlySemicolonsToasts(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	b.Buffer.Text = "  ;  ;"
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRunAll)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunAll handler err = %v", err)
	}
	if len(b.Toast.msgs) != 1 || !strings.Contains(b.Toast.msgs[0].Msg, "no statements found") {
		t.Fatalf("Toast = %#v, want 'no statements found'", b.Toast.msgs)
	}
}

func TestQueryEditorRunNoSessionToasts(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	b.Buffer.Text = "SELECT 1;"
	b.Buffer.Off = 3 // cursor inside "SELECT 1"
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	// Runner was constructed with nil session in newQueryBag, so
	// HasSession() returns false — the no-active-connection toast
	// should fire.
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if len(b.Toast.msgs) != 1 || !strings.Contains(b.Toast.msgs[0].Msg, "no active connection") {
		t.Fatalf("Toast = %#v, want 'no active connection'", b.Toast.msgs)
	}
	if len(b.Tabs.resultCalls) != 0 {
		t.Fatalf("ResultTabs.OpenResultTab was called %d times, want 0", len(b.Tabs.resultCalls))
	}
}

func TestQueryEditorAttachToContextNilSafe(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	// Must not panic.
	ctrl.AttachToContext(nil)
}

// recordingRunnerSession satisfies data.RunnerSession and records every
// call so tests can assert the controller routes the right SQL.
type recordingRunnerSession struct {
	streamCalls  []models.Query
	execCalls    []models.Query
	explainCalls []struct {
		Q       models.Query
		Analyze bool
	}
	explainNotice string
	cancelCalls   []models.QueryID
	beginCalls    int
	inTx          bool
	lastTx        *recordingTransaction
	streamErr     error
}

// recordingTransaction is a minimal drivers.Transaction stub.
type recordingTransaction struct {
	rolledBack bool
}

func (t *recordingTransaction) Commit(_ stdcontext.Context) error               { return nil }
func (t *recordingTransaction) Rollback(_ stdcontext.Context) error             { t.rolledBack = true; return nil }
func (t *recordingTransaction) Savepoint(_ stdcontext.Context, _ string) error  { return nil }
func (t *recordingTransaction) Release(_ stdcontext.Context, _ string) error    { return nil }
func (t *recordingTransaction) RollbackTo(_ stdcontext.Context, _ string) error { return nil }
func (t *recordingTransaction) Savepoints() []string                            { return nil }
func (t *recordingTransaction) Status() models.TxStatus                         { return models.TxActive }
func (t *recordingTransaction) ObserveError(_ error)                            {}
func (t *recordingTransaction) StatementCount() int                             { return 0 }

func (r *recordingRunnerSession) Execute(_ stdcontext.Context, q models.Query) (models.Result, error) {
	r.execCalls = append(r.execCalls, q)
	return models.Result{}, nil
}

func (r *recordingRunnerSession) Stream(_ stdcontext.Context, q models.Query) (*session.RunHandle, error) {
	r.streamCalls = append(r.streamCalls, q)
	return nil, r.streamErr
}

func (r *recordingRunnerSession) Explain(_ stdcontext.Context, q models.Query, analyze bool) (models.Plan, error) {
	r.explainCalls = append(r.explainCalls, struct {
		Q       models.Query
		Analyze bool
	}{q, analyze})
	return models.Plan{Notice: r.explainNotice}, nil
}

func (r *recordingRunnerSession) Begin(_ stdcontext.Context, _ models.TxOptions) (drivers.Transaction, error) {
	r.beginCalls++
	r.inTx = true
	r.lastTx = &recordingTransaction{}
	return r.lastTx, nil
}

func (r *recordingRunnerSession) InTransaction() bool { return r.inTx }

func (r *recordingRunnerSession) CurrentTransaction() drivers.Transaction {
	if r.lastTx == nil {
		return nil
	}
	return r.lastTx
}

func (r *recordingRunnerSession) Cancel(qid models.QueryID) error {
	r.cancelCalls = append(r.cancelCalls, qid)
	return nil
}

func (r *recordingRunnerSession) SetDisconnected(_ bool) {}
func (r *recordingRunnerSession) IsDisconnected() bool   { return false }
func (r *recordingRunnerSession) MarkPreemptPending()    {}

// TestQueryEditorRunOnSecondLineRoutesCorrectStatement covers the AC:
//
//	Given QueryEditor buffer "SELECT 1;\nSELECT 2;" with cursor on line 2
//	When the user presses <leader>r
//	Then QueryRunner is invoked with statement == "SELECT 2"
func TestQueryEditorRunOnSecondLineRoutesCorrectStatement(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	buf := &fakeEditorBuffer{
		Text: "SELECT 1;\nSELECT 2;",
		Off:  10, // 'S' of "SELECT 2"
	}
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = buf
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("streamCalls = %#v, want one", rec.streamCalls)
	}
	if rec.streamCalls[0].SQL != "SELECT 2" {
		t.Fatalf("dispatched SQL = %q, want %q", rec.streamCalls[0].SQL, "SELECT 2")
	}
}

// runEditorWith builds a query-editor controller over a recording runner
// session with the given buffer text and connection profile, drives the
// QueryRun action, and returns the recorder + fake confirm for assertions.
func runEditorWith(t *testing.T, bufText string, conn *models.Connection) (*recordingRunnerSession, *fakeConfirm) {
	t.Helper()
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: bufText, Off: 0}
	base.HelperBag.ResultTabs = &fakeResultTabs{}
	base.HelperBag.ConnProfile = func() *models.Connection { return conn }

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	return rec, base.Confirm
}

// TestQueryEditorConfirmWritesGatesUpdate verifies that with ConfirmWrites
// enabled an UPDATE is NOT executed until the confirmation popup's onYes
// fires.
func TestQueryEditorConfirmWritesGatesUpdate(t *testing.T) {
	rec, confirm := runEditorWith(t, "UPDATE t SET a=1", &models.Connection{ConfirmWrites: true})

	if len(rec.streamCalls) != 0 {
		t.Fatalf("statement executed before confirmation: %#v", rec.streamCalls)
	}
	if len(confirm.calls) != 1 {
		t.Fatalf("confirm popup calls = %d, want 1", len(confirm.calls))
	}
	if confirm.calls[0].OnYes == nil {
		t.Fatal("confirm onYes is nil")
	}
	if err := confirm.calls[0].OnYes(); err != nil {
		t.Fatalf("onYes err = %v", err)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("after confirm streamCalls = %d, want 1", len(rec.streamCalls))
	}
	if rec.streamCalls[0].SQL != "UPDATE t SET a=1" {
		t.Fatalf("dispatched SQL = %q", rec.streamCalls[0].SQL)
	}
}

// TestQueryEditorConfirmWritesSkipsSelect verifies a read-only statement
// runs immediately even with ConfirmWrites enabled.
func TestQueryEditorConfirmWritesSkipsSelect(t *testing.T) {
	rec, confirm := runEditorWith(t, "SELECT 1", &models.Connection{ConfirmWrites: true})

	if len(confirm.calls) != 0 {
		t.Fatalf("SELECT triggered a confirmation popup: %#v", confirm.calls)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("SELECT not executed: streamCalls = %d, want 1", len(rec.streamCalls))
	}
}

// TestQueryEditorConfirmWritesOffRunsUpdate verifies an UPDATE runs without
// a prompt when the connection has ConfirmWrites disabled.
func TestQueryEditorConfirmWritesOffRunsUpdate(t *testing.T) {
	rec, confirm := runEditorWith(t, "UPDATE t SET a=1", &models.Connection{ConfirmWrites: false})

	if len(confirm.calls) != 0 {
		t.Fatalf("UPDATE prompted despite ConfirmWrites=false: %#v", confirm.calls)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("UPDATE not executed: streamCalls = %d, want 1", len(rec.streamCalls))
	}
}

// TestQueryEditorConfirmDDLGatesCreate verifies ConfirmDDL gates a DDL
// statement but ConfirmWrites alone does not.
func TestQueryEditorConfirmDDLGatesCreate(t *testing.T) {
	rec, confirm := runEditorWith(t, "CREATE TABLE t (id int)", &models.Connection{ConfirmDDL: true})
	if len(confirm.calls) != 1 {
		t.Fatalf("DDL with ConfirmDDL did not prompt: calls=%d", len(confirm.calls))
	}
	if len(rec.streamCalls) != 0 {
		t.Fatalf("DDL executed before confirmation: %#v", rec.streamCalls)
	}

	rec2, confirm2 := runEditorWith(t, "CREATE TABLE t (id int)", &models.Connection{ConfirmWrites: true})
	if len(confirm2.calls) != 0 {
		t.Fatalf("DDL prompted under ConfirmWrites-only: %#v", confirm2.calls)
	}
	if len(rec2.streamCalls) != 1 {
		t.Fatalf("DDL not executed under ConfirmWrites-only: %d", len(rec2.streamCalls))
	}
}

// TestQueryEditorRunAppliesConfigDefaultTimeout covers the case where,
// when query.default_statement_timeout is non-zero, the streaming
// run path must apply it as the streamed Query.Timeout so the pg Stream
// derives a deadline (context.WithTimeout) and bounds a runaway query.
func TestQueryEditorRunAppliesConfigDefaultTimeout(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	c := common.NewDummyCommon()
	c.Cfg().Query.DefaultStatementTimeout = 2 * time.Second

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(c, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("streamCalls len = %d, want 1", len(rec.streamCalls))
	}
	if rec.streamCalls[0].Timeout != 2*time.Second {
		t.Fatalf("streamed Query.Timeout = %v, want 2s (config default applied)", rec.streamCalls[0].Timeout)
	}
}

// TestQueryEditorRunNoTimeoutWhenConfigOff covers the 0=off sentinel:
// with query.default_statement_timeout == 0 (the documented default), the
// streamed Query.Timeout stays 0 — no ceiling.
func TestQueryEditorRunNoTimeoutWhenConfigOff(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	c := common.NewDummyCommon() // GetDefaultConfig → DefaultStatementTimeout == 0

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(c, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("streamCalls len = %d, want 1", len(rec.streamCalls))
	}
	if rec.streamCalls[0].Timeout != 0 {
		t.Fatalf("streamed Query.Timeout = %v, want 0 (off)", rec.streamCalls[0].Timeout)
	}
}

// TestQueryEditorRunNilCommonNoTimeout guards the test-path / pre-config
// case: a nil Common must not panic and yields Query.Timeout == 0 (off).
func TestQueryEditorRunNilCommonNoTimeout(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("streamCalls len = %d, want 1", len(rec.streamCalls))
	}
	if rec.streamCalls[0].Timeout != 0 {
		t.Fatalf("streamed Query.Timeout = %v, want 0 with nil Common", rec.streamCalls[0].Timeout)
	}
}

func TestQueryEditorRunAllDispatchesEverySegmentSequentially(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1; SELECT 2; SELECT 3;"}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRunAll)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunAll handler err = %v", err)
	}
	if got := len(rec.streamCalls); got != 3 {
		t.Fatalf("streamCalls len = %d, want 3", got)
	}
	wantSQL := []string{"SELECT 1", "SELECT 2", "SELECT 3"}
	for i, want := range wantSQL {
		if rec.streamCalls[i].SQL != want {
			t.Fatalf("streamCalls[%d].SQL = %q, want %q", i, rec.streamCalls[i].SQL, want)
		}
	}
}

// TestQueryEditorRunSQLNoSessionReturnsFalse covers the TABLES <CR>
// "open table data" reuse path: with no active session,
// RunSQL toasts, opens no tab, and returns false so the caller skips
// focusing an empty results panel.
func TestQueryEditorRunSQLNoSessionReturnsFalse(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)

	if ctrl.RunSQL("SELECT 1") {
		t.Fatal("RunSQL returned true with no active session, want false")
	}
	if len(b.Toast.msgs) != 1 || !strings.Contains(b.Toast.msgs[0].Msg, "no active connection") {
		t.Fatalf("Toast = %#v, want 'no active connection'", b.Toast.msgs)
	}
	if len(b.Tabs.resultCalls) != 0 {
		t.Fatalf("OpenResultTab called %d times, want 0", len(b.Tabs.resultCalls))
	}
}

// TestQueryEditorRunSQLBlankReturnsFalse: a blank/whitespace statement
// short-circuits before touching the runner.
func TestQueryEditorRunSQLBlankReturnsFalse(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)

	if ctrl.RunSQL("   ") {
		t.Fatal("RunSQL returned true for a blank statement, want false")
	}
	if len(b.Toast.msgs) != 0 {
		t.Fatalf("Toast = %#v, want none for blank statement", b.Toast.msgs)
	}
}

// TestQueryEditorRunSQLDispatchesStatement: RunSQL routes the supplied
// SQL verbatim through the runner and returns true, without reading the
// editor buffer (none is wired here) — the property the TABLES <CR>
// path depends on.
func TestQueryEditorRunSQLDispatchesStatement(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)

	const sql = `SELECT * FROM "public"."users" LIMIT 100`
	if !ctrl.RunSQL(sql) {
		t.Fatal("RunSQL returned false with an active session, want true")
	}
	if len(rec.streamCalls) != 1 {
		t.Fatalf("streamCalls = %#v, want one", rec.streamCalls)
	}
	if rec.streamCalls[0].SQL != sql {
		t.Fatalf("dispatched SQL = %q, want %q", rec.streamCalls[0].SQL, sql)
	}
}

// TestSurfaceErrPreemptPendingShowsToastNotErrorTab is the AD4 UI
// guard: a session refusing a query because a prior one is still terminating
// (ErrPreemptPending) must surface as a transient toast, NOT a sticky error
// tab — the prior query failed nothing, it is merely still winding down.
func TestSurfaceErrPreemptPendingShowsToastNotErrorTab(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	rec := &recordingRunnerSession{streamErr: session.ErrPreemptPending}
	b.HelperBag.QueryRunner = data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})
	b.Buffer.Text = "SELECT 1"
	b.HelperBag.ConnProfile = func() *models.Connection { return &models.Connection{} }

	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}

	if len(b.Tabs.errorCalls) != 0 {
		t.Fatalf("ErrPreemptPending opened %d error tab(s), want 0 (should be a transient toast)", len(b.Tabs.errorCalls))
	}
	if len(b.Toast.msgs) != 1 {
		t.Fatalf("Toast msgs = %#v, want exactly one preempt-pending toast", b.Toast.msgs)
	}
}

// preemptRecorder records, at each PreemptInFlight call, how many
// streams the runner session has issued so far — a witness for ordering.
// Wired to the runner via SetPreempter (preemption now
// lives in the QueryRunner chokepoint, not behind the controller's
// ResultTabs surface).
type preemptRecorder struct {
	rec               *recordingRunnerSession
	preemptStreamLens []int
}

func (p *preemptRecorder) PreemptInFlight() bool {
	p.preemptStreamLens = append(p.preemptStreamLens, len(p.rec.streamCalls))
	return false
}

// TestQueryEditorRunAllPreemptsBeforeEachStream is the deadlock-fix wiring
// guard (now centralized): each statement
// must preempt any in-flight stream BEFORE it issues its own Stream, so a
// >200-row prior run can't leave the per-session queue lock held and freeze
// the UI. The witness is the stream count observed at each preempt: [0, 1]
// means preempt#1 ran before any Stream and preempt#2 ran after exactly
// statement 1 had streamed.
func TestQueryEditorRunAllPreemptsBeforeEachStream(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	tabs := &preemptRecorder{rec: rec}
	runner.SetPreempter(tabs.PreemptInFlight)
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1; SELECT 2;"}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRunAll)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunAll handler err = %v", err)
	}

	if got := len(rec.streamCalls); got != 2 {
		t.Fatalf("streamCalls len = %d, want 2", got)
	}
	want := []int{0, 1}
	if len(tabs.preemptStreamLens) != len(want) {
		t.Fatalf("preempt count = %d, want %d (once per statement, before its Stream)", len(tabs.preemptStreamLens), len(want))
	}
	for i, w := range want {
		if tabs.preemptStreamLens[i] != w {
			t.Errorf("preempt[%d] saw %d prior streams, want %d (preempt must precede its statement's Stream)", i, tabs.preemptStreamLens[i], w)
		}
	}
}

func TestQueryEditorRunInNewTxIssuesBeginBeforeStream(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRunInNewTx)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunInNewTx handler err = %v", err)
	}
	if rec.beginCalls != 1 {
		t.Fatalf("beginCalls = %d, want 1", rec.beginCalls)
	}
	if len(rec.streamCalls) != 1 || rec.streamCalls[0].SQL != "SELECT 1" {
		t.Fatalf("streamCalls = %#v, want one SELECT 1", rec.streamCalls)
	}
}

func TestQueryEditorExplainAnalyzeWrapsInBeginRollback(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	// SELECT (not a write) so the fail-closed EffectiveAnalyze gate keeps
	// analyze=true; this test verifies the QueryRunner Begin/Rollback wrap,
	// not the gate. A write statement would (correctly) be downgraded to
	// estimate-only and never reach Begin.
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT * FROM t;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryExplainAnalyze)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ExplainAnalyze handler err = %v", err)
	}
	// QueryRunner wraps Explain(analyze=true) in Begin/Rollback when
	// no tx is active. Verify via the Transaction API.
	if rec.beginCalls != 1 {
		t.Fatalf("beginCalls = %d, want 1", rec.beginCalls)
	}
	if rec.lastTx == nil || !rec.lastTx.rolledBack {
		t.Fatal("ExplainAnalyze must rollback the transaction")
	}
	if len(rec.explainCalls) != 1 || !rec.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v, want one analyze", rec.explainCalls)
	}
}

// TestQueryEditorExplainNoticeToasts asserts that when the driver falls back to
// a bare EXPLAIN it surfaces the degraded-mode notice via a toast (T1: the
// EXPLAIN option-unsupported fallback must be user-visible, not silent).
func TestQueryEditorExplainNoticeToasts(t *testing.T) {
	const notice = "EXPLAIN options unsupported by server; showing basic plan"
	rec := &recordingRunnerSession{explainNotice: notice}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryExplain)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Explain handler err = %v", err)
	}
	if len(base.Toast.msgs) != 1 || base.Toast.msgs[0].Msg != notice {
		t.Fatalf("Toast msgs = %#v, want one degraded-notice toast %q", base.Toast.msgs, notice)
	}
}

// TestQueryEditorVisualRunFansOutEachStatement asserts the
// contract: when <leader>r fires in Visual mode, the controller reads
// SelectionText, splits on ';' via SplitStatements, and calls runner.Run
// once per non-empty statement (under the cap).
func TestQueryEditorVisualRunFansOutEachStatement(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{
		Text:   "SELECT 1; SELECT 2; SELECT 3;",
		Sel:    "SELECT 1; SELECT 2;",
		HasSel: true,
	}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if got := len(rec.streamCalls); got != 2 {
		t.Fatalf("streamCalls len = %d, want 2 (visual fan-out)", got)
	}
	wantSQL := []string{"SELECT 1", "SELECT 2"}
	for i, want := range wantSQL {
		if rec.streamCalls[i].SQL != want {
			t.Fatalf("streamCalls[%d].SQL = %q, want %q", i, rec.streamCalls[i].SQL, want)
		}
	}
}

// TestQueryEditorVisualRunOverCapAbortsBeforeAnyRun asserts the
// hard cap: a Visual selection that splits into >maxVisualRunBatch
// non-empty statements toasts and does NOT invoke runner.Run at all
// (no partial run).
func TestQueryEditorVisualRunOverCapAbortsBeforeAnyRun(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	// 33 statements (one over the 32 cap).
	var sb strings.Builder
	for range 33 {
		sb.WriteString("SELECT 1;")
	}
	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{
		Text:   sb.String(),
		Sel:    sb.String(),
		HasSel: true,
	}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if got := len(rec.streamCalls); got != 0 {
		t.Fatalf("streamCalls len = %d, want 0 (over-cap should abort)", got)
	}
	if len(base.Toast.msgs) != 1 || !strings.Contains(base.Toast.msgs[0].Msg, "exceeds cap") {
		t.Fatalf("Toast = %#v, want one 'exceeds cap' message", base.Toast.msgs)
	}
}

// TestQueryEditorVisualRunEmptySelectionToasts covers the empty-selection
// path: a Visual mode dispatch with no live selection or whitespace-only
// selection emits the "no selection" toast and does NOT invoke runner.
func TestQueryEditorVisualRunEmptySelectionToasts(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{
		Text:   "SELECT 1;",
		Sel:    "   ",
		HasSel: true,
	}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRun)
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeVisual}); err != nil {
		t.Fatalf("Run handler err = %v", err)
	}
	if got := len(rec.streamCalls); got != 0 {
		t.Fatalf("streamCalls len = %d, want 0", got)
	}
	if len(base.Toast.msgs) != 1 || !strings.Contains(base.Toast.msgs[0].Msg, "no selection") {
		t.Fatalf("Toast = %#v, want one 'no selection' message", base.Toast.msgs)
	}
}

// TestQueryEditorAllActionsAppearInAllActionIDs ensures every new
// action constant is appended to commands.AllActionIDs().
func TestQueryEditorAllActionsAppearInAllActionIDs(t *testing.T) {
	all := map[string]bool{}
	for _, id := range commands.AllActionIDs() {
		all[id] = true
	}
	for _, want := range []string{
		commands.QueryRun,
		commands.QueryRunAll,
		commands.QueryExplain,
		commands.QueryExplainAnalyze,
		commands.QueryCancel,
		commands.QueryRunInNewTx,
	} {
		if !all[want] {
			t.Errorf("AllActionIDs() missing %q", want)
		}
	}
}
