package controllers_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeApplyHook records Apply invocations and returns the configured
// error on each call.
type fakeApplyHook struct {
	calls []applyArgs
	err   error
}

type applyArgs struct {
	Set  *models.PendingEditSet
	Conn *models.Connection
}

func (f *fakeApplyHook) Apply(set *models.PendingEditSet, conn *models.Connection) error {
	f.calls = append(f.calls, applyArgs{set, conn})
	return f.err
}

// fakeDryRunHook returns the configured report / error.
type fakeDryRunHook struct {
	calls  int
	report []guicontext.DryRunStmtResult
	err    error
}

func (f *fakeDryRunHook) DryRun(
	_ *models.PendingEditSet,
	_ *models.Connection,
) ([]guicontext.DryRunStmtResult, error) {
	f.calls++
	return f.report, f.err
}

// fakeShowSqlHook counts emissions.
type fakeShowSqlHook struct{ calls int }

func (f *fakeShowSqlHook) OnShowSQL(_ *models.PendingEditSet, _ *models.Connection) { f.calls++ }

// fakeCancelHook counts emissions.
type fakeCancelHook struct{ calls int }

func (f *fakeCancelHook) OnCancel() { f.calls++ }

// stagedSet returns a PendingEditSet pre-loaded with n one-column-per-
// row Literal edits on (schema, table). Defensive copy from the
// context-package helper kept package-local so the controller tests
// compile without reaching into pkg/gui/context internals.
func stagedSet(schema, table string, n int) *models.PendingEditSet {
	s := &models.PendingEditSet{Table: models.Ref{Schema: schema, Table: table}}
	for i := range n {
		_ = s.Add(models.PendingEdit{
			PrimaryKey: []any{int64(i + 1)},
			Column:     "name",
			OldValue:   "old",
			NewValue:   "new",
			Kind:       models.Literal,
			LoadedAt:   time.Now(),
		})
	}
	return s
}

func newCommitDialogTestCtx() *guicontext.CommitDialogContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.CommitDialogKey(),
		ViewName: string(guicontext.CommitDialogKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewCommitDialogContext(base, guicontext.Deps{})
}

// AC: keybindings cover [a], [d], [s], [c], [Esc] on COMMIT_DIALOG.
func TestCommitDialogController_Keybindings(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})

	scope := guicontext.CommitDialogKey()
	type sigKey struct {
		Action string
		Scope  types.ContextKey
		Mode   types.Mode
	}
	have := map[sigKey]bool{}
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		have[sigKey{kb.ActionID, kb.Scope, kb.Mode}] = true
	}

	want := []sigKey{
		{controllers.CommitDialogApply, scope, types.ModeNormal},
		{controllers.CommitDialogDryRun, scope, types.ModeNormal},
		{controllers.CommitDialogShowSql, scope, types.ModeNormal},
		{controllers.CommitDialogCancel, scope, types.ModeNormal},
		{controllers.CommitDialogTypeName, scope, types.ModeNormal},
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing binding for action=%s scope=%s mode=%v", w.Action, w.Scope, w.Mode)
		}
	}

	// Cancel is bound twice (`[Esc]` + `[c]`).
	cancelCount := 0
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.ActionID == controllers.CommitDialogCancel {
			cancelCount++
		}
	}
	if cancelCount != 2 {
		t.Errorf("CommitDialogCancel bound %d times; want 2 ([Esc] + [c])", cancelCount)
	}
}

// AC: [a] on default connection fires Apply, pops, and closes ctx.
func TestCommitDialogController_ApplyDefaultConnection(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	conn := &models.Connection{Name: "dev"}
	ctx.Open(stagedSet("public", "users", 1), conn)

	tree := &fakeFocusTree{}
	apply := &fakeApplyHook{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, tree)
	ctrl.SetApplyHook(apply)

	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(apply.calls) != 1 {
		t.Fatalf("apply.calls = %d, want 1", len(apply.calls))
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Apply; want false")
	}
}

