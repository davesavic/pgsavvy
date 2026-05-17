package controllers

import (
	"context"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// The interfaces below capture only the methods T7a controllers actually
// call. Concrete implementations land in sibling task dbsavvy-zro (T7b)
// under pkg/gui/controllers/helpers/ui/* and
// pkg/gui/controllers/helpers/data/refresh_helper.go. Each interface
// is intentionally narrow so that:
//
//   - Controllers in T7a build and test in isolation (no zro dependency).
//   - T7b's concrete types satisfy the interfaces structurally without
//     needing to import this package.
//   - Tests inject lightweight fakes.

// ConfirmHelper pushes a confirmation popup. onYes runs after the user
// approves; onNo (may be nil) runs after Esc / "No".
type ConfirmHelper interface {
	Confirm(title, body string, onYes func() error, onNo func() error) error
}

// PromptHelper pushes a single-line prompt. Used by connections_controller
// for the chained add-connection flow (a thin facade — the real walk is
// owned by data.ConnectionFormHelper.WalkAddConnection, which T7b's
// prompt helper drives).
type PromptHelper interface {
	Prompt(label string, initial string, onSubmit func(value string) error, onCancel func() error) error
}

// ToastHelper writes a transient message to the status bar slot.
type ToastHelper interface {
	Show(message string, ttl time.Duration)
}

// OneshotArmer arms a one-shot prefix dispatcher: after Arm is invoked,
// the next key matching a suffix in suffixes triggers the matched
// handler; any other key cancels silently.
type OneshotArmer interface {
	Arm(prefix string, suffixes map[rune]func() error, scope string) error
}

// RefreshHelper reloads side-rail data after a hide/unhide mutation.
type RefreshHelper interface {
	RefreshSchemas(ctx context.Context) error
	RefreshTables(ctx context.Context, schema string) error
	RefreshColumns(ctx context.Context, schema, table string) error
	RefreshIndexes(ctx context.Context, schema, table string) error
}

// TipHelper handles the first-run tip popup lifecycle.
type TipHelper interface {
	DismissStartupTip() error
}

// TablesDoubleClickHelper surfaces the deferred-action toast when the
// user activates a table row (Enter or double-click).
type TablesDoubleClickHelper interface {
	DoubleClickStub(t *models.Table) error
}

// MenuPushHelper opens / closes the MENU popup.
type MenuPushHelper interface {
	PushMenu() error
	PopMenu() error
}

// ConnectionPicker exposes the cursor-selected connection from the
// CONNECTIONS context.
type ConnectionPicker interface {
	SelectedConnection() *models.Connection
}

// SchemaPicker exposes the cursor-selected schema name and the show-
// hidden toggle controlled by the SCHEMAS context.
type SchemaPicker interface {
	SelectedSchemaName() string
	ToggleShowHidden()
}

// TablePicker exposes the cursor-selected table from the TABLES context.
type TablePicker interface {
	SelectedTable() *models.Table
}

// ActiveConnection returns the ID of the currently open connection
// profile. SchemasHelper.HideSchema needs this to scope the
// AppState.HiddenSchemas key.
type ActiveConnection interface {
	ActiveConnectionID() string
}

// Compile-time sanity check: data.ErrNeedsConfirmation must remain an
// exported sentinel; this assertion fails to compile if the helper
// package renames or unexports it.
var _ = data.ErrNeedsConfirmation
