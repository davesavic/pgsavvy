package context

import (
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// newTestCommitDialog builds a CommitDialogContext wired to the
// supplied driver. Mirrors newTestCellEditor for symmetry.
func newTestCommitDialog(drv types.GuiDriver) *CommitDialogContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      CommitDialogKey(),
		ViewName: string(CommitDialogKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewCommitDialogContext(base, deps)
}

// stagedSet returns a PendingEditSet pre-loaded with n one-column-per-
// row Literal edits on (schema, table). PK values are int64 i+1.
func stagedSet(schema, table string, n int) *models.PendingEditSet {
	s := &models.PendingEditSet{Table: models.Ref{Schema: schema, Table: table}}
	for i := 0; i < n; i++ {
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

func TestCommitDialogContext_InactiveByDefault(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCommitDialog(drv)
	if c.Active() {
		t.Fatal("Active() = true on fresh context; want false")
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times while inactive; want 0", drv.writes)
	}
}

func TestCommitDialogContext_OpenCapturesSnapshot(t *testing.T) {
	c := newTestCommitDialog(nil)
	set := stagedSet("public", "users", 3)
	conn := &models.Connection{Name: "prod", ConfirmWrites: true, Color: "red"}
	c.Open(set, conn)

	if !c.Active() {
		t.Fatal("Active() = false after Open; want true")
	}
	if c.Set() != set {
		t.Errorf("Set() = %p, want %p", c.Set(), set)
	}
	if c.Connection() != conn {
		t.Errorf("Connection() = %p, want %p", c.Connection(), conn)
	}
	if c.Mode() != CommitDialogPreview {
		t.Errorf("Mode() = %v, want CommitDialogPreview", c.Mode())
	}
	if c.TypedName() != "" {
		t.Errorf("TypedName() = %q, want empty on Open", c.TypedName())
	}
}

func TestCommitDialogContext_CloseResetsState(t *testing.T) {
	c := newTestCommitDialog(nil)
	c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "x"})
	c.SetMode(CommitDialogSqlPreview)
	c.SetTypedName("partial")
	c.SetDryRunResult([]DryRunStmtResult{{SQL: "UPDATE x", RowsAffected: 1}})

	c.Close()
	if c.Active() {
		t.Error("Active() = true after Close; want false")
	}
	if c.Set() != nil {
		t.Errorf("Set() = %p after Close; want nil", c.Set())
	}
	if c.Connection() != nil {
		t.Errorf("Connection() = %p after Close; want nil", c.Connection())
	}
	if c.Mode() != CommitDialogPreview {
		t.Errorf("Mode() = %v after Close; want Preview", c.Mode())
	}
	if c.TypedName() != "" {
		t.Errorf("TypedName() = %q after Close; want empty", c.TypedName())
	}
	if c.DryRunResult() != nil {
		t.Errorf("DryRunResult() = %v after Close; want nil", c.DryRunResult())
	}
}

// AC: TypedName persists across mode transitions in a single dialog.
func TestCommitDialogContext_TypedNamePersistsAcrossModeToggles(t *testing.T) {
	c := newTestCommitDialog(nil)
	c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod", ConfirmWrites: true})
	c.SetTypedName("pro")

	c.SetMode(CommitDialogSqlPreview)
	if c.TypedName() != "pro" {
		t.Errorf("TypedName lost on SqlPreview switch: %q", c.TypedName())
	}
	c.SetMode(CommitDialogDryRunResult)
	if c.TypedName() != "pro" {
		t.Errorf("TypedName lost on DryRunResult switch: %q", c.TypedName())
	}
	c.SetMode(CommitDialogPreview)
	if c.TypedName() != "pro" {
		t.Errorf("TypedName lost on Preview switch back: %q", c.TypedName())
	}
}

// AC: re-opening clears TypedName (no memory).
func TestCommitDialogContext_ReopenClearsTypedName(t *testing.T) {
	c := newTestCommitDialog(nil)
	conn := &models.Connection{Name: "prod", ConfirmWrites: true}
	c.Open(stagedSet("s", "t", 1), conn)
	c.SetTypedName("prod")
	c.Close()

	c.Open(stagedSet("s", "t", 1), conn)
	if c.TypedName() != "" {
		t.Errorf("TypedName = %q after re-Open; want empty (no memory)", c.TypedName())
	}
}

func TestCommitDialogContext_ApplyEnabled(t *testing.T) {
	cases := []struct {
		name    string
		conn    *models.Connection
		typed   string
		setN    int
		want    bool
		comment string
	}{
		{
			name: "default-conn-1-edit",
			conn: &models.Connection{Name: "dev"},
			setN: 1,
			want: true,
		},
		{
			name:    "read-only-blocks",
			conn:    &models.Connection{Name: "dev", ReadOnly: true},
			setN:    1,
			want:    false,
			comment: "ReadOnly hard-disables apply",
		},
		{
			name:    "confirm-writes-empty-typed",
			conn:    &models.Connection{Name: "prod", ConfirmWrites: true},
			setN:    1,
			want:    false,
			comment: "TypedName mismatch blocks",
		},
		{
			name:  "confirm-writes-wrong-typed",
			conn:  &models.Connection{Name: "prod", ConfirmWrites: true},
			typed: "wrong",
			setN:  1,
			want:  false,
		},
		{
			name:  "confirm-writes-match",
			conn:  &models.Connection{Name: "prod", ConfirmWrites: true},
			typed: "prod",
			setN:  1,
			want:  true,
		},
		{
			name:    "empty-set-blocks",
			conn:    &models.Connection{Name: "dev"},
			setN:    0,
			want:    false,
			comment: "empty PendingEditSet must hard-disable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCommitDialog(nil)
			c.Open(stagedSet("s", "t", tc.setN), tc.conn)
			c.SetTypedName(tc.typed)
			if got := c.ApplyEnabled(); got != tc.want {
				t.Errorf("ApplyEnabled() = %v, want %v (%s)", got, tc.want, tc.comment)
			}
		})
	}
}

