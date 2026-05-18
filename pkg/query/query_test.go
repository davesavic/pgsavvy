package query

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

func TestExecute_FluentComposeRecordsQueryShape(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "SELECT $1").
		Bind(42).
		WithTimeout(time.Second).
		Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: unexpected error %v", err)
	}

	got := fe.lastExecute()
	if got.Query.SQL != "SELECT $1" {
		t.Errorf("SQL = %q; want SELECT $1", got.Query.SQL)
	}
	if !reflect.DeepEqual(got.Query.Args, []any{42}) {
		t.Errorf("Args = %v; want [42]", got.Query.Args)
	}
	if got.Query.Timeout != time.Second {
		t.Errorf("Timeout = %v; want 1s", got.Query.Timeout)
	}
	if got.Suppressed {
		t.Errorf("LoggingSuppressed = true; want false (default)")
	}
}

func TestQueryObj_ImmutabilityAcrossDerivations(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	base := newWith(fe, "SELECT 1")

	a := base.WithTimeout(2 * time.Second)
	b := base.WithTimeout(5 * time.Second)

	// Base must remain untouched.
	if base.timeout != 0 {
		t.Errorf("base.timeout mutated: got %v want 0", base.timeout)
	}
	if a.timeout != 2*time.Second {
		t.Errorf("a.timeout = %v; want 2s", a.timeout)
	}
	if b.timeout != 5*time.Second {
		t.Errorf("b.timeout = %v; want 5s", b.timeout)
	}

	// Dispatch each child and confirm Timeout matches the child's value.
	if _, err := a.Execute(context.Background()); err != nil {
		t.Fatalf("a.Execute: %v", err)
	}
	if got := fe.lastExecute().Query.Timeout; got != 2*time.Second {
		t.Errorf("a Execute Timeout = %v; want 2s", got)
	}
	if _, err := b.Execute(context.Background()); err != nil {
		t.Fatalf("b.Execute: %v", err)
	}
	if got := fe.lastExecute().Query.Timeout; got != 5*time.Second {
		t.Errorf("b Execute Timeout = %v; want 5s", got)
	}

	// Dispatch the base to confirm Timeout remains zero.
	if _, err := base.Execute(context.Background()); err != nil {
		t.Fatalf("base.Execute: %v", err)
	}
	if got := fe.lastExecute().Query.Timeout; got != 0 {
		t.Errorf("base Execute Timeout = %v; want 0", got)
	}
}

func TestImmutability_BindOnChildDoesNotMutateParent(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	base := newWith(fe, "SELECT $1").Bind(1)
	child := base.Bind(2)

	if !reflect.DeepEqual(base.args, []any{1}) {
		t.Errorf("base.args mutated: got %v want [1]", base.args)
	}
	if !reflect.DeepEqual(child.args, []any{2}) {
		t.Errorf("child.args = %v; want [2]", child.args)
	}
}

func TestDontLog_AppliesWithoutLoggingToContext(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	if _, err := newWith(fe, "SELECT 1").DontLog().Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !fe.lastExecute().Suppressed {
		t.Errorf("Execute: LoggingSuppressed = false; want true")
	}

	if _, err := newWith(fe, "SELECT 1").DontLog().Stream(context.Background()); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !fe.lastStream().Suppressed {
		t.Errorf("Stream: LoggingSuppressed = false; want true")
	}

	if _, err := newWith(fe, "SELECT 1").DontLog().Explain(context.Background(), false); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !fe.lastExplain().Suppressed {
		t.Errorf("Explain: LoggingSuppressed = false; want true")
	}

	// Sanity: a sibling without DontLog must not see the flag.
	if _, err := newWith(fe, "SELECT 1").Execute(context.Background()); err != nil {
		t.Fatalf("plain Execute: %v", err)
	}
	if fe.lastExecute().Suppressed {
		t.Errorf("plain Execute: LoggingSuppressed = true; want false")
	}
}

func TestWithTx_NilIsNoOp_NonNilIsRecorded(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	base := newWith(fe, "SELECT 1")

	// Nil tx leaves the field nil.
	nilDerived := base.WithTx(nil)
	if nilDerived.tx != nil {
		t.Errorf("WithTx(nil): tx = %v; want nil", nilDerived.tx)
	}

	// Non-nil tx is stored on the derived QueryObj.
	tx := fakeTx{}
	derived := base.WithTx(tx)
	if derived.tx == nil {
		t.Fatalf("WithTx(tx): tx unexpectedly nil")
	}
	// Base untouched.
	if base.tx != nil {
		t.Errorf("base.tx mutated by WithTx: got %v want nil", base.tx)
	}
}

