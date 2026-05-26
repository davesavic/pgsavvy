package helpers_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	helpers "github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// --- Fakes ---------------------------------------------------------------

// recordedExec captures a single Execute call made on the fake session.
// SQL keeps the statement verbatim so tests can assert on identifier
// quoting + parameter placement; Args is shallow-copied.
type recordedExec struct {
	SQL  string
	Args []any
}

// execResponse is the per-statement scripted reply. RowsAffected and Err
// drive the helper's branching; Rows + Cols are used to script the
// post-conflict server-value re-fetch and the post-commit refetch.
type execResponse struct {
	RowsAffected int64
	Err          error
	Rows         [][]any
	Cols         []string
}

// fakeSession implements drivers.Session well enough for CellApplyHelper.
// Other Session methods panic — the helper does not call them.
type fakeSession struct {
	id       models.SessionID
	calls    []recordedExec
	script   []execResponse
	closed   bool
	beginErr error
}

func (f *fakeSession) Close() error             { f.closed = true; return nil }
func (f *fakeSession) ID() models.SessionID     { return f.id }
func (f *fakeSession) Encoder() drivers.Encoder { return nil }

func (f *fakeSession) Execute(_ context.Context, q models.Query) (models.Result, error) {
	idx := len(f.calls)
	argsCopy := append([]any(nil), q.Args...)
	f.calls = append(f.calls, recordedExec{SQL: q.SQL, Args: argsCopy})

	if idx >= len(f.script) {
		return models.Result{}, nil
	}
	resp := f.script[idx]
	if resp.Err != nil {
		return models.Result{}, resp.Err
	}
	cols := make([]*models.Column, len(resp.Cols))
	for i, n := range resp.Cols {
		cols[i] = &models.Column{Name: n}
	}
	rows := make([]*models.Row, len(resp.Rows))
	for i, vals := range resp.Rows {
		rows[i] = &models.Row{Values: vals}
	}
	return models.Result{
		Columns:      cols,
		Rows:         rows,
		RowsAffected: resp.RowsAffected,
	}, nil
}

// The remaining drivers.Session methods are not exercised by the helper.
// Panicking on use makes accidental new-call regressions visible.
func (f *fakeSession) ListDatabases(context.Context) ([]models.Database, error) {
	panic("ListDatabases not used")
}

func (f *fakeSession) ListSchemas(context.Context, string) ([]models.Schema, error) {
	panic("ListSchemas not used")
}

func (f *fakeSession) ListTables(context.Context, string) ([]*models.Table, error) {
	panic("ListTables not used")
}

func (f *fakeSession) ListColumns(context.Context, string, string) ([]models.Column, error) {
	panic("ListColumns not used")
}

func (f *fakeSession) ListIndexes(context.Context, string, string) ([]models.Index, error) {
	panic("ListIndexes not used")
}

func (f *fakeSession) ListConstraints(context.Context, string, string) ([]models.Constraint, error) {
	panic("ListConstraints not used")
}

func (f *fakeSession) ListForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	panic("ListForeignKeys not used")
}

func (f *fakeSession) ListInboundForeignKeys(context.Context, string, string) ([]models.ForeignKey, error) {
	panic("ListInboundForeignKeys not used")
}

func (f *fakeSession) ListFunctions(context.Context) ([]string, error) {
	panic("ListFunctions not used")
}

func (f *fakeSession) DescribeFunction(context.Context, string, string) (models.FunctionDetail, error) {
	panic("DescribeFunction not used")
}

func (f *fakeSession) Stream(context.Context, models.Query) (drivers.RowStream, error) {
	panic("Stream not used")
}

func (f *fakeSession) Explain(context.Context, models.Query, bool) (models.Plan, error) {
	panic("Explain not used")
}

func (f *fakeSession) Begin(context.Context, models.TxOptions) (drivers.Transaction, error) {
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	panic("Begin not used (apply uses Execute BEGIN)")
}
func (f *fakeSession) InTransaction() bool                     { return false }
func (f *fakeSession) CurrentTransaction() drivers.Transaction { return nil }

// fakeAcquirer hands out a pre-built fakeSession (or an error). Mirrors
// drivers.Connection.AcquireSession's signature.
type fakeAcquirer struct {
	sess *fakeSession
	err  error
}

func (a *fakeAcquirer) AcquireSession(_ context.Context) (drivers.Session, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.sess, nil
}

// --- Helpers -------------------------------------------------------------

func mustAdd(t *testing.T, set *models.PendingEditSet, e models.PendingEdit) {
	t.Helper()
	if err := set.Add(e); err != nil {
		t.Fatalf("PendingEditSet.Add: %v", err)
	}
}

