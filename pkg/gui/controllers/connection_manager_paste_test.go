package controllers_test

import (
	"errors"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

// newPasteController wires a form controller with a clipboard + toast seam.
func newPasteController(ctx *guicontext.ConnectionManagerContext, clip func() (string, error), toasts *[]string) *controllers.ConnectionManagerController {
	ctrl := controllers.NewConnectionManagerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, func() {})
	ctrl.SetDeps(controllers.ConnectionManagerDeps{
		Ctx:           ctx,
		DriversFn:     driverNames,
		ReadClipboard: clip,
		ShowToast:     func(m string) { *toasts = append(*toasts, m) },
	})
	return ctrl
}

// TestController_PasteDSN_PopulatesFieldsAndDropsPassword asserts paste-DSN
// fills the discrete fields, drops the inline password, and toasts (decision 9).
func TestController_PasteDSN_PopulatesFieldsAndDropsPassword(t *testing.T) {
	ctx := newFormCtx()
	var toasts []string
	ctrl := newPasteController(ctx, func() (string, error) {
		return "postgres://u:secret@h:5432/db?sslmode=require", nil
	}, &toasts)

	ctx.OpenAddForm(nil, driverNames)
	ctx.FormSetFocusedValue("pasted") // name @ focus 0 (so validate-all passes)
	dispatchCM(t, ctrl, commands.ConnectionManagerPasteDSN)

	conn, _, _, ok := ctx.FormValidateAll(i18n.EnglishTranslationSet())
	if !ok {
		t.Fatal("validate-all failed after paste")
	}
	if conn.Host != "h" || conn.Port != 5432 || conn.User != "u" ||
		conn.Database != "db" || conn.SSLMode != "require" {
		t.Errorf("paste did not populate discrete fields: %+v", conn)
	}
	if conn.Password != "" {
		t.Errorf("password leaked into the form: %q", conn.Password)
	}
	if len(toasts) != 1 {
		t.Errorf("want one dropped-password toast, got %v", toasts)
	}
}

// TestController_PasteDSN_NoToastWhenNoPassword asserts a password-less DSN
// paste populates fields but shows no dropped-password toast.
func TestController_PasteDSN_NoToastWhenNoPassword(t *testing.T) {
	ctx := newFormCtx()
	var toasts []string
	ctrl := newPasteController(ctx, func() (string, error) {
		return "postgres://u@h:6000/db", nil
	}, &toasts)

	ctx.OpenAddForm(nil, driverNames)
	ctx.FormSetFocusedValue("p")
	dispatchCM(t, ctrl, commands.ConnectionManagerPasteDSN)

	conn, _, _, ok := ctx.FormValidateAll(i18n.EnglishTranslationSet())
	if !ok || conn.Host != "h" || conn.Port != 6000 {
		t.Fatalf("paste without password failed: %+v ok=%v", conn, ok)
	}
	if len(toasts) != 0 {
		t.Errorf("unexpected toast for password-less paste: %v", toasts)
	}
}

// TestController_PasteDSN_InvalidLeavesFieldsUntouched asserts an unparseable
// clipboard string mutates nothing (the Add defaults survive).
func TestController_PasteDSN_InvalidLeavesFieldsUntouched(t *testing.T) {
	ctx := newFormCtx()
	var toasts []string
	ctrl := newPasteController(ctx, func() (string, error) {
		return "postgres://h:notaport/db", nil
	}, &toasts)

	ctx.OpenAddForm(nil, driverNames) // seeds Host=localhost, Port=5432
	ctx.FormSetFocusedValue("p")
	dispatchCM(t, ctrl, commands.ConnectionManagerPasteDSN)

	conn, _, _, _ := ctx.FormValidateAll(i18n.EnglishTranslationSet())
	if conn.Host != "localhost" || conn.Port != 5432 {
		t.Errorf("invalid paste mutated fields: %+v", conn)
	}
	if len(toasts) != 0 {
		t.Errorf("invalid paste should not toast: %v", toasts)
	}
}

// TestController_PasteDSN_EmptyClipboardIsNoOp asserts an empty/error clipboard
// read does not mutate fields or toast.
func TestController_PasteDSN_EmptyClipboardIsNoOp(t *testing.T) {
	ctx := newFormCtx()
	var toasts []string
	ctrl := newPasteController(ctx, func() (string, error) {
		return "", errors.New("clipboard unavailable")
	}, &toasts)

	ctx.OpenAddForm(nil, driverNames)
	ctx.FormSetFocusedValue("p")
	dispatchCM(t, ctrl, commands.ConnectionManagerPasteDSN)

	conn, _, _, _ := ctx.FormValidateAll(i18n.EnglishTranslationSet())
	if conn.Host != "localhost" {
		t.Errorf("empty clipboard mutated fields: %+v", conn)
	}
	if len(toasts) != 0 {
		t.Errorf("empty clipboard should not toast: %v", toasts)
	}
}
