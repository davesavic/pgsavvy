package helpers_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	helpers "github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/models"
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

func (f *fakeSession) TableStats(context.Context, string, string) (int64, int64, error) {
	panic("TableStats not used")
}

func (f *fakeSession) ListFunctions(context.Context) ([]string, error) {
	panic("ListFunctions not used")
}

func (f *fakeSession) DescribeFunction(context.Context, string, string) ([]models.FunctionDetail, error) {
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
func (f *fakeSession) InTransaction() bool                       { return false }
func (f *fakeSession) CurrentTransaction() drivers.Transaction   { return nil }
func (f *fakeSession) LiveTxStatus() (models.TxStatus, []string) { return "", nil }

// fakeAcquirer hands out a pre-built fakeSession (or an error). Mirrors
// drivers.Connection.AcquireSession's signature.
type fakeAcquirer struct {
	sess  *fakeSession
	err   error
	calls int
}

func (a *fakeAcquirer) AcquireSession(_ context.Context) (drivers.Session, error) {
	a.calls++
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

func TestReview_row_outcome_grouped_identity(t *testing.T) {
	for _, mode := range []string{"apply", "overwrite", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			set := newApplySet()
			mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{int64(1)}, Column: "id", OldValue: int64(1), NewExpr: "id + 10", Kind: models.Expression})
			mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{int64(1)}, Column: "name", OldValue: "old", NewValue: "new"})
			sess := &fakeSession{script: []execResponse{{}, {RowsAffected: 1, Rows: [][]any{{int64(111)}}, Cols: []string{"id"}}, {}, {Rows: [][]any{{int64(111), "new"}}, Cols: []string{"id", "name"}}}}
			helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
			var err error
			if mode == "overwrite" {
				_, err = helper.Overwrite(context.Background(), set, []string{"id"})
			} else {
				var conflicts []models.ConflictedEdit
				_, conflicts, err = helper.Apply(context.Background(), set, []string{"id"}, mode == "dry_run")
				if len(conflicts) != 0 {
					t.Fatalf("unexpected conflicts: %d", len(conflicts))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			updates := 0
			for _, call := range sess.calls {
				if strings.HasPrefix(call.SQL, "UPDATE ") {
					updates++
					if !strings.Contains(call.SQL, `"id" = id + 10, "name" = $1`) || !strings.Contains(call.SQL, `RETURNING "id"`) {
						t.Fatalf("not one returning row mutation: %s", call.SQL)
					}
				}
			}
			if updates != 1 {
				t.Fatalf("updates=%d, want one atomic row mutation", updates)
			}
			if mode != "dry_run" {
				last := sess.calls[len(sess.calls)-1]
				if !reflect.DeepEqual(last.Args, []any{int64(111)}) {
					t.Fatalf("refetch used %v, want server-returned key 111", last.Args)
				}
			}
		})
	}
}