func newApplySet() *models.PendingEditSet {
	return &models.PendingEditSet{Table: models.Ref{Schema: "app", Table: "users"}}
}

// --- Tests ---------------------------------------------------------------

func TestApply_EmptySet_NoBegin(t *testing.T) {
	sess := &fakeSession{}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	res, conflicts, err := helper.Apply(context.Background(), newApplySet(), []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if conflicts != nil {
		t.Fatalf("conflicts = %v, want nil", conflicts)
	}
	if len(res.RowsAffected) != 0 {
		t.Fatalf("rowsAffected = %v, want empty", res.RowsAffected)
	}
	if len(sess.calls) != 0 {
		t.Fatalf("calls = %v, want no BEGIN issued on empty set", sess.calls)
	}
}

func TestApply_NoAcquirer(t *testing.T) {
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{})
	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})
	_, _, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if !errors.Is(err, helpers.ErrNoAcquirer) {
		t.Fatalf("err = %v, want ErrNoAcquirer", err)
	}
}

func TestApply_MissingPKColumns(t *testing.T) {
	sess := &fakeSession{}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})
	_, _, err := helper.Apply(context.Background(), set, nil, false)
	if !errors.Is(err, helpers.ErrMissingPKColumns) {
		t.Fatalf("err = %v, want ErrMissingPKColumns", err)
	}
}

func TestApply_PKLengthMismatch(t *testing.T) {
	sess := &fakeSession{}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
	set := newApplySet()
	// PK with one value, but pkCols declares two.
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})
	_, _, err := helper.Apply(context.Background(), set, []string{"a", "b"}, false)
	if !errors.Is(err, helpers.ErrPKLengthMismatch) {
		t.Fatalf("err = %v, want ErrPKLengthMismatch", err)
	}
}

func TestApply_DedicatedSession_AcquireFailure(t *testing.T) {
	wantErr := errors.New("pool: no spare conn")
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{err: wantErr}})
	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})

	_, _, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err == nil {
		t.Fatalf("err = nil, want pool-exhausted wrap")
	}
	if !errors.Is(err, helpers.ErrPoolExhausted) {
		t.Fatalf("err = %v, want ErrPoolExhausted in chain", err)
	}
	if !strings.Contains(err.Error(), "pool") {
		t.Fatalf("err message %q must mention 'pool' so users see actionable hint", err.Error())
	}
}

func TestApply_HappyPath_LiteralAndExpression(t *testing.T) {
	sess := &fakeSession{
		script: []execResponse{
			{},                // 0: BEGIN
			{RowsAffected: 1}, // 1: UPDATE name
			{RowsAffected: 1}, // 2: UPDATE flags (expression)
			{},                // 3: COMMIT
			{Cols: []string{"id", "name"}, Rows: [][]any{{int64(1), "bob"}}}, // 4: SELECT refetch
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "alice", NewValue: "bob", Kind: models.Literal,
	})
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "flags",
		OldValue: int64(0), NewExpr: "flags | 1", Kind: models.Expression,
	})

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if conflicts != nil {
		t.Fatalf("conflicts = %v, want nil", conflicts)
	}
	if got, want := res.RowsAffected, []int{1, 1}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("rowsAffected = %v, want %v", got, want)
	}
	if len(sess.calls) != 5 {
		t.Fatalf("len(calls) = %d, want 5 (BEGIN, 2 UPDATEs, COMMIT, refetch); calls=%v", len(sess.calls), sess.calls)
	}

	// BEGIN at index 0.
	if sess.calls[0].SQL != "BEGIN" {
		t.Fatalf("calls[0].SQL = %q, want BEGIN", sess.calls[0].SQL)
	}
	// First UPDATE: literal edit; identifier quoted via QuoteIdent;
	// arguments are [NewValue, pk, OldValue].
	got := sess.calls[1]
	wantSQL := `UPDATE "app"."users" SET "name" = $1 WHERE "id" = $2 AND "name" IS NOT DISTINCT FROM $3`
	if got.SQL != wantSQL {
		t.Fatalf("UPDATE name SQL =\n  %q\nwant\n  %q", got.SQL, wantSQL)
	}
	if len(got.Args) != 3 || got.Args[0] != "bob" || got.Args[1] != int64(1) || got.Args[2] != "alice" {
		t.Fatalf("UPDATE name args = %v, want [\"bob\", 1, \"alice\"]", got.Args)
	}
	// Second UPDATE: expression — NewExpr inlined verbatim, NOT
	// parameterized. Args carry only [pk, OldValue].
	got = sess.calls[2]
	wantSQL = `UPDATE "app"."users" SET "flags" = flags | 1 WHERE "id" = $1 AND "flags" IS NOT DISTINCT FROM $2`
	if got.SQL != wantSQL {
		t.Fatalf("UPDATE flags SQL =\n  %q\nwant\n  %q", got.SQL, wantSQL)
	}
	if len(got.Args) != 2 || got.Args[0] != int64(1) || got.Args[1] != int64(0) {
		t.Fatalf("UPDATE flags args = %v, want [1, 0]", got.Args)
	}
	// COMMIT.
	if sess.calls[3].SQL != "COMMIT" {
		t.Fatalf("calls[3].SQL = %q, want COMMIT", sess.calls[3].SQL)
	}
	// Refetch by PK.
	if !strings.Contains(sess.calls[4].SQL, "SELECT * FROM ") || !strings.Contains(sess.calls[4].SQL, `"id" IN ($1)`) {
		t.Fatalf("refetch SQL = %q, want SELECT * FROM with parameterized IN-list", sess.calls[4].SQL)
	}
	if len(sess.calls[4].Args) != 1 || sess.calls[4].Args[0] != int64(1) {
		t.Fatalf("refetch args = %v, want [1]", sess.calls[4].Args)
	}
	if len(res.RefetchedRows) != 1 {
		t.Fatalf("refetchedRows = %d, want 1", len(res.RefetchedRows))
	}
	if !sess.closed {
		t.Fatalf("session not closed; helper must Release dedicated session")
	}
}