// AC: confirm_writes:true connection blocks [a] until TypedName matches.
func TestCommitDialogController_ApplyConfirmWritesGate(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	conn := &models.Connection{Name: "prod", ConfirmWrites: true}
	ctx.Open(stagedSet("public", "users", 1), conn)

	tree := &fakeFocusTree{}
	apply := &fakeApplyHook{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, tree)
	ctrl.SetApplyHook(apply)

	// No typed name → Apply is a no-op.
	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(apply.calls) != 0 {
		t.Fatalf("apply.calls = %d before gate; want 0", len(apply.calls))
	}
	if tree.pops != 0 {
		t.Errorf("tree.pops = %d before gate; want 0", tree.pops)
	}

	// Wrong typed name → still blocked.
	ctx.SetTypedName("wrong")
	_ = ctrl.Apply(commands.ExecCtx{})
	if len(apply.calls) != 0 {
		t.Fatalf("apply.calls = %d on mismatch; want 0", len(apply.calls))
	}

	// Right typed name → Apply fires.
	ctx.SetTypedName("prod")
	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(apply.calls) != 1 {
		t.Fatalf("apply.calls = %d after gate; want 1", len(apply.calls))
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d after gate; want 1", tree.pops)
	}
}

// AC: GetDisabled predicate surfaces the right reason per failure.
func TestCommitDialogController_ApplyDisabledReasons(t *testing.T) {
	cases := []struct {
		name   string
		setup  func() *guicontext.CommitDialogContext
		reason string
	}{
		{
			name: "no dialog active",
			setup: func() *guicontext.CommitDialogContext {
				return newCommitDialogTestCtx()
			},
			reason: "no commit dialog active",
		},
		{
			name: "read-only connection",
			setup: func() *guicontext.CommitDialogContext {
				c := newCommitDialogTestCtx()
				c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev", ReadOnly: true})
				return c
			},
			reason: "read-only connection",
		},
		{
			name: "empty set",
			setup: func() *guicontext.CommitDialogContext {
				c := newCommitDialogTestCtx()
				c.Open(stagedSet("s", "t", 0), &models.Connection{Name: "dev"})
				return c
			},
			reason: "no staged edits",
		},
		{
			name: "confirm-writes typed-name mismatch",
			setup: func() *guicontext.CommitDialogContext {
				c := newCommitDialogTestCtx()
				c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})
				return c
			},
			reason: "press [t] and type the connection name to enable apply",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.setup()
			ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
			reg := commands.NewRegistry()
			ctrl.RegisterActions(reg)

			cmd, ok := reg.Get(controllers.CommitDialogApply)
			if !ok || cmd == nil {
				t.Fatal("CommitDialogApply not registered")
			}
			reason, disabled := cmd.Disabled(commands.ExecCtx{})
			if !disabled {
				t.Fatalf("Disabled = false; want true (%s)", tc.name)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

// AC: Apply hook error keeps dialog open so the user can retry.
func TestCommitDialogController_ApplyHookErrorKeepsDialogOpen(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})

	tree := &fakeFocusTree{}
	apply := &fakeApplyHook{err: errors.New("update failed")}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, tree)
	ctrl.SetApplyHook(apply)

	if err := ctrl.Apply(commands.ExecCtx{}); err == nil {
		t.Fatal("Apply: nil error; want wrapped apply error")
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after apply error; want still active")
	}
	if tree.pops != 0 {
		t.Errorf("tree.pops = %d after apply error; want 0", tree.pops)
	}
}

// AC: [d] flips mode and invokes the dry-run hook.
func TestCommitDialogController_DryRun(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})

	report := []guicontext.DryRunStmtResult{
		{SQL: "UPDATE x", RowsAffected: 3},
	}
	hook := &fakeDryRunHook{report: report}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
	ctrl.SetDryRunHook(hook)

	if err := ctrl.DryRun(commands.ExecCtx{}); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if ctx.Mode() != guicontext.CommitDialogDryRunResult {
		t.Errorf("Mode = %v, want CommitDialogDryRunResult", ctx.Mode())
	}
	if hook.calls != 1 {
		t.Errorf("hook.calls = %d, want 1", hook.calls)
	}
	got := ctx.DryRunResult()
	if len(got) != 1 || got[0].RowsAffected != 3 {
		t.Errorf("DryRunResult = %v, want one entry with RowsAffected=3", got)
	}
}

