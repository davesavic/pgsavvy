package controllers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// CoreDeps carries the two deps every controller needs: the GuiDriver
// and the DebugLogger. Both are load-bearing and guaranteed non-nil —
// see NewCoreDeps, which fails fast (panics) on a nil argument. Because
// production wiring constructs CoreDeps via NewCoreDeps, controllers may
// treat Driver/Logger as always present.
type CoreDeps struct {
	Driver types.GuiDriver
	Logger DebugLogger
}

// NewCoreDeps constructs the fail-fast core dependency bundle. It PANICS
// if driver or logger is nil: a wiring site that fails to supply either
// is a programmer error that must surface at construction, not as a
// silently dead keybinding at dispatch (dbsavvy-fow.10 D2, Option C).
func NewCoreDeps(driver types.GuiDriver, logger DebugLogger) CoreDeps {
	if driver == nil {
		panic("controllers.NewCoreDeps: nil driver")
	}
	if logger == nil {
		panic("controllers.NewCoreDeps: nil logger")
	}
	return CoreDeps{Driver: driver, Logger: logger}
}

// NavDeps carries the schema/object-navigation collaborators: the rail
// pickers, the active-connection accessor, the connect/schemas/form data
// helpers, the refresh helper, and the rail-activation closures. Connect
// is a required constructor parameter (load-bearing — connections cannot
// open without it); the remaining fields are optional and nil-safe, set
// directly on the returned struct.
type NavDeps struct {
	// Pickers expose context cursor state.
	Connections      ConnectionPicker
	Schemas          SchemaPicker
	Tables           TablePicker
	ActiveConnection ActiveConnection

	// Data helpers (concrete adapters; tests inject fakes).
	Connect        ConnectInvoker
	SchemasHelper  SchemasInvoker
	ConnectionForm ConnectionFormInvoker

	Refresh RefreshHelper

	// HiddenPatterns supplies the (builtin, profile) glob lists for
	// SchemasInvoker.UnhideSchema. Resolved per-call so a hot-reloaded
	// profile change takes effect on the next U keystroke.
	HiddenPatterns func() (builtin []string, profile []string)

	// Reconnector owns the Ping + Disconnect/Reconnect surface the
	// ReconnectController calls when the session is disconnected. Wired
	// by the orchestrator to a concrete adapter over ConnectHelper +
	// connectInvoker. Nil-safe: the controller no-ops when unwired.
	// hq5.7.
	Reconnector ReconnectInvoker

	// OnPickConnection pushes the CONNECTIONS context onto the focus
	// stack so the user can choose a different profile. Wired by the
	// orchestrator via tree.Push(registry.Connections). hq5.7.
	OnPickConnection func() error

	// OnSchemaActivate fires when <CR> is pressed in the SCHEMAS rail.
	// The orchestrator wires this to a closure that reloads the TABLES
	// rail for the supplied schema name on a worker goroutine
	// (dbsavvy-04n). Nil-safe: SchemasController no-ops when unwired.
	OnSchemaActivate func(schema string)

	// OnTableActivate fires when <CR> is pressed in the TABLES rail. The
	// orchestrator wires this to a closure that runs a bounded
	// "SELECT * FROM <table>" through the QueryEditorController run path
	// and pushes the active result tab onto the focus stack so results
	// take focus (dbsavvy-gj8). Nil-safe: TablesController no-ops when
	// unwired.
	OnTableActivate func(table *models.Table) error
}

// NewNavDeps constructs the navigation bundle. Connect is a required
// parameter so a wiring site that forgets it is a compile error; nil is
// still a legal value, so unit tests pass nil explicitly. The optional
// pickers and closures are set on the returned struct by the caller.
func NewNavDeps(connect ConnectInvoker) NavDeps {
	return NavDeps{Connect: connect}
}