func TestApply_Conflict_RollsBackAndCollects(t *testing.T) {
	sess := &fakeSession{
		script: []execResponse{
			{},                // 0: BEGIN
			{RowsAffected: 0}, // 1: UPDATE (conflict)
			{Cols: []string{"name"}, Rows: [][]any{{"server-side-name"}}}, // 2: SELECT server value
			{}, // 3: ROLLBACK
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "alice", NewValue: "bob", Kind: models.Literal,
		LoadedAt: time.Now(),
	})

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want 1 entry", conflicts)
	}
	if conflicts[0].ServerValue != "server-side-name" {
		t.Fatalf("ServerValue = %v, want 'server-side-name'", conflicts[0].ServerValue)
	}
	if conflicts[0].Edit.Column != "name" {
		t.Fatalf("Edit.Column = %q, want name", conflicts[0].Edit.Column)
	}
	if len(res.RowsAffected) != 0 {
		t.Fatalf("rowsAffected = %v, want empty on conflict", res.RowsAffected)
	}
	// Final call MUST be ROLLBACK — no COMMIT issued.
	last := sess.calls[len(sess.calls)-1]
	if last.SQL != "ROLLBACK" {
		t.Fatalf("final SQL = %q, want ROLLBACK", last.SQL)
	}
	for _, c := range sess.calls {
		if c.SQL == "COMMIT" {
			t.Fatalf("COMMIT issued despite conflict: calls=%v", sess.calls)
		}
	}
}

func TestApply_ServerError_RollsBack(t *testing.T) {
	serverErr := errors.New("column \"nope\" does not exist")
	sess := &fakeSession{
		script: []execResponse{
			{},               // 0: BEGIN
			{Err: serverErr}, // 1: UPDATE fails
			{},               // 2: ROLLBACK
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "nope",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})

	_, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if conflicts != nil {
		t.Fatalf("conflicts = %v, want nil on server error", conflicts)
	}
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want wrapped server error", err)
	}
	last := sess.calls[len(sess.calls)-1]
	if last.SQL != "ROLLBACK" {
		t.Fatalf("final SQL = %q, want ROLLBACK", last.SQL)
	}
}