// AC: [d] without a hook still flips mode + clears stale results.
func TestCommitDialogController_DryRunNoHook(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})
	ctx.SetDryRunResult([]guicontext.DryRunStmtResult{{SQL: "stale"}})

	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
	if err := ctrl.DryRun(commands.ExecCtx{}); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if ctx.Mode() != guicontext.CommitDialogDryRunResult {
		t.Errorf("Mode = %v, want CommitDialogDryRunResult", ctx.Mode())
	}
	if got := ctx.DryRunResult(); got != nil {
		t.Errorf("DryRunResult = %v, want nil after no-hook DryRun", got)
	}
}

// AC: DryRun hook error stashes a single-entry report so the body
// still renders.
func TestCommitDialogController_DryRunHookErrorRendersErrorRow(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})

	hook := &fakeDryRunHook{err: errors.New("rollback failed")}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
	ctrl.SetDryRunHook(hook)

	_ = ctrl.DryRun(commands.ExecCtx{})
	got := ctx.DryRunResult()
	if len(got) != 1 || got[0].Err == nil {
		t.Errorf("DryRunResult = %v, want one entry with Err non-nil", got)
	}
}

// AC: [s] toggles SqlPreview mode and emits OnShowSQL once per
// enter-into-SqlPreview transition.
func TestCommitDialogController_ShowSqlToggle(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})

	hook := &fakeShowSqlHook{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
	ctrl.SetShowSqlHook(hook)

	// First press → SqlPreview, OnShowSQL fires.
	if err := ctrl.ShowSql(commands.ExecCtx{}); err != nil {
		t.Fatalf("ShowSql: %v", err)
	}
	if ctx.Mode() != guicontext.CommitDialogSqlPreview {
		t.Errorf("Mode after first [s] = %v, want SqlPreview", ctx.Mode())
	}
	if hook.calls != 1 {
		t.Errorf("OnShowSQL calls = %d after first toggle; want 1", hook.calls)
	}

	// Second press → back to Preview, no OnShowSQL (toggle-off).
	if err := ctrl.ShowSql(commands.ExecCtx{}); err != nil {
		t.Fatalf("ShowSql: %v", err)
	}
	if ctx.Mode() != guicontext.CommitDialogPreview {
		t.Errorf("Mode after second [s] = %v, want Preview", ctx.Mode())
	}
	if hook.calls != 1 {
		t.Errorf("OnShowSQL calls = %d after toggle-off; want still 1", hook.calls)
	}

	// Third press → SqlPreview again, OnShowSQL fires again.
	_ = ctrl.ShowSql(commands.ExecCtx{})
	if hook.calls != 2 {
		t.Errorf("OnShowSQL calls = %d after third toggle; want 2", hook.calls)
	}
}

