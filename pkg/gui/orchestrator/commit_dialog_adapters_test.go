package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

type rowOutcomeSession struct {
	drivers.Session
	refreshErr error
	commitErr  error
	onCommit   func()
	queries    []models.Query
	closed     bool
}

func (s *rowOutcomeSession) Execute(_ context.Context, q models.Query) (models.Result, error) {
	s.queries = append(s.queries, q)
	switch {
	case strings.HasPrefix(q.SQL, "UPDATE "):
		return models.Result{RowsAffected: 1, Rows: []*models.Row{{Values: []any{int64(99)}}}}, nil
	case q.SQL == "COMMIT":
		if s.onCommit != nil {
			s.onCommit()
		}
		return models.Result{}, s.commitErr
	case strings.HasPrefix(q.SQL, "SELECT *"):
		return models.Result{Columns: []*models.Column{{Name: "id"}, {Name: "data"}}, Rows: []*models.Row{{Values: []any{int64(99), "server"}}}}, s.refreshErr
	default:
		return models.Result{}, nil
	}
}
func (s *rowOutcomeSession) Close() error { s.closed = true; return nil }

type rowOutcomeAcquirer struct{ sess *rowOutcomeSession }

func (a rowOutcomeAcquirer) AcquireSession(context.Context) (drivers.Session, error) {
	return a.sess, nil
}

type rowOutcomeFocus struct{ pops int }

func (f *rowOutcomeFocus) Pop() error                  { f.pops++; return nil }
func (*rowOutcomeFocus) Push(types.IBaseContext) error { return nil }

func TestReview_row_outcome_controller_refresh_warning(t *testing.T) {
	tabs, set := newConflictHookFixture(t, models.Row{Values: []any{int64(42), "loaded"}}, "id", "data")
	if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "data", OldValue: "loaded", NewValue: "mine"}); err != nil {
		t.Fatal(err)
	}
	sess := &rowOutcomeSession{refreshErr: errors.New("synthetic refresh failure")}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: rowOutcomeAcquirer{sess}})
	toast := ui.NewToastHelper(nil)
	ctx := guicontext.NewCommitDialogContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CommitDialogKey(), ViewName: string(guicontext.CommitDialogKey()), Kind: types.TEMPORARY_POPUP}), guicontext.Deps{})
	ctx.Open(set, &models.Connection{Name: "review"})
	focus := &rowOutcomeFocus{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, focus)
	ctrl.SetApplyHook(commitApplyHook{deps: commitDialogDeps{apply: helper, tabs: tabs, toast: toast}})
	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Fatal(err)
	}
	if ctx.Active() || focus.pops != 1 || !set.IsEmpty() || !strings.Contains(toast.Current(), "Writes committed") {
		t.Fatal("confirmed commit left a retryable popup or hid refresh warning")
	}
	queries := len(sess.queries)
	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Fatal(err)
	}
	if len(sess.queries) != queries {
		t.Fatal("closed popup replayed committed write")
	}
}

func TestReview_row_outcome_adapter(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		for _, refreshFails := range []bool{false, true} {
			name := "apply"
			if overwrite {
				name = "overwrite"
			}
			if refreshFails {
				name += "/refresh_failure"
			} else {
				name += "/refresh_success"
			}
			t.Run(name, func(t *testing.T) {
				tabs, set := newConflictHookFixture(t, models.Row{Values: []any{int64(42), "loaded"}}, "id", "data")
				if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "id", OldValue: int64(42), NewExpr: "id + 57", Kind: models.Expression}); err != nil {
					t.Fatal(err)
				}
				attempt := set.Edits()
				sess := &rowOutcomeSession{}
				if refreshFails {
					sess.refreshErr = errors.New("synthetic refresh failure")
				}
				helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: rowOutcomeAcquirer{sess}})
				toast := ui.NewToastHelper(nil)
				var log bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&log, nil))
				toast.SetLogger(logger)
				evicted := 0
				var err error
				if overwrite {
					err = (conflictOverwriteHook{deps: conflictDialogDeps{apply: helper, tabs: tabs, toast: toast, activeSetFunc: func() *models.PendingEditSet { return set }, evictCounts: func(string, string) { evicted++ }}}).Overwrite([]models.ConflictedEdit{{Edit: attempt[0]}}, nil)
				} else {
					err = (commitApplyHook{deps: commitDialogDeps{apply: helper, tabs: tabs, toast: toast, logger: logger, evictCounts: func(string, string) { evicted++ }}}).Apply(set, nil)
				}
				if err != nil {
					t.Fatalf("confirmed write was returned as retryable failure: %v", err)
				}
				if !set.IsEmpty() || evicted != 1 || !sess.closed {
					t.Fatalf("committed snapshot unsettled: pending=%d eviction=%d closed=%v", set.Count(), evicted, sess.closed)
				}
				row := tabs.Active().Grid().AllRows()[0]
				if refreshFails {
					if !strings.Contains(toast.Current(), "Writes committed") || toast.CurrentLevel() != ui.ToastError {
						t.Fatal("committed refresh failure lost warning")
					}
					if strings.Contains(log.String(), "synthetic refresh failure") {
						t.Fatal("raw backend failure copied to telemetry")
					}
					if row.Values[0] != int64(42) {
						t.Fatal("failed refresh invented display data")
					}
				} else if row.Values[0] != int64(99) || row.Values[1] != "server" {
					t.Fatalf("original grid row not replaced: %+v", row)
				}
			})
		}
	}
}

