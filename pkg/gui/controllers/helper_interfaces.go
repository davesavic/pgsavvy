package controllers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
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

// ResultTabsHelper is the narrow surface the QueryEditorController calls
// to hand each launched RunHandle (or Explain plan) to the multi-tab
// result pane. The concrete implementation lands in dbsavvy-66p.12;
// 66p.11 declares the interface so the controller compiles and is
// testable today.
//
// OpenResultTab opens a new result tab for rh and starts streaming it.
// label is the tab title (typically the first ~40 chars of the SQL).
//
// OpenPlanTab opens a tab rendering the raw plan text (and, later, the
// parsed tree in epic E7). plan.RawText is the v1 source of truth.
//
// ShowError surfaces a non-streamable failure (e.g. driver error before
// Stream returns) in the result tabs pane rather than as a toast.
type ResultTabsHelper interface {
	OpenResultTab(label string, rh *session.RunHandle) error
	OpenPlanTab(label string, plan models.Plan) error
	ShowError(label string, err error)
}

// EditorBufferReader is the narrow surface the QueryEditorController
// queries to learn what statement to run. It returns the full buffer
// text and the cursor's byte offset into that buffer. The concrete
// implementation reads from the QUERY_EDITOR view's TextArea once the
// real (non-stub) context lands; tests inject a fake.
//
// BufferText returns the full editor buffer. The empty string is a
// valid return (empty buffer).
//
// CursorOffset returns the byte offset of the cursor into BufferText.
// Out-of-range values are clamped by callers; the implementation may
// return any int.
type EditorBufferReader interface {
	BufferText() string
	CursorOffset() int
}

// NoticeReporter routes server NOTICE / WARNING messages from
// streaming queries to the command_log panel and a first-of-run toast.
// QueryEditorController calls OnRunStart before launching a run,
// AttachStream for each RunHandle the run produces, and Finish once
// no more streams will attach; OnRunEnd then fires automatically when
// the last attached stream's notice channel drains. dbsavvy-66p.13.
type NoticeReporter interface {
	OnRunStart(runID string)
	OnRunEnd(runID string)
	OnNotice(n pgconn.Notice)
	AttachStream(rh *session.RunHandle)
	Finish(runID string)
}

// Compile-time sanity check: data.ErrNeedsConfirmation must remain an
// exported sentinel; this assertion fails to compile if the helper
// package renames or unexports it.
var _ = data.ErrNeedsConfirmation