func TestReview_row_outcome_commit_boundary(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		for _, phase := range []string{"refresh", "commit"} {
			name := phase + "/apply"
			if overwrite {
				name = phase + "/overwrite"
			}
			t.Run(name, func(t *testing.T) {
				set := newApplySet()
				mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{int64(1)}, Column: "n", Kind: models.Expression, NewExpr: "n + 1"})
				failure := errors.New("synthetic I/O failure")
				script := []execResponse{{}, {RowsAffected: 1, Rows: [][]any{{int64(1)}}}, {}, {}}
				if phase == "commit" {
					script[2].Err = failure
				} else {
					script[3].Err = failure
				}
				sess := &fakeSession{script: script}
				h := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
				var result helpers.ApplyResult
				var err error
				if overwrite {
					result, err = h.Overwrite(context.Background(), set, []string{"id"})
				} else {
					result, _, err = h.Apply(context.Background(), set, []string{"id"}, false)
				}
				if !errors.Is(err, failure) || result.Committed != (phase == "refresh") || result.CommitUnknown != (phase == "commit") || len(result.Edits) != 1 || !sess.closed {
					t.Fatalf("wrong boundary receipt: committed=%v unknown=%v edits=%d err=%v", result.Committed, result.CommitUnknown, len(result.Edits), err)
				}
				if phase == "commit" {
					if !errors.Is(err, helpers.ErrCommitUnknown) || !set.CommitOutcomeUnknown() {
						t.Fatal("COMMIT error assumed rollback")
					}
					calls := len(sess.calls)
					for _, mode := range []string{"apply", "overwrite", "dry_run"} {
						if mode == "overwrite" {
							_, err = h.Overwrite(context.Background(), set, []string{"id"})
						} else {
							_, _, err = h.Apply(context.Background(), set, []string{"id"}, mode == "dry_run")
						}
						if !errors.Is(err, models.ErrCommitOutcomeUnknown) || len(sess.calls) != calls {
							t.Fatalf("%s replayed uncertain write", mode)
						}
					}
					set.Clear()
					_, _, err = h.Apply(context.Background(), set, []string{"id"}, false)
					if !errors.Is(err, models.ErrCommitOutcomeUnknown) || len(sess.calls) != calls {
						t.Fatal("Clear allowed uncertain replay")
					}
				}
			})
		}
	}
}

func TestReview_row_outcome_missing_returning(t *testing.T) {
	for _, returned := range [][][]any{nil, {{}}, {{1, 2}}, {{1}, {2}}} {
		t.Run(fmt.Sprint(returned), func(t *testing.T) {
			set := newApplySet()
			mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{1}, Column: "n", NewValue: 2})
			sess := &fakeSession{script: []execResponse{{}, {RowsAffected: 1, Rows: returned}, {}}}
			h := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
			res, _, err := h.Apply(context.Background(), set, []string{"id"}, false)
			if err == nil || res.Committed || sess.calls[len(sess.calls)-1].SQL != "ROLLBACK" {
				t.Fatal("malformed identity committed")
			}
		})
	}
}

func TestReview_row_outcome_refetch_order(t *testing.T) {
	set := newApplySet()
	for _, key := range []int{1, 2} {
		mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{key}, Column: "id", OldValue: key, NewValue: key + 10})
	}
	sess := &fakeSession{script: []execResponse{{}, {RowsAffected: 1, Rows: [][]any{{11}}}, {RowsAffected: 1, Rows: [][]any{{12}}}, {}, {Cols: []string{"id", "n"}, Rows: [][]any{{12, "second"}, {11, "first"}}}}}
	h := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
	res, _, err := h.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.RefetchedOriginalPKs, [][]any{{1}, {2}}) || res.RefetchedRows[0].Values[1] != "first" || res.RefetchedRows[1].Values[1] != "second" {
		t.Fatal("SELECT order corrupted original-row mapping")
	}
}

func TestReview_row_outcome_structured_keys(t *testing.T) {
	edits := []models.PendingEdit{{PrimaryKey: []any{[]string{"a b"}}, Column: "n", NewValue: 1}, {PrimaryKey: []any{[]string{"a", "b"}}, Column: "m", NewValue: 2}}
	batch, err := helpers.BuildUpdateBatch(models.Ref{Schema: "app", Table: "items"}, []string{"key"}, edits, helpers.SQLModeParameterized, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || !reflect.DeepEqual(batch[0].OriginalPK, edits[0].PrimaryKey) || !reflect.DeepEqual(batch[1].OriginalPK, edits[1].PrimaryKey) {
		t.Fatal("formatted array identity conflated separate rows")
	}
}

func TestReview_row_outcome_NaN_identity(t *testing.T) {
	set := newApplySet()
	key := math.NaN()
	for _, col := range []string{"n", "m"} {
		mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{key}, Column: col, OldValue: 1, NewValue: 2})
	}
	sess := &fakeSession{script: []execResponse{{}, {RowsAffected: 1, Rows: [][]any{{key}}}, {}, {Cols: []string{"id", "n", "m"}, Rows: [][]any{{key, 2, 2}}}}}
	h := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
	res, conflicts, err := h.Apply(context.Background(), set, []string{"id"}, false)
	if err != nil || len(conflicts) != 0 || len(res.SQL) != 1 || len(res.RefetchedRows) != 1 {
		t.Fatal("NaN identity did not group/refetch one committed row")
	}
}

