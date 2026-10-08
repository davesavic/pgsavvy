package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

func siblingCommitFixture(t *testing.T, conn drivers.Connection) (asyncCommitHooks, *models.PendingEditSet, *ui.Tab, *ui.Tab) {
	t.Helper()
	h, set, _ := asyncCommitFixture(t, conn)
	h.g.resultTabsH = h.deps.tabs
	h.activeSet = h.g.wireEditDeps().ActivePendingEditSet
	origin := h.deps.tabs.Active()
	identity := query.ResultIdentity{BaseTable: "t", HasRowIdentity: true}
	origin.SetIdentity("A", identity)
	origin.Grid().SetEditability(true, []int{0}, "", set.Table)
	if h.activeSet() != set {
		t.Fatal("origin did not resolve the connection/table pending set")
	}
	if err := h.deps.tabs.OpenResultTab("sibling", nil); err != nil {
		t.Fatal(err)
	}
	sibling := h.deps.tabs.Active()
	sibling.SetIdentity("A", identity)
	sibling.Grid().SetColumns(origin.Grid().Columns())
	sibling.Grid().AppendRows([]models.Row{{Values: []any{int64(42), "loaded"}}})
	sibling.Grid().SetEditability(true, []int{0}, "", set.Table)
	sibling.Grid().SetCursor(0, 1)
	h.deps.tabs.SwitchToTabByID(fmt.Sprint(origin.ID()))
	set.Clear()
	if err := set.Add(models.PendingEdit{PrimaryKey: []any{int64(42)}, Column: "id", OldValue: int64(42), NewValue: int64(99), Kind: models.Literal}); err != nil {
		t.Fatal(err)
	}
	return h, set, origin, sibling
}

func sampleSiblingEdit(t *testing.T, h asyncCommitHooks) (controllers.PendingEditStore, models.PendingEdit) {
	t.Helper()
	sample := &cellEditorSample{}
	picker := cellEditorPicker{tabs: h.deps.tabs, sample: sample}
	value, col, pk, ok := picker.CellSnapshot()
	if !ok || len(pk) != 1 {
		t.Fatal("cell sampling failed")
	}
	store := cellEditorStore{resolve: h.activeSet, sample: sample}
	return store.Capture(), models.PendingEdit{PrimaryKey: pk, Column: col.Name, OldValue: value, NewExpr: "data || 'next'", Kind: models.Expression}
}

func siblingEditor(h asyncCommitHooks) (*controllers.CellEditorController, *guicontext.CellEditorContext, *asyncCellEditorFocus) {
	sample := &cellEditorSample{}
	ctx := guicontext.NewCellEditorContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: guicontext.CellEditorKey(), ViewName: "editor", Kind: types.TEMPORARY_POPUP}), guicontext.Deps{})
	focus := &asyncCellEditorFocus{}
	ctrl := controllers.NewCellEditorController(nil, controllers.CoreDeps{}, controllers.UIDeps{Toast: h.deps.toast}, ctx, focus,
		cellEditorPicker{tabs: h.deps.tabs, sample: sample}, cellEditorStore{resolve: h.activeSet, sample: sample})
	return ctrl, ctx, focus
}

