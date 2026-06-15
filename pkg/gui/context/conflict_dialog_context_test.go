package context

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// newTestConflictDialog builds a ConflictDialogContext wired to the
// supplied driver. Mirrors newTestCommitDialog for symmetry.
func newTestConflictDialog(drv types.GuiDriver) *ConflictDialogContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      ConflictDialogKey(),
		ViewName: string(ConflictDialogKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	return NewConflictDialogContext(base, deps)
}

// conflictBatch builds n distinct ConflictedEdits with PK i+1 and a
// distinct server-now value so equality checks (already-applied) do
// NOT fire by default.
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

func TestConflictDialogContext_InactiveByDefault(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConflictDialog(drv)
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

// AC: Open with empty Conflicts refuses (precondition test).
func TestConflictDialogContext_OpenEmptyRefuses(t *testing.T) {
	c := newTestConflictDialog(nil)
	conn := &models.Connection{Name: "dev"}

	if err := c.Open(nil, conn); !errors.Is(err, ErrNoConflicts) {
		t.Errorf("Open(nil): err = %v, want ErrNoConflicts", err)
	}
	if c.Active() {
		t.Error("Active() = true after refused Open; want false")
	}

	if err := c.Open([]models.ConflictedEdit{}, conn); !errors.Is(err, ErrNoConflicts) {
		t.Errorf("Open(empty): err = %v, want ErrNoConflicts", err)
	}
	if c.Active() {
		t.Error("Active() = true after refused Open(empty); want false")
	}
}

func TestConflictDialogContext_OpenCapturesSnapshot(t *testing.T) {
	c := newTestConflictDialog(nil)
	batch := conflictBatch(3)
	conn := &models.Connection{Name: "prod", Color: "red"}

	if err := c.Open(batch, conn); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !c.Active() {
		t.Fatal("Active() = false after Open; want true")
	}
	if len(c.Conflicts()) != 3 {
		t.Errorf("Conflicts() len = %d, want 3", len(c.Conflicts()))
	}
	if c.Connection() != conn {
		t.Errorf("Connection() = %p, want %p", c.Connection(), conn)
	}
}

func TestConflictDialogContext_CloseResetsState(t *testing.T) {
	c := newTestConflictDialog(nil)
	_ = c.Open(conflictBatch(2), &models.Connection{Name: "x"})

	c.Close()
	if c.Active() {
		t.Error("Active() = true after Close; want false")
	}
	if c.Conflicts() != nil {
		t.Errorf("Conflicts() = %v after Close; want nil", c.Conflicts())
	}
	if c.Connection() != nil {
		t.Errorf("Connection() = %p after Close; want nil", c.Connection())
	}
}

// AC: OverwriteAllowed is false on confirm_writes:true connection.
func TestConflictDialogContext_OverwriteAllowed(t *testing.T) {
	cases := []struct {
		name string
		conn *models.Connection
		want bool
	}{
		{"default-conn", &models.Connection{Name: "dev"}, true},
		{"confirm-writes-blocks", &models.Connection{Name: "prod", ConfirmWrites: true}, false},
		{"read-only-allows-overwrite", &models.Connection{Name: "dev", ReadOnly: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestConflictDialog(nil)
			_ = c.Open(conflictBatch(1), tc.conn)
			if got := c.OverwriteAllowed(); got != tc.want {
				t.Errorf("OverwriteAllowed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConflictDialogContext_OverwriteAllowedInactive(t *testing.T) {
	c := newTestConflictDialog(nil)
	if c.OverwriteAllowed() {
		t.Error("OverwriteAllowed = true while inactive; want false")
	}
}

func TestConflictDialogContext_HandleRenderUsesHook(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConflictDialog(drv)
	c.SetRenderHook(func(v ConflictDialogView) string {
		return "BODY:" + v.Conn.Name
	})
	_ = c.Open(conflictBatch(1), &models.Connection{Name: "prod"})
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

func TestConflictDialogContext_HandleRenderInactiveNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConflictDialog(drv)
	c.SetRenderHook(func(v ConflictDialogView) string { return "X" })
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("inactive render produced %d writes; want 0", drv.writes)
	}
}

func TestConflictDialogContext_HandleRenderNoHookNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConflictDialog(drv)
	_ = c.Open(conflictBatch(1), &models.Connection{Name: "x"})
	// No SetRenderHook call.
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("render without hook produced %d writes; want 0", drv.writes)
	}
}

func TestConflictDialogContext_NilGuiDriverNoPanic(t *testing.T) {
	c := newTestConflictDialog(nil)
	c.SetRenderHook(func(v ConflictDialogView) string { return "X" })
	_ = c.Open(conflictBatch(1), &models.Connection{Name: "x"})
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestConflictDialogContext_PresentationNilHook(t *testing.T) {
	c := newTestConflictDialog(nil)
	style, header := c.Presentation()
	if header != "" {
		t.Errorf("header = %q, want empty when no PresentationHook", header)
	}
	_ = style
}

func TestConflictDialogContext_PresentationWithHook(t *testing.T) {
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
		Key:      ConflictDialogKey(),
		ViewName: string(ConflictDialogKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	c := NewConflictDialogContext(base, deps)
	_ = c.Open(conflictBatch(1), conn)
	_, h := c.Presentation()
	if h != "header-text" {
		t.Errorf("header = %q, want header-text", h)
	}
	if calls != 1 {
		t.Errorf("PresentationHook calls = %d, want 1", calls)
	}
}

func TestConflictDialogKey_Stable(t *testing.T) {
	// Z1 will replace conflictDialogKey with types.CONFLICT_DIALOG.
	// Until then the value MUST remain "conflict_dialog" so view-name
	// registration can find the popup.
	if got := string(ConflictDialogKey()); got != "conflict_dialog" {
		t.Errorf("ConflictDialogKey = %q, want conflict_dialog", got)
	}
}

func TestConflictDialogContext_SatisfiesIBaseContext(t *testing.T) {
	var _ types.IBaseContext = &ConflictDialogContext{}
}