func newApplySet() *models.PendingEditSet {
	return newCanonicalApplyFixture(models.Ref{Schema: "app", Table: "users"}, "id")
}

func TestApplyRejectsUnresolvedAndReinterpretedIdentityBeforeAcquire(t *testing.T) {
	acquirer := &fakeAcquirer{err: errors.New("unexpected acquisition")}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: acquirer})
	unbound := &models.PendingEditSet{Table: models.Ref{Schema: "app", Table: "users"}}
	mustAdd(t, unbound, models.PendingEdit{PrimaryKey: []any{1}, Column: "name", OldValue: "old", NewValue: "new"})
	if _, _, err := helper.Apply(context.Background(), unbound, []string{"display_alias"}, false); !errors.Is(err, models.ErrEditIdentityUnresolved) || acquirer.calls != 0 {
		t.Fatal("unresolved identity reached database acquisition")
	}
	bound := newApplySet()
	mustAdd(t, bound, models.PendingEdit{PrimaryKey: []any{1}, Column: "name", OldValue: "old", NewValue: "new"})
	if _, err := helper.Overwrite(context.Background(), bound, []string{"another_key"}); !errors.Is(err, models.ErrEditIdentityMismatch) || acquirer.calls != 0 {
		t.Fatal("overwrite reinterpreted captured identity before acquisition")
	}
}

func newCanonicalApplyFixture(ref models.Ref, names ...string) *models.PendingEditSet {
	set := &models.PendingEditSet{Table: ref}
	id := models.EditIdentity{RelationOID: 42}
	for i, name := range names {
		id.Columns = append(id.Columns, models.IdentityColumn{Attnum: uint16(i + 1), Name: name})
	}
	if err := set.BindIdentity(id); err != nil {
		panic(err)
	}
	return set
}

// --- Tests ---------------------------------------------------------------

