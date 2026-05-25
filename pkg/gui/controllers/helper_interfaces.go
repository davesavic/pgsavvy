package controllers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
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
//
// Yes and No are the dismissal handlers invoked by ConfirmationController's
// y/<cr> and n/<esc> bindings respectively. They consume the recorded
// onYes/onNo callback, pop the popup off the focus stack, and clear the
// helper's pending state. Concrete impl: *ui.ConfirmHelper (see
// helpers/ui/confirm_helper.go).
type ConfirmHelper interface {
	Confirm(title, body string, onYes func() error, onNo func() error) error
	Yes() error
	No() error
}

// PromptHelper pushes a single-line prompt. Used by connections_controller
// for the chained add-connection flow (a thin facade — the real walk is
// owned by data.ConnectionFormHelper.WalkAddConnection, which T7b's
// prompt helper drives).
//
// Submit / Cancel are the seams the PromptController calls from its
// <cr> / <esc> handlers. SetResetHandler lets the controller subscribe
// to fresh Prompt invocations so it can re-seed its line buffer with
// the new `initial` value (dbsavvy-m47.1).
type PromptHelper interface {
	Prompt(label string, initial string, onSubmit func(value string) error, onCancel func() error) error
	Submit(value string) error
	Cancel() error
	SetResetHandler(fn func(initial string))
}

// ChoiceHelper pushes a list-style selection popup (driver picker and
// similar pickers in the connection-add flow). The helper owns the
// label / choices / cursor; SelectionController reads them live via the
// concrete *ui.ChoiceHelper accessors.
//
// Submit(idx) invokes the stored onSubmit(idx) callback if idx is in
// [0, len(choices)) and pops the popup; out-of-range returns an error
// WITHOUT invoking the callback or popping. Cancel invokes onCancel and
// pops. (Both Submit and Cancel clear helper state so the next Choose
// call starts fresh.)
type ChoiceHelper interface {
	Choose(label string, choices []string, onSubmit func(idx int) error, onCancel func() error) error
	Submit(idx int) error
	Cancel() error
}

// ToastHelper writes a transient message to the status bar slot.
type ToastHelper interface {
	Show(message string, ttl time.Duration)
}

// RefreshHelper reloads side-rail data after a hide/unhide mutation or
// the per-rail `r` keypress (dbsavvy-56u.1). Each method loads fresh
// data via the underlying driver AND pushes the result back into the
// rail context's SetItems — the controllers only need to know which
// rail to refresh.
type RefreshHelper interface {
	RefreshSchemas(ctx context.Context) error
	RefreshTables(ctx context.Context, schema string) error
	RefreshColumns(ctx context.Context, schema, table string) error
	RefreshIndexes(ctx context.Context, schema, table string) error
	RefreshConnections() error
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

// ResultTabIdentityAttacher is the optional surface QueryEditorController
// uses to record the (connID, ResultIdentity) pair on the currently-active
// tab right after OpenResultTab — gating the <leader>gH overlay's
// persistence and seeding the grid's hidden-col set from AppState. The
// concrete *ui.ResultTabsHelper satisfies it; tests that don't implement
// it cause the controller to skip identity attach (overlay then runs
// session-only against any data those tests synthesise). dbsavvy-uv0.6.
type ResultTabIdentityAttacher interface {
	AttachActiveTabIdentity(connID string, ri query.ResultIdentity)
}

// InFlightPreempter is the optional surface QueryEditorController uses to
// stop any in-flight result-tab stream before launching a new run.
// dbsavvy-dk6: a streamed result larger than the initial-fill window
// parks its worker holding the per-session queue lock (SQLSession.streamMu)
// without ever reaching EOF, so a subsequent synchronous Stream on the UI
// goroutine would deadlock the TUI. The concrete *ui.ResultTabsHelper
// satisfies this; test fakes that don't implement it simply skip the
// preempt.
type InFlightPreempter interface {
	PreemptInFlight()
}

// EditorBufferReader is the narrow surface the QueryEditorController
// queries to learn what statement to run. It returns the full buffer
// text, the cursor's byte offset into that buffer, and (post wwd.7)
// the currently selected text when Visual mode is active. The concrete
// implementation reads from the QUERY_EDITOR view's *editor.Buffer;
// tests inject a fake.
//
// BufferText returns the full editor buffer. The empty string is a
// valid return (empty buffer).
//
// CursorOffset returns the byte offset of the cursor into BufferText.
// Out-of-range values are clamped by callers; the implementation may
// return any int.
//
// SelectionText returns the text covered by Buffer.Selection and true
// when a Visual-mode selection is live; ("", false) when no selection
// is active. dbsavvy-wwd.7's <leader>r-in-Visual fan-out reads through
// this method.
type EditorBufferReader interface {
	BufferText() string
	CursorOffset() int
	SelectionText() (string, bool)
}

// NoticeReporter routes server NOTICE / WARNING messages from
// streaming queries to the messages panel and a first-of-run toast.
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