func TestApply_DryRun_AlwaysRollsBack(t *testing.T) {
	sess := &fakeSession{
		script: []execResponse{
			{},                // 0: BEGIN
			{RowsAffected: 1}, // 1: UPDATE
			{},                // 2: ROLLBACK
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if conflicts != nil {
		t.Fatalf("conflicts = %v, want nil in dry-run", conflicts)
	}
	if len(res.RowsAffected) != 1 || res.RowsAffected[0] != 1 {
		t.Fatalf("rowsAffected = %v, want [1]", res.RowsAffected)
	}
	// COMMIT must NEVER be issued on dry-run.
	for _, c := range sess.calls {
		if c.SQL == "COMMIT" {
			t.Fatalf("dry-run committed: calls=%v", sess.calls)
		}
	}
	last := sess.calls[len(sess.calls)-1]
	if last.SQL != "ROLLBACK" {
		t.Fatalf("final SQL = %q, want ROLLBACK", last.SQL)
	}
}

func TestApply_DryRun_SkipsConflictCollection(t *testing.T) {
	// Even when RowsAffected==0 in dry-run mode, the helper must NOT
	// issue a SELECT-for-server-value (conflict collection is skipped).
	sess := &fakeSession{
		script: []execResponse{
			{},                // 0: BEGIN
			{RowsAffected: 0}, // 1: UPDATE — would-be-conflict
			{},                // 2: ROLLBACK
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "alice", NewValue: "bob", Kind: models.Literal,
	})

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if conflicts != nil {
		t.Fatalf("conflicts = %v, want nil in dry-run", conflicts)
	}
	if len(res.RowsAffected) != 1 || res.RowsAffected[0] != 0 {
		t.Fatalf("rowsAffected = %v, want [0] (preview, no apply)", res.RowsAffected)
	}
	for _, c := range sess.calls {
		if strings.HasPrefix(c.SQL, "SELECT ") {
			t.Fatalf("dry-run issued SELECT for conflict collection: %v", c.SQL)
		}
	}
}

func TestApply_CompositePK_WhereAndRefetchUseAllColumns(t *testing.T) {
	sess := &fakeSession{
		script: []execResponse{
			{},                // 0: BEGIN
			{RowsAffected: 1}, // 1: UPDATE
			{},                // 2: COMMIT
			{Cols: []string{"a", "b", "v"}, Rows: [][]any{{int64(1), "x", "new"}}}, // 3: refetch
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := &models.PendingEditSet{Table: models.Ref{Schema: "app", Table: "t"}}
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1), "x"}, Column: "v",
		OldValue: "old", NewValue: "new", Kind: models.Literal,
	})

	res, conflicts, err := helper.Apply(context.Background(), set, []string{"a", "b"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if conflicts != nil {
		t.Fatalf("conflicts = %v", conflicts)
	}

	// UPDATE WHERE clause includes BOTH PK columns and the
	// IS-NOT-DISTINCT-FROM guard.
	wantSQL := `UPDATE "app"."t" SET "v" = $1 WHERE "a" = $2 AND "b" = $3 AND "v" IS NOT DISTINCT FROM $4`
	if sess.calls[1].SQL != wantSQL {
		t.Fatalf("composite UPDATE SQL =\n  %q\nwant\n  %q", sess.calls[1].SQL, wantSQL)
	}

	// Refetch SELECT uses the row-constructor IN form for composite PKs.
	refetchSQL := sess.calls[3].SQL
	if !strings.Contains(refetchSQL, `("a", "b") IN`) {
		t.Fatalf("refetch SQL = %q, want composite IN form", refetchSQL)
	}
	if !strings.Contains(refetchSQL, "($1, $2)") {
		t.Fatalf("refetch SQL = %q, want parameterized tuple", refetchSQL)
	}
	if len(res.RefetchedRows) != 1 {
		t.Fatalf("refetchedRows = %d, want 1", len(res.RefetchedRows))
	}
}

func TestApply_SamePKMultipleColumns_TwoUpdatesOneCommit(t *testing.T) {
	sess := &fakeSession{
		script: []execResponse{
			{},                // 0: BEGIN
			{RowsAffected: 1}, // 1: UPDATE col1
			{RowsAffected: 1}, // 2: UPDATE col2
			{},                // 3: COMMIT
			{Cols: []string{"id"}, Rows: [][]any{{int64(7)}}}, // 4: refetch — single distinct PK
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(7)}, Column: "name", OldValue: "a", NewValue: "b", Kind: models.Literal,
	})
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(7)}, Column: "email", OldValue: "x@y", NewValue: "z@y", Kind: models.Literal,
	})

	_, conflicts, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if conflicts != nil {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if len(sess.calls) != 5 {
		t.Fatalf("len(calls) = %d, want 5 (BEGIN, 2 UPDATE, COMMIT, refetch)", len(sess.calls))
	}
	// Refetch must collapse to a SINGLE PK in the IN-list — both edits
	// touch row id=7.
	refetchSQL := sess.calls[4].SQL
	if !strings.Contains(refetchSQL, `"id" IN ($1)`) {
		t.Fatalf("refetch SQL = %q, want single-arg IN-list", refetchSQL)
	}
	if len(sess.calls[4].Args) != 1 {
		t.Fatalf("refetch args = %v, want single PK", sess.calls[4].Args)
	}
}