func TestReview_overwrite_SQL(t *testing.T) {
	for _, kind := range []string{"literal", "null", "expression"} {
		for _, mode := range []string{"overwrite", "apply", "dry_run"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				set := newCanonicalApplyFixture(models.Ref{Schema: `a"pp`, Table: `u"sers`}, `i"d`, "key")
				e := models.PendingEdit{PrimaryKey: []any{7, "key'quoted"}, Column: `n"ame`, OldValue: "stale", NewValue: "new", Kind: models.Literal}
				switch kind {
				case "null":
					e.OldValue, e.NewValue = nil, nil
				case "expression":
					e.Kind, e.NewExpr = models.Expression, `upper("n""ame")`
				}
				mustAdd(t, set, e)
				sess := &fakeSession{script: []execResponse{{}, {RowsAffected: 1, Rows: [][]any{{7, "key'quoted"}}}, {}, {Rows: [][]any{{7, "key'quoted", "new"}}, Cols: []string{`i"d`, "key", `n"ame`}}}}
				helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
				var res helpers.ApplyResult
				var err error
				if mode == "overwrite" {
					res, err = helper.Overwrite(context.Background(), set, []string{`i"d`, "key"})
				} else {
					var conflicts []models.ConflictedEdit
					res, conflicts, err = helper.Apply(context.Background(), set, []string{`i"d`, "key"}, mode == "dry_run")
					if len(conflicts) != 0 {
						t.Fatalf("unexpected conflicts: %v", conflicts)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				wantArgs := []any{}
				assignment := e.NewExpr
				if e.Kind == models.Literal {
					wantArgs = append(wantArgs, e.NewValue)
					assignment = "$1"
				}
				firstPK := len(wantArgs) + 1
				wantArgs = append(wantArgs, e.PrimaryKey...)
				wantSQL := fmt.Sprintf(`UPDATE "a""pp"."u""sers" SET "n""ame" = %s WHERE "i""d" = $%d AND "key" = $%d`, assignment, firstPK, firstPK+1)
				if mode != "overwrite" {
					wantArgs = append(wantArgs, e.OldValue)
					wantSQL += fmt.Sprintf(` AND "n""ame" IS NOT DISTINCT FROM $%d`, len(wantArgs))
				}
				wantSQL += ` RETURNING "i""d", "key"`
				update := sess.calls[1]
				if update.SQL != wantSQL || !reflect.DeepEqual(update.Args, wantArgs) {
					t.Errorf("UPDATE = %q %v, want %q %v", update.SQL, update.Args, wantSQL, wantArgs)
				}
				if len(res.SQL) != 1 || res.SQL[0] != wantSQL {
					t.Errorf("result SQL = %v, want %q", res.SQL, wantSQL)
				}
				if !sess.closed {
					t.Error("dedicated session not closed")
				}
				for _, sqlMode := range []helpers.SQLMode{helpers.SQLModeParameterized, helpers.SQLModeLiteral} {
					preview, _ := helpers.BuildUpdateSQL(set.Table, []string{`i"d`, "key"}, e, sqlMode)
					if !strings.Contains(preview, ` AND "n""ame" IS NOT DISTINCT FROM `) {
						t.Errorf("ordinary builder lost optimistic guard: %s", preview)
					}
				}
			})
		}
	}
}

func TestReview_overwrite_UpdateError(t *testing.T) {
	for _, failure := range []error{errors.New("synthetic update failure"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			set := newApplySet()
			for _, id := range []int{1, 2} {
				mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{id}, Column: "name", OldValue: "old", NewValue: "new"})
			}
			sess := &fakeSession{script: []execResponse{{}, {RowsAffected: 1, Rows: [][]any{{1}}}, {Err: failure}, {}}}
			helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
			res, err := helper.Overwrite(context.Background(), set, []string{"id"})
			if !errors.Is(err, failure) || len(res.RowsAffected) != 0 {
				t.Fatalf("res=%+v err=%v, want wrapped update failure without success data", res, err)
			}
			if len(sess.calls) != 4 || sess.calls[3].SQL != "ROLLBACK" || !sess.closed {
				t.Errorf("failed batch did not roll back and close: %+v", sess.calls)
			}
		})
	}
}