type liveRowOutcomeAcquirer struct {
	conn  drivers.Connection
	phase string
	calls int
}

func (a *liveRowOutcomeAcquirer) AcquireSession(ctx context.Context) (drivers.Session, error) {
	s, err := a.conn.AcquireSession(ctx)
	if err != nil {
		return nil, err
	}
	return &liveRowOutcomeSession{Session: s, owner: a}, nil
}

type liveRowOutcomeSession struct {
	drivers.Session
	owner *liveRowOutcomeAcquirer
}

func (s *liveRowOutcomeSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	s.owner.calls++
	if s.owner.phase == "refresh" && strings.HasPrefix(q.SQL, "SELECT *") {
		return models.Result{}, errors.New("synthetic post-COMMIT refresh failure")
	}
	res, err := s.Session.Execute(ctx, q)
	if err == nil && q.SQL == "COMMIT" && s.owner.phase == "commit" {
		return res, errors.New("synthetic lost COMMIT acknowledgment")
	}
	return res, err
}

func TestReview_row_outcome_adapter_live(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("PGSAVVY_TEST_PG unset; isolated fixture required")
	}
	for _, overwrite := range []bool{false, true} {
		phases := []string{"refresh", "commit"}
		if overwrite {
			phases = append(phases, "commit_partial")
		}
		for _, phase := range phases {
			name := phase + "/apply"
			if overwrite {
				name = phase + "/overwrite"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				factory := pg.New(nil)
				drv, err := factory(ctx)
				if err != nil {
					t.Fatal(err)
				}
				conn, err := drv.Open(ctx, models.Connection{Name: "review-outcome", Driver: "postgres", DSN: dsn}, nil)
				if err != nil {
					t.Fatal("fixture open failed")
				}
				t.Cleanup(func() { _ = conn.Close() })
				admin, err := conn.AcquireSession(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = admin.Close() })
				schema := fmt.Sprintf("review_receipt_%d", time.Now().UnixNano())
				qualified := pg.QuoteQualified(schema, "items")
				for _, sql := range []string{"CREATE SCHEMA " + pg.QuoteIdent(schema), "CREATE TABLE " + qualified + " (id integer PRIMARY KEY,data integer)", "INSERT INTO " + qualified + " VALUES (42,10),(7,20)"} {
					if _, err := admin.Execute(ctx, models.Query{SQL: sql}); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() {
					if _, err := admin.Execute(ctx, models.Query{SQL: "DROP SCHEMA " + pg.QuoteIdent(schema) + " CASCADE"}); err != nil {
						t.Error(err)
					}
				})
				tabs, set := newConflictHookFixture(t, models.Row{Values: []any{int32(42), int32(10)}}, "id", "data")
				set.Table = models.Ref{Schema: schema, Table: "items"}
				for _, e := range []models.PendingEdit{{PrimaryKey: []any{int32(42)}, Column: "id", OldValue: int32(42), Kind: models.Expression, NewExpr: "id + 57"}, {PrimaryKey: []any{int32(42)}, Column: "data", OldValue: int32(10), Kind: models.Expression, NewExpr: "data + 1"}} {
					if err := set.Add(e); err != nil {
						t.Fatal(err)
					}
				}
				attempt := set.Edits()
				acq := &liveRowOutcomeAcquirer{conn: conn, phase: phase}
				if phase == "commit_partial" {
					attempt = attempt[:1]
					acq.phase = "commit"
				}
				helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: acq})
				toast := ui.NewToastHelper(nil)
				apply := func() error {
					if overwrite {
						conflicts := make([]models.ConflictedEdit, len(attempt))
						for i, e := range attempt {
							conflicts[i].Edit = e
						}
						return (conflictOverwriteHook{deps: conflictDialogDeps{apply: helper, tabs: tabs, toast: toast, activeSetFunc: func() *models.PendingEditSet { return set }}}).Overwrite(conflicts, nil)
					}
					return (commitApplyHook{deps: commitDialogDeps{apply: helper, tabs: tabs, toast: toast}}).Apply(set, nil)
				}
				err = apply()
				if phase == "refresh" {
					if err != nil || !set.IsEmpty() || !strings.Contains(toast.Current(), "Writes committed") {
						t.Fatal("confirmed live write remained retryable or lost warning")
					}
				} else {
					if !errors.Is(err, models.ErrCommitOutcomeUnknown) || !set.CommitOutcomeUnknown() || set.Count() != 2 {
						t.Fatal("lost acknowledgment did not inhibit replay")
					}
					calls := acq.calls
					if err := apply(); !errors.Is(err, models.ErrCommitOutcomeUnknown) || acq.calls != calls {
						t.Fatal("uncertain expression was replayed")
					}
				}
				res, err := admin.Execute(ctx, models.Query{SQL: "SELECT id,data FROM " + qualified + " ORDER BY id"})
				if err != nil {
					t.Fatal(err)
				}
				wantData := int32(11)
				if phase == "commit_partial" {
					wantData = 10
				}
				if len(res.Rows) != 2 || res.Rows[0].Values[0] != int32(7) || res.Rows[0].Values[1] != int32(20) || res.Rows[1].Values[0] != int32(99) || res.Rows[1].Values[1] != wantData {
					t.Fatal("wrong durable result or expression applied twice")
				}
				if strings.HasPrefix(phase, "commit") {
					if err := set.ReconcileCommitOutcome(true); err != nil {
						t.Fatal(err)
					}
					wantPending := 0
					if phase == "commit_partial" {
						wantPending = 1
					}
					if set.Count() != wantPending || set.CommitOutcomeUnknown() {
						t.Fatal("explicit checked-server reconciliation failed")
					}
					if phase == "commit_partial" {
						if !set.HasEdit([]any{int32(99)}, "data") {
							t.Fatal("survivor retained obsolete identity")
						}
						if _, err := admin.Execute(ctx, models.Query{SQL: "INSERT INTO " + qualified + " VALUES (42,10)"}); err != nil {
							t.Fatal(err)
						}
						acq.phase = ""
						out, conflicts, err := helper.Apply(ctx, set, []string{"id"}, false)
						if err != nil || !out.Committed || len(conflicts) != 0 {
							t.Fatal("reconciled survivor did not commit")
						}
						check, err := admin.Execute(ctx, models.Query{SQL: "SELECT id,data FROM " + qualified + " WHERE id IN (42,99) ORDER BY id"})
						if err != nil {
							t.Fatal(err)
						}
						if len(check.Rows) != 2 || check.Rows[0].Values[0] != int32(42) || check.Rows[0].Values[1] != int32(10) || check.Rows[1].Values[0] != int32(99) || check.Rows[1].Values[1] != int32(11) {
							t.Fatal("survivor mutated reused key or replayed key expression")
						}
					}
				}
			})
		}
	}
}