func TestApply_PKEdit_WhereUsesOldValue(t *testing.T) {
	// A PK edit (changing column "id" itself) MUST address the row by
	// the OLD pk value — using the NEW value would target a different
	// (or non-existent) row. The WHERE clause's IS NOT DISTINCT FROM
	// also references OldValue.
	sess := &fakeSession{
		script: []execResponse{
			{},                // BEGIN
			{RowsAffected: 1}, // UPDATE
			{},                // COMMIT
			{Cols: []string{"id"}, Rows: [][]any{{int64(99)}}}, // refetch
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		// The PendingEdit.PrimaryKey is the OLD identifier (per A2's
		// staging contract: PK is captured at edit-time and stays
		// fixed across re-edits).
		PrimaryKey: []any{int64(42)}, Column: "id",
		OldValue: int64(42), NewValue: int64(99), Kind: models.Literal,
	})

	_, _, err := helper.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := sess.calls[1]
	// Expected layout: SET "id" = $1 (NewValue=99) WHERE "id" = $2 (PK=42)
	// AND "id" IS NOT DISTINCT FROM $3 (OldValue=42).
	if len(got.Args) != 3 {
		t.Fatalf("args len = %d, want 3", len(got.Args))
	}
	if got.Args[0] != int64(99) {
		t.Fatalf("SET arg = %v, want 99 (NewValue)", got.Args[0])
	}
	if got.Args[1] != int64(42) {
		t.Fatalf("WHERE pk arg = %v, want 42 (PK from edit, NOT NewValue)", got.Args[1])
	}
	if got.Args[2] != int64(42) {
		t.Fatalf("IS NOT DISTINCT FROM arg = %v, want 42 (OldValue, NOT NewValue)", got.Args[2])
	}
}

func TestApply_IdentifiersQuotedViaQuoteIdent(t *testing.T) {
	// Reserved-word + mixed-case identifiers must round-trip via
	// QuoteIdent / QuoteQualified — proves no string-formatted
	// identifier reaches the server (ADR-21).
	sess := &fakeSession{
		script: []execResponse{
			{},                // BEGIN
			{RowsAffected: 1}, // UPDATE
			{},                // COMMIT
			{Cols: []string{"user"}, Rows: [][]any{{int64(1)}}}, // refetch
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := &models.PendingEditSet{Table: models.Ref{Schema: "weird schema", Table: "User"}}
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "Order",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})

	_, _, err := helper.Apply(context.Background(), set, []string{"User"}, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := sess.calls[1].SQL
	if !strings.Contains(got, `"weird schema"."User"`) {
		t.Fatalf("UPDATE SQL = %q, missing quoted qualified name", got)
	}
	if !strings.Contains(got, `"Order"`) {
		t.Fatalf("UPDATE SQL = %q, missing quoted column", got)
	}
}

func TestApply_BoundedContext_AppliesDefaultTimeout(t *testing.T) {
	// When the caller-supplied ctx carries NO deadline, the helper
	// applies its own timeout. We can detect this by checking that the
	// ctx passed to AcquireSession has a deadline.
	captured := &deadlineCheckingAcquirer{}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{
		Acquirer: captured,
		Timeout:  50 * time.Millisecond,
	})
	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})
	// AcquireSession will return an error so we short-circuit; we only
	// care about the ctx the helper passed in.
	_, _, _ = helper.Apply(context.Background(), set, []string{"id"}, false)
	if !captured.sawDeadline {
		t.Fatalf("AcquireSession received a ctx with no deadline; helper must impose one")
	}
}

func TestApply_BoundedContext_RespectsCallerDeadline(t *testing.T) {
	captured := &deadlineCheckingAcquirer{}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{
		Acquirer: captured,
		Timeout:  1 * time.Hour, // would dwarf the caller's deadline
	})
	set := newApplySet()
	mustAdd(t, set, models.PendingEdit{
		PrimaryKey: []any{int64(1)}, Column: "name",
		OldValue: "a", NewValue: "b", Kind: models.Literal,
	})

	callerDeadline := time.Now().Add(10 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()

	_, _, _ = helper.Apply(ctx, set, []string{"id"}, false)
	if captured.lastDeadline.After(callerDeadline.Add(time.Millisecond)) {
		t.Fatalf("helper widened caller deadline: got %v, caller had %v",
			captured.lastDeadline, callerDeadline)
	}
}

// deadlineCheckingAcquirer records whether the ctx passed to
// AcquireSession had a Deadline set. Returns an error so the helper
// exits early — we only need ctx-introspection.
type deadlineCheckingAcquirer struct {
	sawDeadline  bool
	lastDeadline time.Time
}

func (a *deadlineCheckingAcquirer) AcquireSession(ctx context.Context) (drivers.Session, error) {
	if d, ok := ctx.Deadline(); ok {
		a.sawDeadline = true
		a.lastDeadline = d
	}
	return nil, errors.New("short-circuit: deadline-checking acquirer never returns a session")
}
