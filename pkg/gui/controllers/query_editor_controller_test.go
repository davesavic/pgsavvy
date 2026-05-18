package controllers_test

import (
	stdcontext "context"
	"strings"
	"testing"

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
type fakeEditorBuffer struct {
	Text string
	Off  int
}

func (f *fakeEditorBuffer) BufferText() string { return f.Text }
func (f *fakeEditorBuffer) CursorOffset() int  { return f.Off }

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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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

func TestQueryEditorControllerRegisterActionsRegistersAllSix(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: true})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryCancel)
	if _, disabled := cmd.Disabled(commands.ExecCtx{}); disabled {
		t.Fatal("QueryCancel should be enabled when caps.HasLiveCancel=true")
	}
}

// TestQueryEditorController_CancelDisabled_RecomputesAfterBind locks in
// dbsavvy-3tt: the <leader>x disabled state must be evaluated against
// the LIVE QueryRunner.Capabilities() at dispatch time, not captured at
// RegisterActions time. Production bootstrap wires the runner with
// caps={} (HasLiveCancel=false) before Connect; once Bind() lands the
// real driver caps, the same registered Command must flip to enabled
// without re-registration.
func TestQueryEditorController_CancelDisabled_RecomputesAfterBind(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{HasLiveCancel: false})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag)
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
	cancelCalls []models.QueryID
	inTx        bool
}

func (r *recordingRunnerSession) Execute(_ stdcontext.Context, q models.Query) (models.Result, error) {
	r.execCalls = append(r.execCalls, q)
	return models.Result{}, nil
}

func (r *recordingRunnerSession) Stream(_ stdcontext.Context, q models.Query) (*session.RunHandle, error) {
	r.streamCalls = append(r.streamCalls, q)
	return nil, nil
}

func (r *recordingRunnerSession) Explain(_ stdcontext.Context, q models.Query, analyze bool) (models.Plan, error) {
	r.explainCalls = append(r.explainCalls, struct {
		Q       models.Query
		Analyze bool
	}{q, analyze})
	return models.Plan{}, nil
}

func (r *recordingRunnerSession) InTransaction() bool { return r.inTx }

func (r *recordingRunnerSession) Cancel(qid models.QueryID) error {
	r.cancelCalls = append(r.cancelCalls, qid)
	return nil
}

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

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag)
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

func TestQueryEditorRunAllDispatchesEverySegmentSequentially(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1; SELECT 2; SELECT 3;"}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag)
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

func TestQueryEditorRunInNewTxIssuesBeginBeforeStream(t *testing.T) {
	rec := &recordingRunnerSession{}
	runner := data.NewQueryRunner(rec, drivers.Capabilities{HasLiveCancel: true})

	base := newBag()
	base.HelperBag.QueryRunner = runner
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryRunInNewTx)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("RunInNewTx handler err = %v", err)
	}
	if len(rec.execCalls) != 1 || rec.execCalls[0].SQL != "BEGIN" {
		t.Fatalf("execCalls = %#v, want one BEGIN", rec.execCalls)
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
	base.HelperBag.EditorBuffer = &fakeEditorBuffer{Text: "INSERT INTO t VALUES (1);", Off: 3}
	base.HelperBag.ResultTabs = &fakeResultTabs{}

	ctrl := controllers.NewQueryEditorController(nil, base.HelperBag)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.QueryExplainAnalyze)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ExplainAnalyze handler err = %v", err)
	}
	// QueryRunner wraps Explain(analyze=true) in BEGIN/ROLLBACK when
	// no tx is active. Verify both surfaces in declaration order.
	if len(rec.execCalls) != 2 {
		t.Fatalf("execCalls = %#v, want [BEGIN, ROLLBACK]", rec.execCalls)
	}
	if rec.execCalls[0].SQL != "BEGIN" || rec.execCalls[1].SQL != "ROLLBACK" {
		t.Fatalf("execCalls = %#v, want BEGIN then ROLLBACK", rec.execCalls)
	}
	if len(rec.explainCalls) != 1 || !rec.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v, want one analyze", rec.explainCalls)
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