func TestCommitSiblingBaselinesRejectStaleEditors(t *testing.T) {
	for _, mode := range []string{"apply", "overwrite", "refresh_failure"} {
		for _, timing := range []string{"before_settlement", "after_settlement"} {
			t.Run(mode+"/"+timing, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				defer asyncEnsureReleased(release)
				sess := &rowOutcomeSession{onCommit: func() { close(entered); <-release }}
				if mode == "refresh_failure" {
					sess.refreshErr = errors.New("refetch failed")
				}
				h, set, origin, sibling := siblingCommitFixture(t, &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return sess, nil }})
				op, err := h.capture(set, &models.Connection{Name: "A"}, nil, mode == "overwrite")
				if err != nil {
					t.Fatal(err)
				}
				h.launch(op, false, mode == "overwrite", func(commitReceipt) error { return nil })
				asyncAwait(t, entered)
				h.deps.tabs.SwitchToTabByID(fmt.Sprint(sibling.ID()))
				var captured controllers.PendingEditStore
				var edit models.PendingEdit
				if timing == "before_settlement" {
					captured, edit = sampleSiblingEdit(t, h)
				}
				close(release)
				asyncAwait(t, op.done)
				// Do not publish the queued UI receipt: safety must precede it.
				if set.CommitInFlight() || !set.IsEmpty() {
					t.Fatal("committed reservation was not retired")
				}
				if timing == "after_settlement" {
					captured, edit = sampleSiblingEdit(t, h)
				}
				if err := captured.Add(edit); err == nil || !set.IsEmpty() {
					t.Fatalf("obsolete sibling key staged after settlement: err=%v count=%d", err, set.Count())
				}
				if timing == "before_settlement" {
					if err := captured.Remove(edit.PrimaryKey, edit.Column); !errors.Is(err, models.ErrCommitSnapshotStale) {
						t.Fatalf("retained sibling discard: %v", err)
					}
				}
				ctrl, ctx, _ := siblingEditor(h)
				if err := ctrl.Enter(commands.ExecCtx{}); err != nil || ctx.Active() || sibling.Grid().Editable() || h.activeSet() != nil {
					t.Fatal("fresh editor authorized obsolete sibling values")
				}
				if !strings.Contains(sibling.Grid().DisabledReason(), "query refresh") {
					t.Fatal("sibling does not explain how to restore editing")
				}
				sibling.Grid().SetEditability(true, []int{0}, "", set.Table)
				if sibling.Grid().Editable() {
					t.Fatal("metadata-only refresh reauthorized stale rows")
				}
				if mode != "refresh_failure" && (origin.Grid().AllRows()[0].Values[0] != int64(99) || !origin.Grid().Editable()) {
					t.Fatal("origin lost its authoritative receipt")
				}
			})
		}
	}
}

func TestCommitSiblingBaselineIsolation(t *testing.T) {
	for _, mode := range []string{"unattached", "other_connection", "other_table", "other_schema", "new_result", "new_identity", "connection_switch", "dry_run", "unknown", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			sess := &rowOutcomeSession{}
			if mode == "unknown" {
				sess.commitErr = errors.New("lost acknowledgement")
			}
			h, set, _, sibling := siblingCommitFixture(t, &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) {
				close(entered)
				<-release
				if mode == "rollback" {
					return nil, errors.New("acquisition failed before writing")
				}
				return sess, nil
			}})
			gv := sibling.Grid()
			switch mode {
			case "other_connection":
				sibling.SetIdentity("B", query.ResultIdentity{HasRowIdentity: true})
				gv.SetEditability(true, []int{0}, "", set.Table)
			case "other_table":
				gv.SetEditability(true, []int{0}, "", models.Ref{Schema: "app", Table: "other"})
			case "other_schema":
				gv.SetEditability(true, []int{0}, "", models.Ref{Schema: "other", Table: "t"})
			}
			if gv.PendingEdits() != nil {
				t.Fatal("sibling fixture unexpectedly attached the pending set")
			}
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			h.launch(op, mode == "dry_run", false, func(commitReceipt) error { return nil })
			asyncAwait(t, entered)
			switch mode {
			case "new_result":
				gv.SetColumns(gv.Columns())
				gv.AppendRows([]models.Row{{Values: []any{int64(99), "new result"}}})
				gv.SetEditability(true, []int{0}, "", set.Table)
			case "new_identity":
				sibling.SetIdentity("B", query.ResultIdentity{HasRowIdentity: true})
				gv.SetEditability(true, []int{0}, "", set.Table)
			case "connection_switch":
				h.g.connectionState.activeConnID = "B"
				h.g.connectionState.activeConnProfile = &models.Connection{Name: "B"}
			}
			_, _, _, baseline, _ := gv.CellEditSnapshot()
			close(release)
			asyncAwait(t, op.done)
			_, _, _, settled, _ := gv.CellEditSnapshot()
			invalidated := mode == "unattached" || mode == "connection_switch"
			if invalidated {
				if gv.Editable() || baseline == settled {
					t.Fatal("original connection/table sibling baseline survived commit")
				}
			} else if !gv.Editable() || baseline != settled {
				t.Fatal("unrelated or replacement baseline was changed")
			}
			if mode == "unknown" {
				if _, err := set.BeginCommit(nil); !errors.Is(err, models.ErrCommitOutcomeUnknown) {
					t.Fatalf("unknown outcome became replayable: %v", err)
				}
			}
		})
	}
}