// UIDeps carries the interface-typed UI helpers (confirm/prompt/choice/
// toast popups, tips, the menu push surface, and the table double-click
// handler). Confirm and Toast are required constructor parameters
// (load-bearing — destructive flows gate on Confirm, and connect/edit
// feedback rides Toast); the rest are optional and nil-safe.
type UIDeps struct {
	Confirm     ConfirmHelper
	Prompt      PromptHelper
	Choice      ChoiceHelper
	Toast       ToastHelper
	Tip         TipHelper
	TableDouble TablesDoubleClickHelper
	Menu        MenuPushHelper
}

// NewUIDeps constructs the UI bundle. Confirm and Toast are required
// parameters (compile error if a wiring site omits them); nil stays a
// legal value, so unit tests pass nil explicitly. The remaining fields
// are set on the returned struct.
func NewUIDeps(confirm ConfirmHelper, toast ToastHelper) UIDeps {
	return UIDeps{Confirm: confirm, Toast: toast}
}

// QueryDeps carries the query-editor + result-pane collaborators
// (dbsavvy-66p.11). QueryRunner orchestrates SQLSession.Stream / Explain;
// ResultTabs hands the launched RunHandle to the multi-tab pane;
// EditorBuffer reports the buffer + cursor offset; Notice routes server
// NOTICE/WARNING messages; ActivePlanContextFn resolves the focused plan
// tab; KbRuntime bundles the keybinding-system collaborators. All are
// optional and nil-safe — the controller no-ops when any is unwired.
type QueryDeps struct {
	QueryRunner  *data.QueryRunner
	ResultTabs   ResultTabsHelper
	EditorBuffer EditorBufferReader

	// Notice routes server NOTICE/WARNING messages from streaming
	// queries to the messages panel and a first-of-run toast
	// (dbsavvy-66p.13). Nil-safe: the controller no-ops when unwired.
	Notice NoticeReporter

	// ActivePlanContextFn resolves the currently-active plan tab's
	// *context.PlanContext (or nil when no plan tab is focused). Wired
	// by the orchestrator to a closure over the result_tabs helper;
	// PlanController handlers use it to find their target. Nil-safe:
	// PlanController treats nil as "no active plan tab" and no-ops.
	// dbsavvy-uv0.8.
	ActivePlanContextFn PlanContextResolver

	// KbRuntime is the aggregate that bundles every keybinding-system
	// collaborator (commands.Registry, Matcher, ModeStore, WhichKey,
	// ExRegistry). Controllers use it to register action handlers via
	// RegisterActions and to reach the Matcher when needed. Nil during
	// unit tests that do not exercise dispatch.
	KbRuntime *keys.Runtime
}

// ThreadingDeps carries the UI-thread / worker scheduling closures
// (DESIGN.md §17). Controllers call these to schedule UI-thread work and
// to spawn background workers without importing the orchestrator (which
// would close the import cycle: orchestrator imports controllers). In
// production wiring all three closures delegate to *orchestrator.Gui's
// methods of the same name. All three are required constructor
// parameters so a wiring site that forgets one is a compile error; nil
// stays a legal value, so unit tests that do not exercise async paths
// pass nil explicitly.
type ThreadingDeps struct {
	OnUIThread            func(fn func() error)
	OnUIThreadContentOnly func(fn func() error)
	OnWorker              func(fn func(gocui.Task) error)
}

// NewThreadingDeps constructs the threading bundle. All three closures
// are required parameters (compile error if a wiring site omits one);
// nil stays a legal value, so unit tests pass nil explicitly.
func NewThreadingDeps(
	onUIThread func(fn func() error),
	onUIThreadContentOnly func(fn func() error),
	onWorker func(fn func(gocui.Task) error),
) ThreadingDeps {
	return ThreadingDeps{
		OnUIThread:            onUIThread,
		OnUIThreadContentOnly: onUIThreadContentOnly,
		OnWorker:              onWorker,
	}
}