// AC: [Esc] / [c] cancel without modifying PendingEditSet.
func TestCommitDialogController_CancelDoesNotMutatePendingSet(t *testing.T) {
	set := stagedSet("s", "t", 3)
	preCount := set.Count()

	ctx := newCommitDialogTestCtx()
	ctx.Open(set, &models.Connection{Name: "dev"})

	tree := &fakeFocusTree{}
	cancel := &fakeCancelHook{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, tree)
	ctrl.SetCancelHook(cancel)

	if err := ctrl.Cancel(commands.ExecCtx{}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancel.calls != 1 {
		t.Errorf("cancel.calls = %d, want 1", cancel.calls)
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Cancel; want false")
	}
	if got := set.Count(); got != preCount {
		t.Errorf("PendingEditSet.Count = %d after Cancel; want %d (unmodified)", got, preCount)
	}
}

// AC: stale dispatches (handlers fired after the popup is already
// closed) are silent no-ops.
func TestCommitDialogController_InactiveDispatchNoOps(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	tree := &fakeFocusTree{}
	apply := &fakeApplyHook{}
	dry := &fakeDryRunHook{}
	show := &fakeShowSqlHook{}
	cancel := &fakeCancelHook{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, tree)
	ctrl.SetApplyHook(apply)
	ctrl.SetDryRunHook(dry)
	ctrl.SetShowSqlHook(show)
	ctrl.SetCancelHook(cancel)

	// No Open call — context never activated.
	_ = ctrl.Apply(commands.ExecCtx{})
	_ = ctrl.DryRun(commands.ExecCtx{})
	_ = ctrl.ShowSql(commands.ExecCtx{})
	_ = ctrl.Cancel(commands.ExecCtx{})

	if len(apply.calls)+dry.calls+show.calls+cancel.calls+tree.pops != 0 {
		t.Errorf("stale dispatches produced apply=%d dry=%d show=%d cancel=%d pops=%d; want all 0",
			len(apply.calls), dry.calls, show.calls, cancel.calls, tree.pops)
	}
}

// AC: nil collaborators don't panic.
func TestCommitDialogController_NilCollaboratorsAreSafe(t *testing.T) {
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, nil, nil)
	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Errorf("Apply: %v", err)
	}
	if err := ctrl.DryRun(commands.ExecCtx{}); err != nil {
		t.Errorf("DryRun: %v", err)
	}
	if err := ctrl.ShowSql(commands.ExecCtx{}); err != nil {
		t.Errorf("ShowSql: %v", err)
	}
	if err := ctrl.Cancel(commands.ExecCtx{}); err != nil {
		t.Errorf("Cancel: %v", err)
	}
}

// AC: Apply with no hook still pops cleanly (graceful before A5 lands).
func TestCommitDialogController_ApplyNoHookPopsCleanly(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})

	tree := &fakeFocusTree{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, tree)
	// No SetApplyHook call.

	if err := ctrl.Apply(commands.ExecCtx{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1 (graceful pop without hook)", tree.pops)
	}
}

// AC: CommitDialogOpen is registered (so :w / <leader>cw resolve).
func TestCommitDialogController_OpenActionRegistered(t *testing.T) {
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, newCommitDialogTestCtx(), &fakeFocusTree{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	cmd, ok := reg.Get(controllers.CommitDialogOpen)
	if !ok || cmd == nil {
		t.Fatal("CommitDialogOpen not registered")
	}
	// Handler is a no-op until Z1 wires the real open path; just
	// assert calling it doesn't error.
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Errorf("CommitDialogOpen handler: %v", err)
	}
}

// === Render-layer tests (DefaultCommitDialogRender) =====================

// AC: Default render includes the header with icon + label + N + table.
func TestDefaultCommitDialogRender_HeaderShape(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:  stagedSet("public", "users", 2),
		Conn: &models.Connection{Name: "prod", Icon: "🔴", Label: "Production"},
		Mode: guicontext.CommitDialogPreview,
	}
	body := controllers.DefaultCommitDialogRender(view)
	for _, want := range []string{"🔴", "Production", "Commit 2 changes to public.users"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// AC: Header shows "1 change" (singular) for one edit.
func TestDefaultCommitDialogRender_HeaderSingular(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:  stagedSet("s", "t", 1),
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "Commit 1 change to") {
		t.Errorf("body missing singular wording:\n%s", body)
	}
}

// AC: Default render lists row diffs with PK + column-level changes.
func TestDefaultCommitDialogRender_RowDiffPreview(t *testing.T) {
	set := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "users"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(42)},
		Column:     "email",
		OldValue:   "alice@example.com",
		NewValue:   "bob@example.com",
		Kind:       models.Literal,
	})
	view := guicontext.CommitDialogView{
		Set:  set,
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "row 42") {
		t.Errorf("body missing row PK header: %s", body)
	}
	if !strings.Contains(body, "email") {
		t.Errorf("body missing column name: %s", body)
	}
	if !strings.Contains(body, "alice@example.com") || !strings.Contains(body, "bob@example.com") {
		t.Errorf("body missing diff values: %s", body)
	}
	if !strings.Contains(body, "→") {
		t.Errorf("body missing diff arrow: %s", body)
	}
}