func TestExecute_EmptySQLReturnsErrEmptyStatement(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "").Execute(context.Background())
	if !errors.Is(err, ErrEmptyStatement) {
		t.Fatalf("Execute err = %v; want %v", err, ErrEmptyStatement)
	}
	if len(fe.executeCalls) != 0 {
		t.Errorf("Execute should not dispatch on empty SQL; got %d calls", len(fe.executeCalls))
	}
}

func TestStream_EmptySQLReturnsErrEmptyStatement(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "").Stream(context.Background())
	if !errors.Is(err, ErrEmptyStatement) {
		t.Fatalf("Stream err = %v; want %v", err, ErrEmptyStatement)
	}
}

func TestExplain_EmptySQLReturnsErrEmptyStatement(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "").Explain(context.Background(), true)
	if !errors.Is(err, ErrEmptyStatement) {
		t.Fatalf("Explain err = %v; want %v", err, ErrEmptyStatement)
	}
}

func TestWithTimeout_ZeroPropagates(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "SELECT 1").WithTimeout(0).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := fe.lastExecute().Query.Timeout; got != 0 {
		t.Errorf("Timeout = %v; want 0", got)
	}
}

func TestBind_TwiceReplaces(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "SELECT $1,$2").Bind(1, 2).Bind(99).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := fe.lastExecute().Query.Args; !reflect.DeepEqual(got, []any{99}) {
		t.Errorf("Args = %v; want [99] (Bind must replace, not append)", got)
	}
}

func TestBind_EmptyCallClearsArgs(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{}
	_, err := newWith(fe, "SELECT 1").Bind(1, 2).Bind().Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := fe.lastExecute().Query.Args; got != nil {
		t.Errorf("Args = %v; want nil after empty Bind()", got)
	}
}

func TestStream_ReturnsHandleVerbatim(t *testing.T) {
	t.Parallel()

	// We can't easily construct a real RunHandle without driver bits, so
	// stash the handle pointer the fake returns and confirm Stream returns
	// the same pointer. nil-pointer round-trip is sufficient — what matters
	// is that QueryObj does not wrap or substitute.
	fe := &fakeExecutor{streamHandle: nil}
	got, err := newWith(fe, "SELECT 1").Stream(context.Background())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got != nil {
		t.Errorf("Stream handle = %v; want the staged nil verbatim", got)
	}

	// Error pass-through.
	wantErr := errors.New("boom")
	fe2 := &fakeExecutor{streamErr: wantErr}
	if _, err := newWith(fe2, "SELECT 1").Stream(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("Stream err = %v; want %v", err, wantErr)
	}
}

func TestExplain_DelegatesAnalyzeFlag(t *testing.T) {
	t.Parallel()

	fe := &fakeExecutor{explainPlan: models.Plan{RawText: "fake plan"}}
	plan, err := newWith(fe, "SELECT 1").Explain(context.Background(), true)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if plan.RawText != "fake plan" {
		t.Errorf("plan.RawText = %q; want fake plan", plan.RawText)
	}
	got := fe.lastExplain()
	if !got.Analyze {
		t.Errorf("Analyze = false; want true")
	}
	if got.Query.SQL != "SELECT 1" {
		t.Errorf("Query.SQL = %q; want SELECT 1", got.Query.SQL)
	}

	// Now without analyze.
	if _, err := newWith(fe, "SELECT 1").Explain(context.Background(), false); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if fe.lastExplain().Analyze {
		t.Errorf("Analyze = true; want false")
	}
}

func TestExecute_PassThroughErrorFromExecutor(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("driver: boom")
	fe := &fakeExecutor{executeErr: wantErr}
	_, err := newWith(fe, "SELECT 1").Execute(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute err = %v; want %v", err, wantErr)
	}
}

// TestLoggingSuppressed_PublicHelper sanity-checks the exported helper so
// future refactors of the ctxKey don't silently break the pkg/query
// integration.
func TestLoggingSuppressed_PublicHelper(t *testing.T) {
	t.Parallel()

	if session.LoggingSuppressed(context.Background()) {
		t.Errorf("LoggingSuppressed(bg) = true; want false")
	}
	if !session.LoggingSuppressed(session.WithoutLogging(context.Background())) {
		t.Errorf("LoggingSuppressed(WithoutLogging(bg)) = false; want true")
	}
}