func TestReview_identity_safety(t *testing.T) {
	for _, mode := range []string{"apply", "overwrite", "dry_run"} {
		for _, count := range []int64{0, 1, 2} {
			for _, prior := range []bool{false, true} {
				name := mode + "/" + fmt.Sprint(count) + "/prior_" + fmt.Sprint(prior)
				t.Run(name, func(t *testing.T) {
					set := newApplySet()
					script := []execResponse{{}}
					if prior {
						mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{2}, Column: "name", OldValue: "old", NewValue: "first", Kind: models.Literal})
						script = append(script, execResponse{RowsAffected: 1, Rows: [][]any{{2}}})
					}
					mustAdd(t, set, models.PendingEdit{PrimaryKey: []any{1}, Column: "name", OldValue: "old", NewValue: "new", Kind: models.Literal})
					response := execResponse{RowsAffected: count}
					if count == 1 {
						response.Rows = [][]any{{1}}
					}
					script = append(script, response)
					if count == 0 && mode == "apply" {
						script = append(script, execResponse{Rows: [][]any{{"server"}}})
					}
					refRows := [][]any{{1, "new"}}
					if prior {
						refRows = append(refRows, []any{2, "first"})
					}
					script = append(script, execResponse{}, execResponse{Rows: refRows, Cols: []string{"id", "name"}})
					sess := &fakeSession{script: script}
					helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})
					var res helpers.ApplyResult
					var conflicts []models.ConflictedEdit
					var err error
					if mode == "overwrite" {
						res, err = helper.Overwrite(context.Background(), set, []string{"id"})
					} else {
						res, conflicts, err = helper.Apply(context.Background(), set, []string{"id"}, mode == "dry_run")
					}
					wantErr := count > 1 || (count == 0 && mode == "overwrite")
					if (err != nil) != wantErr {
						t.Errorf("err=%v, want error=%v", err, wantErr)
					}
					wantConflicts := 0
					if count == 0 && mode == "apply" {
						wantConflicts = 1
					}
					if len(conflicts) != wantConflicts {
						t.Errorf("conflicts=%v, want %d", conflicts, wantConflicts)
					}
					wantCommit := count == 1 && mode != "dry_run"
					commits, rollbacks := 0, 0
					for _, call := range sess.calls {
						switch call.SQL {
						case "COMMIT":
							commits++
						case "ROLLBACK":
							rollbacks++
						}
					}
					if wantCommit {
						if commits != 1 || rollbacks != 0 {
							t.Errorf("commits=%d rollbacks=%d, want 1/0", commits, rollbacks)
						}
					} else if commits != 0 || rollbacks != 1 {
						t.Errorf("commits=%d rollbacks=%d, want 0/1", commits, rollbacks)
					}
					if count > 1 && (len(res.RowsAffected) != 0 || len(res.RefetchedRows) != 0) {
						t.Errorf("unsafe update returned success data: %+v", res)
					}
					if mode == "dry_run" && count <= 1 && res.RowsAffected[len(res.RowsAffected)-1] != int(count) {
						t.Errorf("dry-run counts=%v, want final %d", res.RowsAffected, count)
					}
					if !sess.closed {
						t.Error("dedicated session not closed")
					}
				})
			}
		}
	}
}

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
	if !errors.Is(err, models.ErrEditIdentityMismatch) {
		t.Fatalf("err = %v, want ErrEditIdentityMismatch", err)
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
			{}, // 0: BEGIN
			{RowsAffected: 1, Rows: [][]any{{int64(1)}}}, // 1: grouped UPDATE
			{}, // 2: COMMIT
			{Cols: []string{"id", "name"}, Rows: [][]any{{int64(1), "bob"}}}, // 3: SELECT refetch
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
	if len(sess.calls) != 4 {
		t.Fatalf("len(calls) = %d, want 4 (BEGIN, grouped UPDATE, COMMIT, refetch); calls=%v", len(sess.calls), sess.calls)
	}

	// BEGIN at index 0.
	if sess.calls[0].SQL != "BEGIN" {
		t.Fatalf("calls[0].SQL = %q, want BEGIN", sess.calls[0].SQL)
	}
	// Both assignments use one original-row predicate; expressions stay inline.
	got := sess.calls[1]
	wantSQL := `UPDATE "app"."users" SET "name" = $1, "flags" = flags | 1 WHERE "id" = $2 AND "name" IS NOT DISTINCT FROM $3 AND "flags" IS NOT DISTINCT FROM $4 RETURNING "id"`
	if got.SQL != wantSQL {
		t.Fatalf("UPDATE name SQL =\n  %q\nwant\n  %q", got.SQL, wantSQL)
	}
	if len(got.Args) != 4 || got.Args[0] != "bob" || got.Args[1] != int64(1) || got.Args[2] != "alice" || got.Args[3] != int64(0) {
		t.Fatalf("UPDATE args = %v, want [\"bob\", 1, \"alice\", 0]", got.Args)
	}
	// COMMIT.
	if sess.calls[2].SQL != "COMMIT" {
		t.Fatalf("calls[2].SQL = %q, want COMMIT", sess.calls[2].SQL)
	}
	// Refetch by PK.
	if !strings.Contains(sess.calls[3].SQL, "SELECT * FROM ") || !strings.Contains(sess.calls[3].SQL, `"id" IN ($1)`) {
		t.Fatalf("refetch SQL = %q, want SELECT * FROM with parameterized IN-list", sess.calls[3].SQL)
	}
	if len(sess.calls[3].Args) != 1 || sess.calls[3].Args[0] != int64(1) {
		t.Fatalf("refetch args = %v, want [1]", sess.calls[3].Args)
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
			{}, // 0: BEGIN
			{RowsAffected: 1, Rows: [][]any{{int64(1)}}}, // 1: UPDATE RETURNING id
			{}, // 2: ROLLBACK
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
			{}, // 0: BEGIN
			{RowsAffected: 1, Rows: [][]any{{int64(1), "x"}}}, // 1: UPDATE RETURNING a,b
			{}, // 2: COMMIT
			{Cols: []string{"a", "b", "v"}, Rows: [][]any{{int64(1), "x", "new"}}}, // 3: refetch
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newCanonicalApplyFixture(models.Ref{Schema: "app", Table: "t"}, "a", "b")
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
	wantSQL := `UPDATE "app"."t" SET "v" = $1 WHERE "a" = $2 AND "b" = $3 AND "v" IS NOT DISTINCT FROM $4 RETURNING "a", "b"`
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

func TestApply_SamePKMultipleColumns_OneUpdateOneCommit(t *testing.T) {
	sess := &fakeSession{
		script: []execResponse{
			{}, // 0: BEGIN
			{RowsAffected: 1, Rows: [][]any{{int64(7)}}}, // 1: UPDATE col1,col2
			{}, // 2: COMMIT
			{Cols: []string{"id"}, Rows: [][]any{{int64(7)}}}, // 3: refetch — single distinct PK
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
	if len(sess.calls) != 4 {
		t.Fatalf("len(calls) = %d, want 4 (BEGIN, grouped UPDATE, COMMIT, refetch)", len(sess.calls))
	}
	if !strings.Contains(sess.calls[1].SQL, `"name" = $1, "email" = $2`) || !strings.Contains(sess.calls[1].SQL, `"name" IS NOT DISTINCT FROM $4 AND "email" IS NOT DISTINCT FROM $5`) {
		t.Fatalf("group lost an assignment or guard: %s", sess.calls[1].SQL)
	}
	// Refetch must collapse to a SINGLE PK in the IN-list — both edits
	// touch row id=7.
	refetchSQL := sess.calls[3].SQL
	if !strings.Contains(refetchSQL, `"id" IN ($1)`) {
		t.Fatalf("refetch SQL = %q, want single-arg IN-list", refetchSQL)
	}
	if len(sess.calls[3].Args) != 1 {
		t.Fatalf("refetch args = %v, want single PK", sess.calls[3].Args)
	}
}

func TestApply_PKEdit_WhereUsesOldValue(t *testing.T) {
	// A PK edit (changing column "id" itself) MUST address the row by
	// the OLD pk value — using the NEW value would target a different
	// (or non-existent) row. The WHERE clause's IS NOT DISTINCT FROM
	// also references OldValue.
	sess := &fakeSession{
		script: []execResponse{
			{}, // BEGIN
			{RowsAffected: 1, Rows: [][]any{{int64(99)}}}, // UPDATE RETURNING changed id
			{}, // COMMIT
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
			{}, // BEGIN
			{RowsAffected: 1, Rows: [][]any{{int64(1)}}}, // UPDATE RETURNING User
			{}, // COMMIT
			{Cols: []string{"User"}, Rows: [][]any{{int64(1)}}}, // refetch
		},
	}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: &fakeAcquirer{sess: sess}})

	set := newCanonicalApplyFixture(models.Ref{Schema: "weird schema", Table: "User"}, "User")
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