// AC: a json/jsonb column whose OldValue pgx
// decoded as []byte renders as JSON text in the preview — matching the
// cell-editor seed and the new value — not Go's byte-slice form
// "[123 34 ...]".
func TestDefaultCommitDialogRender_JSONOldValue(t *testing.T) {
	set := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "events"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(7)},
		Column:     "payload",
		ColumnType: "jsonb",
		OldValue:   []byte(`{"a":1}`),
		NewValue:   `{"a":2}`,
		Kind:       models.Literal,
	})
	view := guicontext.CommitDialogView{
		Set:  set,
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, `{"a":1}`) {
		t.Errorf("body should render old json as text, got: %s", body)
	}
	if strings.Contains(body, "[123") {
		t.Errorf("body should not render old json as a byte slice, got: %s", body)
	}
}

// AC: Expression edits render with `(SQL expression)` suffix.
func TestDefaultCommitDialogRender_ExpressionSuffix(t *testing.T) {
	set := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "users"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(1)},
		Column:     "last_seen_at",
		NewExpr:    "now()",
		Kind:       models.Expression,
	})
	view := guicontext.CommitDialogView{
		Set:  set,
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "now()") {
		t.Errorf("body missing expression text: %s", body)
	}
	if !strings.Contains(body, "(SQL expression)") {
		t.Errorf("body missing `(SQL expression)` suffix: %s", body)
	}
}

// AC: Footer reminds the user that expressions execute server-side.
func TestDefaultCommitDialogRender_ExpressionFooter(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:  stagedSet("s", "t", 1),
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "expressions execute server-side at COMMIT") {
		t.Errorf("body missing server-side footer: %s", body)
	}
}

// AC: SqlPreview mode emits BEGIN/COMMIT envelope + IS NOT DISTINCT FROM.
func TestDefaultCommitDialogRender_SqlPreviewShape(t *testing.T) {
	set := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "users"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(7)},
		Column:     "email",
		OldValue:   "old",
		NewValue:   "new",
		Kind:       models.Literal,
	})
	view := guicontext.CommitDialogView{
		Set:  set,
		Conn: &models.Connection{Name: "dev"},
		Mode: guicontext.CommitDialogSqlPreview,
	}
	body := controllers.DefaultCommitDialogRender(view)
	for _, want := range []string{
		"BEGIN;",
		"COMMIT;",
		`UPDATE "public"."users"`,
		`SET "email" = $1`,
		"IS NOT DISTINCT FROM",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SqlPreview body missing %q:\n%s", want, body)
		}
	}
}

// AC: Expression edits in SQL preview render inline-unquoted (no $N
// for the new value).
func TestDefaultCommitDialogRender_SqlPreviewExpressionInline(t *testing.T) {
	set := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "users"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(1)},
		Column:     "last_seen_at",
		NewExpr:    "now()",
		Kind:       models.Expression,
	})
	view := guicontext.CommitDialogView{
		Set:  set,
		Conn: &models.Connection{Name: "dev"},
		Mode: guicontext.CommitDialogSqlPreview,
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, `SET "last_seen_at" = now()`) {
		t.Errorf("expression NOT rendered inline-unquoted:\n%s", body)
	}
	// Critical: the expression must NOT be wrapped in $1.
	if strings.Contains(body, `SET "last_seen_at" = $1`) {
		t.Errorf("expression incorrectly rendered as parameter:\n%s", body)
	}
}