func TestCommitSiblingEditorRecovery(t *testing.T) {
	for _, action := range []string{"commit", "discard_before_pop", "discard_during_pop"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			defer asyncEnsureReleased(release)
			sess := &rowOutcomeSession{onCommit: func() { close(entered); <-release }}
			h, set, _, sibling := siblingCommitFixture(t, &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return sess, nil }})
			op, err := h.capture(set, &models.Connection{Name: "A"}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			h.launch(op, false, false, func(commitReceipt) error { return nil })
			asyncAwait(t, entered)
			h.deps.tabs.SwitchToTabByID(fmt.Sprint(sibling.ID()))
			pk := []any{int64(100)}
			sibling.Grid().AppendRows([]models.Row{{Values: []any{pk[0], "other row"}}})
			sibling.Grid().SetCursor(1, 1)
			if action != "commit" {
				if err := set.Add(models.PendingEdit{PrimaryKey: pk, Column: "data", OldValue: "other row", NewValue: "staged", Kind: models.Literal}); err != nil {
					t.Fatal(err)
				}
			}
			ctrl, ctx, focus := siblingEditor(h)
			if err := ctrl.Enter(commands.ExecCtx{}); err != nil || !ctx.Active() {
				t.Fatal("sibling editor did not open")
			}
			ctx.SetBuffer("recoverable draft")
			settle := func() { close(release); asyncAwait(t, op.done) }
			if action == "commit" {
				settle()
				if err := ctrl.Commit(commands.ExecCtx{}); !errors.Is(err, models.ErrCommitSnapshotStale) || !ctx.Active() || ctx.Buffer() != "recoverable draft" || focus.pops != 0 || set.HasEdit(pk, "data") {
					t.Fatal("stale sibling submission lost its draft or staged obsolete intent")
				}
			} else {
				if action == "discard_before_pop" {
					settle()
				} else {
					focus.onPop = settle
				}
				if err := ctrl.Discard(commands.ExecCtx{}); !errors.Is(err, models.ErrCommitSnapshotStale) || !set.HasEdit(pk, "data") || ctx.Active() || focus.pops != 1 || strings.Contains(h.deps.toast.Current(), "discarded pending edit") {
					t.Fatal("stale sibling discard removed pending intent or reported success")
				}
			}
		})
	}
}

// This session evaluates the UPDATE predicates against row state, so a reused
// key with matching old values really would be written if stale staging passed.
type siblingRowSession struct {
	drivers.Session
	rows    map[int64]string
	tx      map[int64]string
	updates []int64
}

func (s *siblingRowSession) Close() error { return nil }