// EditDeps carries the inline-edit collaborators (epic dbsavvy-bwq).
// PendingDiscard drives the `<leader>cu` / `<leader>cU` discard flows +
// table-switch guard. JumpList records originating-cell entries for
// `<c-o>` / `<c-i>` jump navigation. FKForward owns `gd` forward FK
// navigation. PendingEditSet is the process-wide pending-edit
// collection. OpenFKReversePicker / ReverseFKLookup / ActivePendingEditSet
// / ActiveConnectionProfile resolve the gD reverse-FK + commit-dialog
// paths. All fields are optional and nil-safe: controllers nil-check on
// dispatch.
type EditDeps struct {
	PendingDiscard *helpers.PendingDiscardHelper
	JumpList       *ui.ResultJumpList
	FKForward      *helpers.FKForwardHelper
	PendingEditSet *models.PendingEditSet

	// OpenFKReversePicker pushes the reverse-FK picker popup. Wired by
	// the orchestrator to FKReversePickerController.Open; ResultTabs-
	// Controller's gD handler invokes it after assembling the entries.
	// Nil-safe — the handler surfaces a toast when unwired.
	OpenFKReversePicker FKReversePickerOpener

	// ReverseFKLookup resolves inbound foreign keys for (schema, table)
	// against the currently-active SQLSession's FKCache. The orchestrator
	// wires a closure that routes through activeSQLSession.FKCache().
	// GetReverse. Nil-safe.
	ReverseFKLookup func(ctx context.Context, schema, table string) ([]models.ForeignKey, error)

	// ActivePendingEditSet returns the PendingEditSet for the currently-
	// active result tab's (connID, baseTable), creating one on first
	// access via the per-table registry. Returns nil when no editable
	// tab is active. dbsavvy-8oo stub #10 / #3.
	ActivePendingEditSet func() *models.PendingEditSet

	// ActiveConnectionProfile returns the currently-bound connection
	// profile, or nil when no connection is active. CommitDialogOpen
	// needs the full profile (Color, ConfirmWrites, ReadOnly) to drive
	// the dialog's gates + styling. dbsavvy-8oo stub #5.
	ActiveConnectionProfile func() *models.Connection
}

// HelperBag is the dependency bundle every controller receives. It is the
// composition of the role-specific bundles (CoreDeps, NavDeps, UIDeps,
// QueryDeps, ThreadingDeps, EditDeps), embedded so each bundle's fields
// promote to the bag for backwards-compatible `helpers.Field` access.
// Production wiring constructs each bundle via its constructor so
// load-bearing deps are compile-time-required parameters and CoreDeps
// fails fast on nil (dbsavvy-fow.10 D2, Option C). Unit tests that do not
// exercise a path leave the corresponding bundle zero-valued; the
// controller code nil-checks on use for every optional field.
type HelperBag struct {
	CoreDeps
	NavDeps
	UIDeps
	QueryDeps
	ThreadingDeps
	EditDeps
}

// FKReversePickerOpener is the narrow surface ResultTabsController uses
// to push the reverse-FK picker. The concrete satisfier is
// *FKReversePickerController; orchestrator wiring captures it as a
// closure so this package stays free of an inter-controller reference.
type FKReversePickerOpener func(entries []ReverseEntry, origin FKReverseOriginTab, cursorRow, cursorCol int) bool

// DebugLogger is the minimal logging surface controllers expect.
// *slog.Logger satisfies it.
type DebugLogger interface {
	Debug(msg string, args ...any)
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

// logErr emits the controller's labelled debug-log breadcrumb for err and
// returns nothing. Use it at sites that swallow the error but still want the
// breadcrumb, so the call no longer reads as a discarded error.
func (b *baseController) logErr(label string, err error) {
	if err == nil {
		return
	}
	if b.helpers.Logger != nil {
		b.helpers.Logger.Debug(fmt.Sprintf("controller %q: %v", label, err))
	}
}

// wrapErr decorates a handler error with the controller's label.
func (b *baseController) wrapErr(label string, err error) error {
	if err == nil {
		return nil
	}
	b.logErr(label, err)
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

// Log returns the per-session *slog.Logger this controller's
// common.Common bag carries. Per AD-19: instrumentation paths reach the
// session logger through this accessor rather than widening the narrower
// DebugLogger interface. The underlying Common.Logger() accessor is
// nil-safe and returns a discarding logger when the bag is unwired, so
// callers may pass the return value through without nil-checking.
func (b *baseController) Log() *slog.Logger {
	return b.c.Logger()
}