// AC: SQL preview scrubs the connection password (ADR-28).
func TestDefaultCommitDialogRender_SqlPreviewScrubsPassword(t *testing.T) {
	// Construct a PendingEdit whose new value happens to embed a
	// password substring. The rendered SQL passes through
	// BuildCommitDialogSQL which calls strings.ReplaceAll(pw, "***").
	stmts := controllers.BuildCommitDialogSQL(stagedSet("s", "t", 1), "old")
	if len(stmts) != 1 {
		t.Fatalf("len(stmts) = %d, want 1", len(stmts))
	}
	// The OldValue "old" sits in the test PendingEdit; if it's used
	// as a password, every occurrence is replaced with "***".
	if strings.Contains(stmts[0], "old") && !strings.Contains(stmts[0], "***") {
		t.Errorf("password not scrubbed in: %s", stmts[0])
	}
}

// AC: 80×24 with 50 rows → `... (N more rows)` footer.
func TestDefaultCommitDialogRender_TruncationFooter(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:  stagedSet("public", "users", 50),
		Conn: &models.Connection{Name: "dev"},
		Mode: guicontext.CommitDialogPreview,
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "more rows") {
		t.Errorf("body missing truncation footer for 50-row set:\n%s", body)
	}
}

// AC: Wide diff value (>commitDialogValueWidth) truncates with ellipsis.
func TestDefaultCommitDialogRender_WideValueTruncates(t *testing.T) {
	wide := strings.Repeat("x", 200)
	set := &models.PendingEditSet{Table: models.Ref{Schema: "s", Table: "t"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(1)},
		Column:     "data",
		OldValue:   wide,
		NewValue:   "new",
		Kind:       models.Literal,
	})
	view := guicontext.CommitDialogView{
		Set:  set,
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "…") {
		t.Errorf("body missing ellipsis for wide value:\n%s", body)
	}
	// And the rendered line stays under 80 cols.
	for line := range strings.SplitSeq(body, "\n") {
		if len(line) > 120 {
			// 120 col budget — some lines (header, SqlPreview) may
			// run wider than 80, but a single rendered Literal value
			// should never blow past 120.
			t.Errorf("line longer than 120 cols (likely un-truncated):\n%s", line)
		}
	}
}

// AC: Typed-name gate renders the prompt when confirm_writes:true and
// the buffer doesn't match.
func TestDefaultCommitDialogRender_TypedNamePrompt(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:       stagedSet("s", "t", 1),
		Conn:      &models.Connection{Name: "prod", ConfirmWrites: true},
		TypedName: "pro",
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "prod") {
		t.Errorf("body missing expected connection name in prompt:\n%s", body)
	}
	if !strings.Contains(body, "pro") {
		t.Errorf("body missing typed-name buffer:\n%s", body)
	}
	if !strings.Contains(body, "[t]") {
		t.Errorf("body missing [t] hint for the typed-name prompt:\n%s", body)
	}
}

// AC: Typed-name match shows the "type match — [a] to apply" banner.
func TestDefaultCommitDialogRender_TypedNameMatch(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:       stagedSet("s", "t", 1),
		Conn:      &models.Connection{Name: "prod", ConfirmWrites: true},
		TypedName: "prod",
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "typed-name match") {
		t.Errorf("body missing match banner:\n%s", body)
	}
}

// AC: DryRunResult mode renders the per-statement rows-affected list.
func TestDefaultCommitDialogRender_DryRunListing(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:  stagedSet("s", "t", 1),
		Conn: &models.Connection{Name: "dev"},
		Mode: guicontext.CommitDialogDryRunResult,
		DryRunResult: []guicontext.DryRunStmtResult{
			{SQL: "UPDATE x", RowsAffected: 3},
			{SQL: "UPDATE y", RowsAffected: 0},
		},
	}
	body := controllers.DefaultCommitDialogRender(view)
	for _, want := range []string{"[3 rows]", "[0 rows]", "UPDATE x", "UPDATE y"} {
		if !strings.Contains(body, want) {
			t.Errorf("DryRunResult body missing %q:\n%s", want, body)
		}
	}
}