func TestCommitDialogContext_ApplyEnabledInactive(t *testing.T) {
	c := newTestCommitDialog(nil)
	if c.ApplyEnabled() {
		t.Error("ApplyEnabled = true while inactive; want false")
	}
}

func TestCommitDialogContext_HandleRenderUsesHook(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCommitDialog(drv)
	c.SetRenderHook(func(v CommitDialogView) string {
		return "BODY:" + v.Conn.Name
	})
	c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "prod"})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if !strings.Contains(drv.lastContent, "BODY:prod") {
		t.Errorf("rendered body = %q, want contains BODY:prod", drv.lastContent)
	}
}

func TestCommitDialogContext_HandleRenderInactiveNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCommitDialog(drv)
	c.SetRenderHook(func(v CommitDialogView) string { return "X" })
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("inactive render produced %d writes; want 0", drv.writes)
	}
}

func TestCommitDialogContext_HandleRenderNoHookNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestCommitDialog(drv)
	c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "x"})
	// No SetRenderHook call.
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("render without hook produced %d writes; want 0", drv.writes)
	}
}

func TestCommitDialogContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestCommitDialog(nil)
	c.SetRenderHook(func(v CommitDialogView) string { return "X" })
	c.Open(stagedSet("s", "t", 1), &models.Connection{Name: "x"})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestCommitDialogContext_PresentationNilHook(t *testing.T) {
	c := newTestCommitDialog(nil)
	style, header := c.Presentation()
	if header != "" {
		t.Errorf("header = %q, want empty when no PresentationHook", header)
	}
	_ = style
}

func TestCommitDialogContext_PresentationWithHook(t *testing.T) {
	conn := &models.Connection{Name: "prod", Color: "red"}
	calls := 0
	deps := types.ContextTreeDeps{
		PresentationHook: func(c *models.Connection) (types.TextStyle, string) {
			calls++
			if c != conn {
				t.Errorf("hook conn = %p, want %p", c, conn)
			}
			return types.TextStyle{}, "header-text"
		},
	}
	base := NewBaseContext(BaseContextOpts{
		Key:      CommitDialogKey(),
		ViewName: string(CommitDialogKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	c := NewCommitDialogContext(base, deps)
	c.Open(stagedSet("s", "t", 1), conn)
	_, h := c.Presentation()
	if h != "header-text" {
		t.Errorf("header = %q, want header-text", h)
	}
	if calls != 1 {
		t.Errorf("PresentationHook calls = %d, want 1", calls)
	}
}

func TestCommitDialogKey_Stable(t *testing.T) {
	// Z1 will replace commitDialogKey with types.COMMIT_DIALOG. Until
	// then the value MUST remain "commit_dialog" so view-name
	// registration can find the popup.
	if got := string(CommitDialogKey()); got != "commit_dialog" {
		t.Errorf("CommitDialogKey = %q, want commit_dialog", got)
	}
}

func TestCommitDialogContext_SatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &CommitDialogContext{}
}