func TestReview_row_outcome_adapter_matching_snapshot(t *testing.T) {
	tabs, set := newConflictHookFixture(t, models.Row{Values: []any{int64(42), "loaded"}}, "id", "data")
	e := models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "data", OldValue: "loaded", NewValue: "mine"}
	if err := set.Add(e); err != nil {
		t.Fatal(err)
	}
	sess := &rowOutcomeSession{onCommit: func() {
		e.NewValue = "replacement"
		if err := set.Add(e); err != nil {
			t.Fatal(err)
		}
		if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(7)}, Column: "data", NewValue: "unrelated"}); err != nil {
			t.Fatal(err)
		}
	}}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: rowOutcomeAcquirer{sess}})
	if err := (commitApplyHook{deps: commitDialogDeps{apply: helper, tabs: tabs}}).Apply(set, nil); err != nil {
		t.Fatal(err)
	}
	if set.Count() != 2 {
		t.Fatalf("receipt erased replacement/unrelated edits: %d", set.Count())
	}
}

func TestReview_row_outcome_partial_unknown_identity(t *testing.T) {
	tabs, set := newConflictHookFixture(t, models.Row{Values: []any{int64(42), "loaded"}}, "id", "data")
	for _, e := range []models.PendingEdit{{PrimaryKey: []any{int64(42)}, Column: "id", OldValue: int64(42), NewValue: int64(99)}, {PrimaryKey: []any{int64(42)}, Column: "data", OldValue: "loaded", NewValue: "mine"}} {
		if err := set.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	attempt := set.Edits()[:1]
	sess := &rowOutcomeSession{commitErr: errors.New("synthetic lost acknowledgment")}
	helper := helpers.NewCellApplyHelper(helpers.CellApplyDeps{Acquirer: rowOutcomeAcquirer{sess}})
	hook := conflictOverwriteHook{deps: conflictDialogDeps{apply: helper, tabs: tabs, activeSetFunc: func() *models.PendingEditSet { return set }}}
	if err := hook.Overwrite([]models.ConflictedEdit{{Edit: attempt[0]}}, nil); !errors.Is(err, models.ErrCommitOutcomeUnknown) {
		t.Fatal("partial overwrite lost unknown receipt")
	}
	// The caller has checked the server and confirmed the returned key 99.
	if err := set.ReconcileCommitOutcome(true); err != nil {
		t.Fatal(err)
	}
	if set.Count() != 1 || !set.HasEdit([]any{int64(99)}, "data") || set.HasEdit([]any{int64(42)}, "data") {
		t.Fatal("reconciliation left survivor addressing potentially reused old key")
	}
}