func (s *siblingRowSession) Execute(_ context.Context, q models.Query) (models.Result, error) {
	switch {
	case q.SQL == "BEGIN":
		s.tx = make(map[int64]string, len(s.rows))
		for pk, value := range s.rows {
			s.tx[pk] = value
		}
	case q.SQL == "COMMIT":
		s.rows = s.tx
		s.tx = nil
	case q.SQL == "ROLLBACK":
		s.tx = nil
	case strings.HasPrefix(q.SQL, `UPDATE "app"."t" SET "id" = $1`):
		pk, next := q.Args[1].(int64), q.Args[0].(int64)
		value, exists := s.tx[pk]
		if !exists || q.Args[2] != pk {
			return models.Result{}, nil
		}
		delete(s.tx, pk)
		s.tx[next] = value
		s.updates = append(s.updates, pk)
		return models.Result{RowsAffected: 1, Rows: []*models.Row{{Values: []any{next}}}}, nil
	case strings.HasPrefix(q.SQL, `UPDATE "app"."t" SET "data" = data || 'next'`):
		pk := q.Args[0].(int64)
		value, exists := s.tx[pk]
		guarded := strings.Contains(q.SQL, "IS NOT DISTINCT FROM")
		if !exists || (guarded && q.Args[1] != value) {
			return models.Result{}, nil
		}
		s.tx[pk] = value + "next"
		s.updates = append(s.updates, pk)
		return models.Result{RowsAffected: 1, Rows: []*models.Row{{Values: []any{pk}}}}, nil
	case strings.HasPrefix(q.SQL, "SELECT *"):
		result := models.Result{Columns: []*models.Column{{Name: "id"}, {Name: "data"}}}
		for _, arg := range q.Args {
			pk := arg.(int64)
			if value, ok := s.rows[pk]; ok {
				result.Rows = append(result.Rows, &models.Row{Values: []any{pk, value}})
			}
		}
		return result, nil
	default:
		return models.Result{}, fmt.Errorf("unexpected test query: %s", q.SQL)
	}
	return models.Result{}, nil
}

func TestCommitSiblingCannotWriteReusedKey(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		for _, timing := range []string{"before_settlement", "after_settlement"} {
			t.Run(fmt.Sprintf("overwrite=%t/%s", overwrite, timing), func(t *testing.T) {
				sess := &siblingRowSession{rows: map[int64]string{42: "loaded"}}
				h, set, origin, sibling := siblingCommitFixture(t, &asyncCommitConnection{acquire: func(context.Context) (drivers.Session, error) { return sess, nil }})
				var captured controllers.PendingEditStore
				var edit models.PendingEdit
				if timing == "before_settlement" {
					h.deps.tabs.SwitchToTabByID(fmt.Sprint(sibling.ID()))
					captured, edit = sampleSiblingEdit(t, h)
					h.deps.tabs.SwitchToTabByID(fmt.Sprint(origin.ID()))
				}
				commit := func(overwrite bool) {
					t.Helper()
					op, err := h.capture(set, &models.Connection{Name: "A"}, nil, overwrite)
					if err != nil {
						t.Fatal(err)
					}
					h.launch(op, false, overwrite, func(commitReceipt) error { return nil })
					asyncAwait(t, op.done)
					if !set.IsEmpty() || set.CommitOutcomeUnknown() {
						t.Fatal("test commit did not settle successfully")
					}
				}
				commit(false)
				sess.rows[42] = "loaded" // A replacement row satisfies the old-value guard.
				h.deps.tabs.SwitchToTabByID(fmt.Sprint(sibling.ID()))
				if timing == "after_settlement" {
					captured, edit = sampleSiblingEdit(t, h)
				}
				stageErr := captured.Add(edit)
				if stageErr == nil {
					commit(overwrite)
				}
				if sess.rows[42] != "loaded" || len(sess.updates) != 1 || stageErr == nil {
					t.Fatalf("stale sibling wrote a replacement row: rows=%v updates=%v stageErr=%v", sess.rows, sess.updates, stageErr)
				}
				gv := sibling.Grid()
				gv.SetColumns(gv.Columns())
				gv.AppendRows([]models.Row{{Values: []any{int64(99), sess.rows[99]}}})
				gv.SetEditability(true, []int{0}, "", set.Table)
				gv.SetCursor(0, 1)
				fresh, edit := sampleSiblingEdit(t, h)
				if err := fresh.Add(edit); err != nil {
					t.Fatalf("query refresh did not restore legitimate editing: %v", err)
				}
				commit(overwrite)
				if sess.rows[42] != "loaded" || sess.rows[99] != "loadednext" || len(sess.updates) != 2 || sess.updates[1] != 99 {
					t.Fatalf("fresh edit targeted the wrong row: rows=%v updates=%v", sess.rows, sess.updates)
				}
				if origin.Grid().Editable() {
					t.Fatal("unchanged-key commit left the other tab's old value editable")
				}
			})
		}
	}
}