// AC: DryRunResult with a nil report points the user at [d].
func TestDefaultCommitDialogRender_DryRunNilHint(t *testing.T) {
	view := guicontext.CommitDialogView{
		Set:  stagedSet("s", "t", 1),
		Conn: &models.Connection{Name: "dev"},
		Mode: guicontext.CommitDialogDryRunResult,
	}
	body := controllers.DefaultCommitDialogRender(view)
	if !strings.Contains(body, "press [d]") {
		t.Errorf("body missing dry-run hint:\n%s", body)
	}
}

// AC: BuildCommitDialogSQL composite-PK quoting + multi-PK predicate.
func TestBuildCommitDialogSQL_CompositePK(t *testing.T) {
	set := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "memberships"}}
	_ = set.Add(models.PendingEdit{
		PrimaryKey: []any{int64(1), int64(2)},
		Column:     "role",
		OldValue:   "member",
		NewValue:   "admin",
		Kind:       models.Literal,
	})
	stmts := controllers.BuildCommitDialogSQL(set, "")
	if len(stmts) != 1 {
		t.Fatalf("len(stmts) = %d, want 1", len(stmts))
	}
	want := `UPDATE "public"."memberships" SET "role" = $1 WHERE "pk1" IS NOT DISTINCT FROM $2 AND "pk2" IS NOT DISTINCT FROM $3`
	if stmts[0] != want {
		t.Errorf("stmt = %q\nwant   %q", stmts[0], want)
	}
}

// AC: Mode toggle through ShowSql doesn't lose TypedName (re-check from
// the controller side; context-side covered separately).
func TestCommitDialogController_ShowSqlPreservesTypedName(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})
	ctx.SetTypedName("pro")

	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
	_ = ctrl.ShowSql(commands.ExecCtx{})
	if ctx.TypedName() != "pro" {
		t.Errorf("TypedName lost after [s]: %q", ctx.TypedName())
	}
	_ = ctrl.DryRun(commands.ExecCtx{})
	if ctx.TypedName() != "pro" {
		t.Errorf("TypedName lost after [d]: %q", ctx.TypedName())
	}
}

// capturingPromptHelper records the Prompt invocation (label, initial,
// callbacks) so tests can drive the submit/cancel path directly.
type capturingPromptHelper struct {
	label    string
	initial  string
	onSubmit func(value string) error
	onCancel func() error
	calls    int
}

func (f *capturingPromptHelper) Prompt(label, initial string, onSubmit func(string) error, onCancel func() error) error {
	f.calls++
	f.label = label
	f.initial = initial
	f.onSubmit = onSubmit
	f.onCancel = onCancel
	return nil
}

func (f *capturingPromptHelper) Submit(value string) error {
	if f.onSubmit == nil {
		return nil
	}
	return f.onSubmit(value)
}

func (f *capturingPromptHelper) Cancel() error {
	if f.onCancel == nil {
		return nil
	}
	return f.onCancel()
}

func (f *capturingPromptHelper) SetResetHandler(func(initial string)) {}

// AC: [t] opens the typed-name prompt (label names the connection,
// initial seeds the current buffer); submit fills the gate so [a]
// becomes enabled AND re-pushes the dialog — PROMPT is a
// TEMPORARY_POPUP, so pushing it evicted the dialog from the focus
// stack (ContextTree popup-replaces-popup semantics).
func TestCommitDialogController_TypeNamePromptFillsGate(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})

	prompt := &capturingPromptHelper{}
	tree := &fakeFocusTree{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{Prompt: prompt}, controllers.EditDeps{}, ctx, tree)

	if err := ctrl.TypeName(commands.ExecCtx{}); err != nil {
		t.Fatalf("TypeName: %v", err)
	}
	if prompt.calls != 1 {
		t.Fatalf("prompt.calls = %d, want 1", prompt.calls)
	}
	if !strings.Contains(prompt.label, "prod") {
		t.Errorf("prompt label %q does not name the connection", prompt.label)
	}
	if prompt.initial != "" {
		t.Errorf("prompt initial = %q, want empty (no prior buffer)", prompt.initial)
	}

	if err := prompt.Submit("prod"); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ctx.TypedName() != "prod" {
		t.Errorf("TypedName = %q after submit, want %q", ctx.TypedName(), "prod")
	}
	if !ctx.ApplyEnabled() {
		t.Error("ApplyEnabled() = false after matching submit; want true")
	}
	if tree.pushes != 1 || tree.pushedCtx != types.IBaseContext(ctx) {
		t.Errorf("dialog not re-pushed after submit: pushes=%d pushedCtx=%v", tree.pushes, tree.pushedCtx)
	}
}

