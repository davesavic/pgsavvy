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

// fakeRefreshHook records Refresh invocations and returns the configured
// error.
type fakeRefreshHook struct {
	calls []refreshArgs
	err   error
}

type refreshArgs struct {
	Conflicts []models.ConflictedEdit
	Conn      *models.Connection
}

func (f *fakeRefreshHook) Refresh(c []models.ConflictedEdit, conn *models.Connection) error {
	f.calls = append(f.calls, refreshArgs{c, conn})
	return f.err
}

// fakeOverwriteHook records Overwrite invocations and returns the
// configured error.
type fakeOverwriteHook struct {
	calls []refreshArgs
	err   error
}

func (f *fakeOverwriteHook) Overwrite(c []models.ConflictedEdit, conn *models.Connection) error {
	f.calls = append(f.calls, refreshArgs{c, conn})
	return f.err
}

// conflictBatch builds n distinct ConflictedEdits with PK i+1 and a
// non-equal server-now value (so isAlreadyApplied does not fire).
func conflictBatch(n int) []models.ConflictedEdit {
	out := make([]models.ConflictedEdit, 0, n)
	for i := range n {
		out = append(out, models.ConflictedEdit{
			Edit: models.PendingEdit{
				PrimaryKey: []any{int64(i + 1)},
				Column:     "name",
				OldValue:   "old",
				NewValue:   "new",
				Kind:       models.Literal,
				LoadedAt:   time.Now(),
			},
			ServerValue: "server-now",
			LoadedAt:    time.Now(),
		})
	}
	return out
}

func newConflictDialogTestCtx() *guicontext.ConflictDialogContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.ConflictDialogKey(),
		ViewName: string(guicontext.ConflictDialogKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewConflictDialogContext(base, guicontext.Deps{})
}

// AC: [r] and [Esc] bound on CONFLICT_DIALOG; [o] also bound on default
// (non-confirm_writes) connections.
func TestConflictDialogController_KeybindingsDefaultConn(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(1), &models.Connection{Name: "dev"})

	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, &fakeFocusTree{})

	scope := guicontext.ConflictDialogKey()
	have := map[string]bool{}
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.Scope != scope {
			t.Errorf("scope = %s, want %s", kb.Scope, scope)
		}
		have[kb.ActionID] = true
	}
	for _, id := range []string{
		controllers.ConflictDialogRefresh,
		controllers.ConflictDialogOverwrite,
		controllers.ConflictDialogCancel,
	} {
		if !have[id] {
			t.Errorf("missing binding for action=%s", id)
		}
	}
}

// AC: [o] binding is OMITTED on confirm_writes:true connections.
func TestConflictDialogController_KeybindingsHidesOverwriteOnConfirmWrites(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(1), &models.Connection{Name: "prod", ConfirmWrites: true})

	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, &fakeFocusTree{})

	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.ActionID == controllers.ConflictDialogOverwrite {
			t.Errorf("[o] still bound on confirm_writes connection: %+v", kb)
		}
	}
}

