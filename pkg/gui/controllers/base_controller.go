package controllers

import (
	"context"
	"fmt"

	"github.com/davesavic/dbsavvy/pkg/common"
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
	OneShot     OneshotArmer
	Refresh     RefreshHelper
	Tip         TipHelper
	TableDouble TablesDoubleClickHelper
	Menu        MenuPushHelper

	// HiddenPatterns supplies the (builtin, profile) glob lists for
	// SchemasInvoker.UnhideSchema. Resolved per-call so a hot-reloaded
	// profile change takes effect on the next U keystroke.
	HiddenPatterns func() (builtin []string, profile []string)

	// ProvideLeader returns the configured leader literal (e.g.
	// "<space>"). Resolved at registration time per G1-C: T7a does NOT
	// hardcode "<space>". When nil or returns empty string, the
	// controller falls back to common.Cfg().Leader then to "<space>".
	ProvideLeader func() string
}

// DebugLogger is the minimal logging surface controllers expect.
// *logrus.Logger satisfies it.
type DebugLogger interface {
	Debugf(format string, args ...any)
}

// ConnectInvoker is the slice of *data.ConnectHelper the connections
// controller actually calls.
type ConnectInvoker interface {
	Connect(ctx context.Context, profile *models.Connection) error
}

// SchemasInvoker is the SchemasHelper surface used by schemas_controller.
type SchemasInvoker interface {
	HideSchema(connID, schemaName string) error
	UnhideSchema(connID, schemaName string, builtin, profile []string) error
}

// ConnectionFormInvoker is the WalkAddConnection surface used by
// connections_controller.
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

// leader returns the configured leader prefix label, falling back to
// the Common config and then "<space>". Per G1-C, T7a MUST resolve
// the leader at registration time via Common.Cfg().Leader.
func (b *baseController) leader() string {
	if b.helpers.ProvideLeader != nil {
		if got := b.helpers.ProvideLeader(); got != "" {
			return got
		}
	}
	if b.c != nil {
		if cfg := b.c.Cfg(); cfg != nil && cfg.Leader != "" {
			return cfg.Leader
		}
	}
	return "<space>"
}

// tr returns the active i18n TranslationSet, falling back to a fresh
// English set when c is nil or has no Tr wired (test-friendly).
func (b *baseController) tr() *i18n.TranslationSet {
	if b.c != nil && b.c.Tr != nil {
		return b.c.Tr
	}
	return i18n.EnglishTranslationSet()
}
