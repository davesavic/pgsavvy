package controllers

import (
	"context"
	"fmt"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// HelperBag is the dependency bundle every controller receives. It
// carries the concrete data helpers (real types from T4/T5/T6) and the
// interface-typed UI helpers (concrete types land in T7b). All fields
// are optional at construction time so unit tests can leave the ones
// they do not exercise as nil; the controller code nil-checks on use.
type HelperBag struct {
	Driver types.GuiDriver
	Logger DebugLogger

	// Pickers expose context cursor state.
	Connections      ConnectionPicker
	Schemas          SchemaPicker
	Tables           TablePicker
	ActiveConnection ActiveConnection

	// Data helpers (concrete adapters; tests inject fakes).
	Connect        ConnectInvoker
	SchemasHelper  SchemasInvoker
	ConnectionForm ConnectionFormInvoker

	// UI helpers (interfaces; T7b's concrete types satisfy these).
	Confirm     ConfirmHelper
	Prompt      PromptHelper
	Toast       ToastHelper
	Refresh     RefreshHelper
	Tip         TipHelper
	TableDouble TablesDoubleClickHelper
	Menu        MenuPushHelper

	// HiddenPatterns supplies the (builtin, profile) glob lists for
	// SchemasInvoker.UnhideSchema. Resolved per-call so a hot-reloaded
	// profile change takes effect on the next U keystroke.
	HiddenPatterns func() (builtin []string, profile []string)

	// KbRuntime is the aggregate that bundles every keybinding-system
	// collaborator (commands.Registry, Matcher, ModeStore, WhichKey,
	// ExRegistry). Controllers use it to register action handlers via
	// RegisterActions and to reach the Matcher when needed. Nil during
	// unit tests that do not exercise dispatch.
	KbRuntime *keys.Runtime
}

// DebugLogger is the minimal logging surface controllers expect.
// *logrus.Logger satisfies it.
type DebugLogger interface {
	Debugf(format string, args ...any)
}

// ConnectInvoker is the narrow surface the connections controller calls
// to open a connection. The shape is INTENTIONALLY error-only: the real
// data.ConnectHelper.Connect returns (drivers.Connection, drivers.Session,
// error), but the controller never touches the Connection / Session
// directly. The T10 bootstrap (dbsavvy-enn.11) supplies a thin facade
// closure that calls the real Connect, stashes the returned Connection
// and Session in shared helper state (so refresh/disconnect can reach
// them), and surfaces only the error here.
//
// Tests inject a recording fake; production injects the closure.
type ConnectInvoker interface {
	Connect(ctx context.Context, profile *models.Connection) error
}

// SchemasInvoker is the SchemasHelper surface used by schemas_controller.
type SchemasInvoker interface {
	HideSchema(connID, schemaName string) error
	UnhideSchema(connID, schemaName string, builtin, profile []string) error
}

// ConnectionFormInvoker is the narrow surface connections_controller
// invokes from `a`. The real data.ConnectionFormHelper.WalkAddConnection
// takes a ChainedPrompter and an onComplete callback; the T10 bootstrap
// (dbsavvy-enn.11) closes over those collaborators (T7b's prompt helper
// drives the prompter, and onComplete reloads + reselects the new row)
// and exposes the simpler WalkAdd(ctx) shape here.
type ConnectionFormInvoker interface {
	WalkAdd(ctx context.Context) error
}

// baseController is the shared root of every controller.
type baseController struct {
	c       *common.Common
	helpers HelperBag
}

// newBase constructs a baseController. c may be nil during unit tests.
func newBase(c *common.Common, helpers HelperBag) baseController {
	return baseController{c: c, helpers: helpers}
}

// wrapErr decorates a handler error with the controller's label.
func (b *baseController) wrapErr(label string, err error) error {
	if err == nil {
		return nil
	}
	if b.helpers.Logger != nil {
		b.helpers.Logger.Debugf("controller %q: %v", label, err)
	}
	return fmt.Errorf("controller %s: %w", label, err)
}

// tr returns the active i18n TranslationSet, falling back to a fresh
// English set when c is nil or has no Tr wired (test-friendly).
func (b *baseController) tr() *i18n.TranslationSet {
	if b.c != nil && b.c.Tr != nil {
		return b.c.Tr
	}
	return i18n.EnglishTranslationSet()
}