// AC: [r] invokes Refresh, pops, and closes ctx.
func TestConflictDialogController_RefreshFiresHookAndPops(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	conn := &models.Connection{Name: "dev"}
	batch := conflictBatch(2)
	_ = ctx.Open(batch, conn)

	tree := &fakeFocusTree{}
	hook := &fakeRefreshHook{}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetRefreshHook(hook)

	if err := ctrl.Refresh(commands.ExecCtx{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(hook.calls) != 1 {
		t.Fatalf("hook.calls = %d, want 1", len(hook.calls))
	}
	if len(hook.calls[0].Conflicts) != 2 {
		t.Errorf("hook Conflicts len = %d, want 2", len(hook.calls[0].Conflicts))
	}
	if hook.calls[0].Conn != conn {
		t.Errorf("hook Conn = %p, want %p", hook.calls[0].Conn, conn)
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Refresh; want false")
	}
}

// AC: [o] invokes Overwrite, pops, and closes ctx (default connection).
func TestConflictDialogController_OverwriteFiresHookAndPops(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	conn := &models.Connection{Name: "dev"}
	_ = ctx.Open(conflictBatch(1), conn)

	tree := &fakeFocusTree{}
	hook := &fakeOverwriteHook{}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetOverwriteHook(hook)

	if err := ctrl.Overwrite(commands.ExecCtx{}); err != nil {
		t.Fatalf("Overwrite: %v", err)
	}
	if len(hook.calls) != 1 {
		t.Fatalf("hook.calls = %d, want 1", len(hook.calls))
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Overwrite; want false")
	}
}

// AC: [o] is a no-op on confirm_writes:true connections (defence in
// depth — even if the binding somehow fires, the handler refuses).
func TestConflictDialogController_OverwriteNoOpOnConfirmWrites(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(1), &models.Connection{Name: "prod", ConfirmWrites: true})

	tree := &fakeFocusTree{}
	hook := &fakeOverwriteHook{}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetOverwriteHook(hook)

	if err := ctrl.Overwrite(commands.ExecCtx{}); err != nil {
		t.Fatalf("Overwrite: %v", err)
	}
	if len(hook.calls) != 0 {
		t.Errorf("hook.calls = %d on confirm_writes; want 0", len(hook.calls))
	}
	if tree.pops != 0 {
		t.Errorf("tree.pops = %d on confirm_writes; want 0", tree.pops)
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after blocked Overwrite; want still active")
	}
}

// AC: Hook errors keep the dialog open so the user can retry.
func TestConflictDialogController_RefreshHookErrorKeepsDialogOpen(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(1), &models.Connection{Name: "dev"})

	tree := &fakeFocusTree{}
	hook := &fakeRefreshHook{err: errors.New("network")}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetRefreshHook(hook)

	if err := ctrl.Refresh(commands.ExecCtx{}); err == nil {
		t.Fatal("Refresh: nil error; want wrapped refresh error")
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after refresh error; want still active")
	}
	if tree.pops != 0 {
		t.Errorf("tree.pops = %d after refresh error; want 0", tree.pops)
	}
}

func TestConflictDialogController_OverwriteHookErrorKeepsDialogOpen(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(1), &models.Connection{Name: "dev"})

	tree := &fakeFocusTree{}
	hook := &fakeOverwriteHook{err: errors.New("constraint violation")}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetOverwriteHook(hook)

	if err := ctrl.Overwrite(commands.ExecCtx{}); err == nil {
		t.Fatal("Overwrite: nil error; want wrapped overwrite error")
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after overwrite error; want still active")
	}
}

// AC: [Esc] cancels without invoking any apply / refresh hook and the
// PendingEditSet remains untouched.
func TestConflictDialogController_CancelPopsWithoutMutation(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(2), &models.Connection{Name: "dev"})

	tree := &fakeFocusTree{}
	cancel := &fakeCancelHook{}
	refresh := &fakeRefreshHook{}
	overwrite := &fakeOverwriteHook{}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetCancelHook(cancel)
	ctrl.SetRefreshHook(refresh)
	ctrl.SetOverwriteHook(overwrite)

	if err := ctrl.Cancel(commands.ExecCtx{}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancel.calls != 1 {
		t.Errorf("cancel.calls = %d, want 1", cancel.calls)
	}
	if len(refresh.calls)+len(overwrite.calls) != 0 {
		t.Errorf("apply-side hooks fired on cancel: refresh=%d overwrite=%d",
			len(refresh.calls), len(overwrite.calls))
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Cancel; want false")
	}
}

// AC: stale dispatches (handlers fired after the popup is already
// closed) are silent no-ops.
func TestConflictDialogController_InactiveDispatchNoOps(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	tree := &fakeFocusTree{}
	refresh := &fakeRefreshHook{}
	overwrite := &fakeOverwriteHook{}
	cancel := &fakeCancelHook{}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)
	ctrl.SetRefreshHook(refresh)
	ctrl.SetOverwriteHook(overwrite)
	ctrl.SetCancelHook(cancel)

	// No Open call — context never activated.
	_ = ctrl.Refresh(commands.ExecCtx{})
	_ = ctrl.Overwrite(commands.ExecCtx{})
	_ = ctrl.Cancel(commands.ExecCtx{})

	if len(refresh.calls)+len(overwrite.calls)+cancel.calls+tree.pops != 0 {
		t.Errorf("stale dispatches produced refresh=%d overwrite=%d cancel=%d pops=%d; want all 0",
			len(refresh.calls), len(overwrite.calls), cancel.calls, tree.pops)
	}
}

