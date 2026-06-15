package controllers_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// newTestConnController wires a form controller whose TestConnection closure
// records the routed connection and stamps an inline pass/fail line on the form
// (mirroring the orchestrator closure's inline-publish contract, minus the
// async dial). pass=true stamps a status line; pass=false stamps an error.
func newTestConnController(ctx *guicontext.ConnectionManagerContext, got *[]*models.Connection, pass bool) *controllers.ConnectionManagerController {
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, func() {})
	ctrl.SetDeps(controllers.ConnectionManagerDeps{
		Ctx:       ctx,
		DriversFn: driverNames,
		TestConnection: func(c *models.Connection) {
			*got = append(*got, c)
			if pass {
				ctx.FormSetStatus("connection ok")
				return
			}
			ctx.FormSetError("test failed: dial error")
		},
	})
	return ctrl
}

// TestController_TestConnection_RoutesInProgressConn asserts `t` in form mode
// forwards the IN-PROGRESS (edited, unsaved) connection to the injected closure.
func TestController_TestConnection_RoutesInProgressConn(t *testing.T) {
	ctx := newFormCtx()
	var got []*models.Connection
	ctrl := newTestConnController(ctx, &got, true)

	ctx.OpenAddForm(nil, driverNames) // seeds Host=localhost, Port=5432
	ctx.FormSetFocusedValue("probe")  // name field

	dispatchCM(t, ctrl, commands.ConnectionManagerTestConnection)

	if len(got) != 1 {
		t.Fatalf("TestConnection called %d times, want 1", len(got))
	}
	if got[0].Name != "probe" || got[0].Host != "localhost" || got[0].Port != 5432 {
		t.Errorf("routed connection did not reflect in-progress form: %+v", got[0])
	}
}

// TestController_TestConnection_SaveStillAllowedRegardless asserts a (failed)
// test result leaves the form saveable — saving is independent of the test.
func TestController_TestConnection_SaveStillAllowedRegardless(t *testing.T) {
	ctx := newFormCtx()
	var got []*models.Connection
	ctrl := newTestConnController(ctx, &got, false) // a FAILED test

	ctx.OpenAddForm(nil, driverNames)
	ctx.FormSetFocusedValue("probe")
	dispatchCM(t, ctrl, commands.ConnectionManagerTestConnection)

	// Validate-all must still succeed despite the failed test (name set, SSH
	// off): save is decoupled from the test result.
	if _, _, _, ok := ctx.FormValidateAll(i18n.EnglishTranslationSet()); !ok {
		t.Error("save (validate-all) blocked after a failed test; want independent")
	}
}

// TestController_TestConnection_NoOpOutsideFormMode asserts `t` does nothing
// when the modal is not in form mode (e.g. list mode).
func TestController_TestConnection_NoOpOutsideFormMode(t *testing.T) {
	ctx := newFormCtx() // defaults to ModeList
	var got []*models.Connection
	ctrl := newTestConnController(ctx, &got, true)

	ctx.SetItems([]any{&models.Connection{Name: "beta", DSN: "postgres://u@h/db"}})
	dispatchCM(t, ctrl, commands.ConnectionManagerTestConnection)

	if len(got) != 0 {
		t.Errorf("TestConnection invoked in list mode (got %d calls), want no-op", len(got))
	}
}

// TestController_TestConnection_NilDepIsSafe asserts `t` no-ops (no panic) when
// the TestConnection closure is unwired.
func TestController_TestConnection_NilDepIsSafe(t *testing.T) {
	ctx := newFormCtx()
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, func() {})
	ctrl.SetDeps(controllers.ConnectionManagerDeps{Ctx: ctx, DriversFn: driverNames})
	ctx.OpenAddForm(nil, driverNames)
	dispatchCM(t, ctrl, commands.ConnectionManagerTestConnection)
}

// TestController_TestConnection_BoundToT locks the binding: `t` is bound to
// ConnectionManagerTestConnection in CONNECTION_MANAGER scope.
func TestController_TestConnection_BoundToT(t *testing.T) {
	var got []*models.Connection
	ctrl := newTestConnController(newFormCtx(), &got, true)
	bound := false
	for _, b := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if isRune(b, 't') {
			if b.ActionID != commands.ConnectionManagerTestConnection {
				t.Errorf("`t` → %q, want ConnectionManagerTestConnection", b.ActionID)
			}
			bound = true
		}
	}
	if !bound {
		t.Fatal("`t` binding missing from CONNECTION_MANAGER scope")
	}
}
