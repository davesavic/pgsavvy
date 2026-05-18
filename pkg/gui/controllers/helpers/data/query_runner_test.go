package data

import (
	"context"
	"errors"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// fakeRunnerSession records every call and returns canned responses. It does
// NOT produce a real *session.RunHandle — Run / Stream tests rely on a
// nil RunHandle being acceptable up to QueryRunner.last.Store.
type fakeRunnerSession struct {
	execCalls    []models.Query
	streamCalls  []models.Query
	explainCalls []explainCall
	cancelCalls  []models.QueryID

	streamErr  error
	explainErr error
	executeErr error
	cancelErr  error

	inTx bool

	streamRH *session.RunHandle
}

type explainCall struct {
	Q       models.Query
	Analyze bool
}

func (f *fakeRunnerSession) Execute(_ context.Context, q models.Query) (models.Result, error) {
	f.execCalls = append(f.execCalls, q)
	return models.Result{}, f.executeErr
}

func (f *fakeRunnerSession) Stream(_ context.Context, q models.Query) (*session.RunHandle, error) {
	f.streamCalls = append(f.streamCalls, q)
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.streamRH, nil
}

func (f *fakeRunnerSession) Explain(_ context.Context, q models.Query, analyze bool) (models.Plan, error) {
	f.explainCalls = append(f.explainCalls, explainCall{Q: q, Analyze: analyze})
	return models.Plan{RawText: "fake plan"}, f.explainErr
}

func (f *fakeRunnerSession) InTransaction() bool { return f.inTx }

func (f *fakeRunnerSession) Cancel(qid models.QueryID) error {
	f.cancelCalls = append(f.cancelCalls, qid)
	return f.cancelErr
}

func TestQueryRunnerRunDispatchesStream(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
	if err != nil {
		t.Fatalf("Run returned err = %v", err)
	}
	if len(fs.streamCalls) != 1 || fs.streamCalls[0].SQL != "SELECT 1" {
		t.Fatalf("Stream calls = %#v, want one [SELECT 1]", fs.streamCalls)
	}
	if len(fs.execCalls) != 0 {
		t.Fatalf("unexpected Execute calls on a plain Run: %#v", fs.execCalls)
	}
}

func TestQueryRunnerRunWithNewTxPrependsBegin(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{NewTx: true})
	if err != nil {
		t.Fatalf("Run returned err = %v", err)
	}
	if len(fs.execCalls) != 1 || fs.execCalls[0].SQL != "BEGIN" {
		t.Fatalf("Execute calls = %#v, want [BEGIN]", fs.execCalls)
	}
	if len(fs.streamCalls) != 1 || fs.streamCalls[0].SQL != "SELECT 1" {
		t.Fatalf("Stream calls = %#v", fs.streamCalls)
	}
}

func TestQueryRunnerRunNoSession(t *testing.T) {
	r := NewQueryRunner(nil, drivers.Capabilities{})
	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("Run no-session err = %v, want ErrNoSession", err)
	}
}

func TestQueryRunnerRunPropagatesStreamError(t *testing.T) {
	want := errors.New("driver boom")
	fs := &fakeRunnerSession{streamErr: want}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Run(context.Background(), "SELECT 1", RunOptions{})
	if !errors.Is(err, want) {
		t.Fatalf("Run err = %v, want %v", err, want)
	}
}

func TestQueryRunnerExplainPlainPath(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	plan, err := r.Explain(context.Background(), "SELECT 1", false)
	if err != nil {
		t.Fatalf("Explain err = %v", err)
	}
	if plan.RawText != "fake plan" {
		t.Fatalf("plan.RawText = %q, want fake plan", plan.RawText)
	}
	if len(fs.execCalls) != 0 {
		t.Fatalf("plain Explain should not issue BEGIN/ROLLBACK; got %#v", fs.execCalls)
	}
	if len(fs.explainCalls) != 1 || fs.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v, want one with Analyze=false", fs.explainCalls)
	}
}