// AC: nil collaborators don't panic.
func TestConflictDialogController_NilCollaboratorsAreSafe(t *testing.T) {
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, nil, nil)
	if err := ctrl.Refresh(commands.ExecCtx{}); err != nil {
		t.Errorf("Refresh: %v", err)
	}
	if err := ctrl.Overwrite(commands.ExecCtx{}); err != nil {
		t.Errorf("Overwrite: %v", err)
	}
	if err := ctrl.Cancel(commands.ExecCtx{}); err != nil {
		t.Errorf("Cancel: %v", err)
	}
}

// AC: Refresh/Overwrite with no hook still pops cleanly (graceful
// before A5 wires the hooks).
func TestConflictDialogController_NoHookPopsCleanly(t *testing.T) {
	ctx := newConflictDialogTestCtx()
	_ = ctx.Open(conflictBatch(1), &models.Connection{Name: "dev"})
	tree := &fakeFocusTree{}
	ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, tree)

	if err := ctrl.Refresh(commands.ExecCtx{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d after Refresh, want 1", tree.pops)
	}
}

// AC: GetDisabled predicate reports confirm_writes blocks overwrite.
func TestConflictDialogController_OverwriteDisabledReasons(t *testing.T) {
	cases := []struct {
		name   string
		setup  func() *guicontext.ConflictDialogContext
		reason string
	}{
		{
			name: "no dialog active",
			setup: func() *guicontext.ConflictDialogContext {
				return newConflictDialogTestCtx()
			},
			reason: "no conflict dialog active",
		},
		{
			name: "confirm_writes connection",
			setup: func() *guicontext.ConflictDialogContext {
				c := newConflictDialogTestCtx()
				_ = c.Open(conflictBatch(1), &models.Connection{Name: "prod", ConfirmWrites: true})
				return c
			},
			reason: "confirm_writes connection: overwrite disabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.setup()
			ctrl := controllers.NewConflictDialogController(nil, controllers.CoreDeps{}, ctx, &fakeFocusTree{})
			reg := commands.NewRegistry()
			ctrl.RegisterActions(reg)

			cmd, ok := reg.Get(controllers.ConflictDialogOverwrite)
			if !ok || cmd == nil {
				t.Fatal("ConflictDialogOverwrite not registered")
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

// === Render-layer tests (DefaultConflictDialogRender) ====================

// AC: Default render includes the conflict-detected header.
func TestDefaultConflictDialogRender_Header(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: conflictBatch(1),
		Conn:      &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if !strings.Contains(body, "Conflicts detected") {
		t.Errorf("body missing header:\n%s", body)
	}
	if !strings.Contains(body, "no changes applied") {
		t.Errorf("body missing reassurance:\n%s", body)
	}
}

// AC: Per-row render emits `your edit:`, `server now:`, `loaded at:`.
func TestDefaultConflictDialogRender_PerRowLines(t *testing.T) {
	loaded := time.Date(2026, 5, 23, 12, 34, 56, 0, time.UTC)
	view := guicontext.ConflictDialogView{
		Conflicts: []models.ConflictedEdit{
			{
				Edit: models.PendingEdit{
					PrimaryKey: []any{int64(42)},
					Column:     "email",
					OldValue:   "old@example.com",
					NewValue:   "new@example.com",
					Kind:       models.Literal,
				},
				ServerValue: "drifted@example.com",
				LoadedAt:    loaded,
			},
		},
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	for _, want := range []string{
		"row 42",
		"email",
		"your edit:",
		"new@example.com",
		"server now:",
		"drifted@example.com",
		"loaded at:",
		loaded.Format(time.RFC3339),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

// AC: a json/jsonb conflict renders the server
// value as JSON text — matching the grid and commit preview — not Go's
// byte-slice form for a []byte the server returned.
func TestDefaultConflictDialogRender_JSONValues(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: []models.ConflictedEdit{
			{
				Edit: models.PendingEdit{
					PrimaryKey: []any{int64(7)},
					Column:     "payload",
					ColumnType: "jsonb",
					OldValue:   []byte(`{"a":1}`),
					NewValue:   `{"a":2}`,
					Kind:       models.Literal,
				},
				ServerValue: []byte(`{"a":3}`),
				LoadedAt:    time.Date(2026, 5, 23, 12, 34, 56, 0, time.UTC),
			},
		},
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if !strings.Contains(body, `{"a":3}`) {
		t.Errorf("server value should render as json text, got:\n%s", body)
	}
	if strings.Contains(body, "[123") {
		t.Errorf("server value should not render as a byte slice, got:\n%s", body)
	}
}

// AC: When ServerValue == staged NewValue, the row renders
// "already applied by another session".
func TestDefaultConflictDialogRender_AlreadyAppliedAnnotation(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: []models.ConflictedEdit{
			{
				Edit: models.PendingEdit{
					PrimaryKey: []any{int64(1)},
					Column:     "name",
					OldValue:   "alice",
					NewValue:   "bob",
					Kind:       models.Literal,
				},
				ServerValue: "bob",
				LoadedAt:    time.Now(),
			},
		},
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if !strings.Contains(body, "already applied by another session") {
		t.Errorf("body missing already-applied annotation:\n%s", body)
	}
}

// AC: Legend includes `[r]`, `[o]`, `[Esc]` on default connection.
func TestDefaultConflictDialogRender_LegendDefault(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: conflictBatch(1),
		Conn:      &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	for _, want := range []string{"[r] refresh", "[o] overwrite", "[Esc] cancel"} {
		if !strings.Contains(body, want) {
			t.Errorf("legend missing %q:\n%s", want, body)
		}
	}
}

// AC: Legend OMITS `[o] overwrite` on confirm_writes:true connection.
func TestDefaultConflictDialogRender_LegendHidesOverwriteOnConfirmWrites(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: conflictBatch(1),
		Conn:      &models.Connection{Name: "prod", ConfirmWrites: true},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if strings.Contains(body, "[o]") {
		t.Errorf("legend still shows [o] on confirm_writes:\n%s", body)
	}
	if !strings.Contains(body, "[r] refresh") {
		t.Errorf("legend missing [r] refresh:\n%s", body)
	}
	if !strings.Contains(body, "[Esc] cancel") {
		t.Errorf("legend missing [Esc] cancel:\n%s", body)
	}
}

// AC: Composite-PK conflicts render as `(v1, v2)`.
func TestDefaultConflictDialogRender_CompositePK(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: []models.ConflictedEdit{
			{
				Edit: models.PendingEdit{
					PrimaryKey: []any{int64(7), "child"},
					Column:     "role",
					OldValue:   "member",
					NewValue:   "admin",
					Kind:       models.Literal,
				},
				ServerValue: "viewer",
				LoadedAt:    time.Now(),
			},
		},
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if !strings.Contains(body, "(7, child)") {
		t.Errorf("body missing composite PK tuple:\n%s", body)
	}
}

// AC: NULL server value renders as `NULL` (distinct from empty string).
func TestDefaultConflictDialogRender_NullServerValue(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: []models.ConflictedEdit{
			{
				Edit: models.PendingEdit{
					PrimaryKey: []any{int64(1)},
					Column:     "email",
					NewValue:   "new@example.com",
					Kind:       models.Literal,
				},
				ServerValue: nil,
				LoadedAt:    time.Now(),
			},
		},
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if !strings.Contains(body, "server now: NULL") {
		t.Errorf("body missing NULL marker:\n%s", body)
	}
}

// AC: Expression edits render NewExpr (not NewValue) in `your edit:`.
func TestDefaultConflictDialogRender_ExpressionRendersExpr(t *testing.T) {
	view := guicontext.ConflictDialogView{
		Conflicts: []models.ConflictedEdit{
			{
				Edit: models.PendingEdit{
					PrimaryKey: []any{int64(1)},
					Column:     "last_seen_at",
					NewExpr:    "now()",
					Kind:       models.Expression,
				},
				ServerValue: "2026-05-23T00:00:00Z",
				LoadedAt:    time.Now(),
			},
		},
		Conn: &models.Connection{Name: "dev"},
	}
	body := controllers.DefaultConflictDialogRender(view)
	if !strings.Contains(body, "your edit:  now()") {
		t.Errorf("body missing expression in your-edit line:\n%s", body)
	}
}