// AC: cancelling the typed-name prompt restores the dialog (re-push)
// without touching the buffer.
func TestCommitDialogController_TypeNameCancelRestoresDialog(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})
	ctx.SetTypedName("pro")

	prompt := &capturingPromptHelper{}
	tree := &fakeFocusTree{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{Prompt: prompt}, controllers.EditDeps{}, ctx, tree)

	if err := ctrl.TypeName(commands.ExecCtx{}); err != nil {
		t.Fatalf("TypeName: %v", err)
	}
	if err := prompt.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if ctx.TypedName() != "pro" {
		t.Errorf("TypedName = %q after cancel, want untouched %q", ctx.TypedName(), "pro")
	}
	if tree.pushes != 1 || tree.pushedCtx != types.IBaseContext(ctx) {
		t.Errorf("dialog not re-pushed after cancel: pushes=%d pushedCtx=%v", tree.pushes, tree.pushedCtx)
	}
}

// AC: re-invoking [t] seeds the prompt with the previous attempt so the
// user can fix a typo instead of retyping from scratch.
func TestCommitDialogController_TypeNameReseedsPriorAttempt(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})
	ctx.SetTypedName("prdo")

	prompt := &capturingPromptHelper{}
	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{Prompt: prompt}, controllers.EditDeps{}, ctx, &fakeFocusTree{})

	if err := ctrl.TypeName(commands.ExecCtx{}); err != nil {
		t.Fatalf("TypeName: %v", err)
	}
	if prompt.initial != "prdo" {
		t.Errorf("prompt initial = %q, want prior attempt %q", prompt.initial, "prdo")
	}
}

// AC: TypeName is registered with a GetDisabled predicate covering the
// inactive-dialog and confirmation-not-required states.
func TestCommitDialogController_TypeNameDisabledReasons(t *testing.T) {
	cases := []struct {
		name   string
		setup  func() *guicontext.CommitDialogContext
		reason string
	}{
		{
			name: "no dialog active",
			setup: func() *guicontext.CommitDialogContext {
				return newCommitDialogTestCtx()
			},
			reason: "no commit dialog active",
		},
		{
			name: "typed confirmation not required",
			setup: func() *guicontext.CommitDialogContext {
				c := newCommitDialogTestCtx()
				c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "dev"})
				return c
			},
			reason: "typed confirmation not required on this connection",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.setup()
			ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
			reg := commands.NewRegistry()
			ctrl.RegisterActions(reg)

			cmd, ok := reg.Get(controllers.CommitDialogTypeName)
			if !ok || cmd == nil {
				t.Fatal("CommitDialogTypeName not registered")
			}
			reason, disabled := cmd.Disabled(commands.ExecCtx{})
			if !disabled {
				t.Fatalf("Disabled = false; want true (%s)", tc.name)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

// AC: TypeName with no Prompt helper wired is a safe no-op.
func TestCommitDialogController_TypeNameNilPromptIsSafe(t *testing.T) {
	ctx := newCommitDialogTestCtx()
	ctx.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})

	ctrl := controllers.NewCommitDialogController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, controllers.EditDeps{}, ctx, &fakeFocusTree{})
	if err := ctrl.TypeName(commands.ExecCtx{}); err != nil {
		t.Fatalf("TypeName with nil prompt: %v", err)
	}
}