func TestQueryRunnerExplainAnalyzeWrapsInBeginRollback(t *testing.T) {
	fs := &fakeRunnerSession{} // inTx == false
	r := NewQueryRunner(fs, drivers.Capabilities{})

	plan, err := r.Explain(context.Background(), "INSERT INTO t VALUES (1)", true)
	if err != nil {
		t.Fatalf("Explain err = %v", err)
	}
	if plan.RawText != "fake plan" {
		t.Fatalf("plan.RawText = %q", plan.RawText)
	}
	if len(fs.execCalls) != 2 {
		t.Fatalf("execCalls = %#v, want [BEGIN, ROLLBACK]", fs.execCalls)
	}
	if fs.execCalls[0].SQL != "BEGIN" || fs.execCalls[1].SQL != "ROLLBACK" {
		t.Fatalf("execCalls order = %#v, want [BEGIN, ROLLBACK]", fs.execCalls)
	}
	if len(fs.explainCalls) != 1 || !fs.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v", fs.explainCalls)
	}
}

func TestQueryRunnerExplainAnalyzeInsideTxSkipsWrap(t *testing.T) {
	fs := &fakeRunnerSession{inTx: true}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	if _, err := r.Explain(context.Background(), "INSERT INTO t VALUES (1)", true); err != nil {
		t.Fatalf("Explain err = %v", err)
	}
	if len(fs.execCalls) != 0 {
		t.Fatalf("inside-tx Explain should not wrap; got %#v", fs.execCalls)
	}
	if len(fs.explainCalls) != 1 || !fs.explainCalls[0].Analyze {
		t.Fatalf("explainCalls = %#v", fs.explainCalls)
	}
}

func TestQueryRunnerExplainAnalyzeStillRollsBackOnExplainError(t *testing.T) {
	boom := errors.New("explain boom")
	fs := &fakeRunnerSession{explainErr: boom}
	r := NewQueryRunner(fs, drivers.Capabilities{})

	_, err := r.Explain(context.Background(), "INSERT INTO t VALUES (1)", true)
	if !errors.Is(err, boom) {
		t.Fatalf("Explain err = %v, want %v", err, boom)
	}
	// ROLLBACK must still issue so we don't leak the BEGIN.
	if len(fs.execCalls) != 2 || fs.execCalls[1].SQL != "ROLLBACK" {
		t.Fatalf("execCalls on explain-error = %#v, want [BEGIN, ROLLBACK]", fs.execCalls)
	}
}

func TestQueryRunnerCancelNoOpWhenNoLiveCancel(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{HasLiveCancel: false})

	// Even with a stashed handle, Cancel must short-circuit on caps.
	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel no-live-cancel err = %v", err)
	}
	if len(fs.cancelCalls) != 0 {
		t.Fatalf("cancelCalls = %#v, want none", fs.cancelCalls)
	}
}

func TestQueryRunnerCancelNoOpWhenNoLastHandle(t *testing.T) {
	fs := &fakeRunnerSession{}
	r := NewQueryRunner(fs, drivers.Capabilities{HasLiveCancel: true})

	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel no-handle err = %v", err)
	}
	if len(fs.cancelCalls) != 0 {
		t.Fatalf("cancelCalls = %#v, want none", fs.cancelCalls)
	}
}

func TestQueryRunnerCapabilitiesEcho(t *testing.T) {
	want := drivers.Capabilities{HasLiveCancel: true, HasNotice: true, MaxIdentifierLen: 63}
	r := NewQueryRunner(&fakeRunnerSession{}, want)
	if got := r.Capabilities(); got != want {
		t.Fatalf("Capabilities() = %+v, want %+v", got, want)
	}
}

func TestQueryRunnerHasSession(t *testing.T) {
	if NewQueryRunner(nil, drivers.Capabilities{}).HasSession() {
		t.Fatal("HasSession nil-session = true, want false")
	}
	if !NewQueryRunner(&fakeRunnerSession{}, drivers.Capabilities{}).HasSession() {
		t.Fatal("HasSession with fake = false, want true")
	}
}

func TestNewQueryRunnerForSessionNilSafe(t *testing.T) {
	r := NewQueryRunnerForSession(nil, drivers.Capabilities{HasLiveCancel: true})
	if r.HasSession() {
		t.Fatal("HasSession after nil-session bootstrap = true, want false")
	}
	if r.Capabilities().HasLiveCancel != true {
		t.Fatal("caps were lost across nil-session bootstrap")
	}
}
