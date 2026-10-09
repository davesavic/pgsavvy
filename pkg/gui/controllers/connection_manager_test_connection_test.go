package controllers_test

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
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

func TestReview_form_probe_stale_field_prompt(t *testing.T) {
	for _, value := range []string{"edited-origin", ""} {
		t.Run("submit/"+value, func(t *testing.T) {
			ctx := newFormCtx()
			prompt := &capturePrompt{}
			ctrl := newFormController(ctx, prompt, nil)
			ctx.OpenEditForm(models.Connection{Name: "origin", Driver: "postgres"}, nil, driverNames)
			dispatchCM(t, ctrl, commands.ConnectionManagerFieldEdit)
			ctx.SetMode(guicontext.ModeList)
			ctx.OpenEditForm(models.Connection{Name: "replacement", Driver: "postgres"}, nil, driverNames)
			if err := prompt.Submit(value); err != nil {
				t.Fatal("prompt completion failed")
			}
			if ctx.FormConnection().Name != "replacement" {
				t.Error("old field submit modified replacement form")
			}
		})
	}
}

func TestReview_form_probe_save_and_prompt_controls(t *testing.T) {
	ctx := newFormCtx()
	prompt := &capturePrompt{}
	invalidated := 0
	saves := 0
	ctrl := newFormController(ctx, prompt, func(models.Connection, bool, string) error {
		saves++
		if invalidated != 1 {
			t.Error("save callback ran before probe invalidation")
		}
		return nil
	})
	ctx.OpenEditForm(models.Connection{Name: "origin", Driver: "postgres"}, nil, driverNames)
	ctx.SetOnFormProbeInvalidated(func() { invalidated++ })
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldEdit)
	if err := prompt.Cancel(); err != nil {
		t.Fatal(err)
	}
	dispatchCM(t, ctrl, commands.ConnectionManagerFieldEdit)
	if err := prompt.Submit(""); err != nil {
		t.Fatal(err)
	}
	if invalidated != 0 || ctx.FormConnection().Name != "origin" {
		t.Error("invalid input or prompt cancellation invalidated/changed form")
	}
	dispatchCM(t, ctrl, commands.ConnectionManagerConfirm)
	if saves != 1 || invalidated != 2 || ctx.Mode() != guicontext.ModeList {
		t.Error("save initiation and form exit did not retire probe")
	}
	ctx.OpenAddForm(nil, driverNames)
	invalidated = 0
	dispatchCM(t, ctrl, commands.ConnectionManagerConfirm)
	if saves != 1 || invalidated != 1 || ctx.Mode() != guicontext.ModeForm {
		t.Error("invalid save did not cancel probe while preserving form")
	}
}

func TestControllerProbeUsesVisibleSSH(t *testing.T) {
	for _, scenario := range []string{"disable", "enable default agent", "agent", "password", "key file", "missing host", "missing user", "disabled incomplete", "re-enable", "duplicate name"} {
		t.Run(scenario, func(t *testing.T) {
			rec := testfake.NewRecorderGuiDriver()
			_, _ = rec.SetView(string(types.CONNECTION_MANAGER), 0, 0, 80, 30, 0)
			base := guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.CONNECTION_MANAGER, ViewName: string(types.CONNECTION_MANAGER), Kind: types.MAIN_CONTEXT})
			ctx := guicontext.NewConnectionManagerContext(base, types.ContextTreeDeps{GuiDriver: rec})
			original := models.Connection{Name: "stored", Driver: "postgres", SSHTunnel: &models.SSHTunnelConfig{Host: "bastion", User: "ssh-user", IdentityFile: "/key", SSHPasswordCommand: "synthetic-helper-secret"}}
			if scenario == "key file" {
				original.SSHTunnel.IdentityFromAgent = true
				original.SSHTunnel.IdentityFile = ""
			}
			ctx.OpenEditForm(original, []string{"occupied"}, driverNames)
			focus := func(label string) {
				t.Helper()
				ctx.FormMoveFocus(-100)
				for range 32 {
					if ctx.FormFocusedLabel() == label {
						return
					}
					ctx.FormMoveFocus(1)
				}
				t.Fatalf("field %q not reachable", label)
			}
			set := func(label, value string) { focus(label); ctx.FormSetFocusedValue(value) }
			toggle := func(label string) { focus(label); ctx.FormToggleFocused() }
			wantAgent, wantKey, wantError := false, "/key", ""
			switch scenario {
			case "disable":
				toggle("use_ssh_tunnel")
			case "enable default agent":
				ctx.OpenAddForm(nil, driverNames)
				toggle("use_ssh_tunnel")
				set("ssh_host", "bastion")
				set("ssh_user", "ssh-user")
				wantAgent, wantKey = true, ""
			case "agent":
				toggle("ssh_auth")
				toggle("ssh_auth")
				wantAgent, wantKey = true, ""
			case "password":
				toggle("ssh_auth")
				wantKey = ""
			case "key file":
				toggle("ssh_auth")
				set("identity_file", "/new-key")
				wantKey = "/new-key"
			case "missing host", "disabled incomplete":
				set("ssh_host", "")
				wantError = "session: ssh tunnel requires a host"
				if scenario == "disabled incomplete" {
					toggle("use_ssh_tunnel")
					wantError = ""
				}
			case "missing user":
				set("ssh_user", "")
				wantError = "session: ssh tunnel requires a user"
			case "re-enable":
				toggle("use_ssh_tunnel")
				toggle("use_ssh_tunnel")
			case "duplicate name":
				set("name", "occupied")
			}
			var got []*models.Connection
			ctrl := newTestConnController(ctx, &got, true)
			dispatchCM(t, ctrl, commands.ConnectionManagerTestConnection)
			if ctx.Mode() != guicontext.ModeForm {
				t.Fatal("probing dismissed the unsaved form")
			}
			if wantError != "" {
				if len(got) != 0 {
					t.Fatal("incomplete enabled SSH configuration reached the probe")
				}
				if err := ctx.HandleRender(); err != nil {
					t.Fatal(err)
				}
				body := rec.GetViewBuffer(string(types.CONNECTION_MANAGER))
				if !strings.Contains(body, wantError) || strings.Contains(body, "synthetic-helper-secret") {
					t.Fatal("missing inline SSH validation or exposed credential helper")
				}
				return
			}
			if len(got) != 1 {
				t.Fatal("valid unsaved configuration did not reach the probe exactly once")
			}
			tunnel := got[0].SSHTunnel
			if scenario == "disable" || scenario == "disabled incomplete" {
				if tunnel != nil {
					t.Fatal("disabled SSH configuration reached the probe")
				}
			} else {
				if tunnel == nil || tunnel.Host != "bastion" || tunnel.User != "ssh-user" || tunnel.IdentityFromAgent != wantAgent || tunnel.IdentityFile != wantKey {
					t.Fatal("probe configuration differs from the visible form")
				}
				tunnel.Host = "consumer-mutated"
				if ctx.FormConnection().SSHTunnel.Host != "bastion" || original.SSHTunnel.Host != "bastion" {
					t.Fatal("probe consumer modified the draft or original profile")
				}
			}
		})
	}
}
