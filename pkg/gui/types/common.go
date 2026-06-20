package types

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// IGuiCommon is the minimum aggregator surface every gui-layer collaborator
// receives. It exposes the shared cross-cutting bag declared in
// pkg/common.Common; richer accessors are layered on by HelperCommon and
// by epic-specific extensions.
type IGuiCommon interface {
	Common() *common.Common
}

// HelperCommon is the aggregator passed to controllers, helpers, and
// contexts. It embeds IGuiCommon today; later epics extend it with
// accessors for the Helpers bag, presentation, and GuiDriver.
type HelperCommon interface {
	IGuiCommon
}

// ContextTreeDeps is the injection bag NewContextTree consumes in T2
// (concrete contexts). It carries hooks for empty-state predicates,
// presentation callbacks, and i18n lookups that the concrete Context
// implementations need but which the stack semantics in T1 do not.
//
// All fields are optional (zero value is safe). Concrete contexts MUST
// nil-check every hook before invoking it. The GuiDriver field is the
// sole seam through which contexts perform view writes — no pkg/gui/context
// file is permitted to import gocui directly (see DESIGN.md §8).
//
// Downstream tasks supply concrete implementations:
//   - T6 provides EmptyStateHook (connections empty state hint).
//   - T8 provides PresentationHook (confirmation popup styling),
//     PerRowDecorationHook (connection-picker rows), and LimitText.
//   - T5 consumes the SchemasContext show-hidden accessors.
type ContextTreeDeps struct {
	// GuiDriver is the runtime seam contexts use to schedule writes via
	// Update / UpdateContentOnly. Nil-safe: contexts must nil-check.
	GuiDriver GuiDriver

	// EmptyStateHook is invoked by ConnectionsContext.HandleRender. When
	// renderEmpty is true the context writes hint instead of the row list.
	EmptyStateHook func(common *common.Common) (renderEmpty bool, hint string)

	// RailEmptyText is the rail-aware empty-state hook for the
	// SCHEMAS/TABLES/COLUMNS/INDEXES side rails. The
	// CONNECTIONS rail keeps its dedicated EmptyStateHook (above) because
	// its emptiness is decided by a live profile provider rather than the
	// rendered item slice. These four rails instead already know they are
	// empty (len(items) == 0) and only need the contextual placeholder
	// string, so the hook is keyed by ContextKey and returns the dim
	// placeholder text for that rail. Nil-safe: when nil (or when the
	// returned string is empty) the rail falls through to the prior blank
	// render with no panic.
	RailEmptyText func(rail ContextKey) (placeholder string)

	// PresentationHook is invoked by ConfirmationContext.HandleRender to
	// fetch border style and header text for the connection-color popup.
	PresentationHook func(conn *models.Connection) (borderStyle TextStyle, headerText string)

	// PerRowDecorationHook is invoked by ConnectionsContext per row in the
	// picker rendering pass to fetch the icon, label, and color string.
	PerRowDecorationHook func(conn *models.Connection) (icon, label, color string)

	// RowSuffix returns plain trailing text appended to a connection-picker
	// row after the name (e.g. the parsed "host/database" endpoint). The
	// orchestrator binds this to a closure that parses the profile DSN into
	// its discrete host + database fields. Nil-safe: a nil hook (or an empty
	// return) leaves the row name-only.
	RowSuffix func(conn *models.Connection) string

	// LimitText returns the text rendered by LimitContext for the
	// terminal-too-small overlay (typically Tr.TerminalTooSmall).
	LimitText func() string

	// FirstRunTipText returns the (title, body) pair rendered by
	// FirstRunTipContext on first launch. The
	// orchestrator binds this to Tr.FirstRunTipTitle / Tr.FirstRunTipBody.
	// Nil-safe: FirstRunTipContext.HandleRender is a no-op when nil.
	FirstRunTipText func() (title, body string)

	// WhichKey is the live which-key notifier consumed by WhichKeyContext
	// to fetch visibility + snapshot. Nil at construction time before
	// the orchestrator wires it. Nil-safe: WhichKeyContext
	// renders a no-op when nil.
	WhichKey WhichKeyState

	// WhichKeyRows resolves the children rendered for a given (scope,
	// prefix) when the popup is visible. Bound by the orchestrator
	// to a closure over the live TrieSet + ModeStore. Nil-safe:
	// nil → no rows rendered.
	WhichKeyRows func(scope ContextKey, prefix []ChordKey) []ChildRow

	// ModeStore is the per-context modal-state store. The COMMAND_LINE
	// context flips it to ModeCommand on focus so the Matcher routes
	// printable runes via the Insert/Command passthrough fast path.
	// Typed as the minimal ModeSetter interface so pkg/gui/context does
	// not import pkg/gui/keys (the canonical *keys.ModeStore satisfies
	// it structurally). Nil-safe: contexts must nil-check.
	ModeStore ModeSetter

	// Matcher is the runtime keys-matcher surface contexts need on
	// focus transitions to drop any half-built chord pending. Only the
	// Cancel hook is exposed here so pkg/gui/context stays decoupled
	// from pkg/gui/keys. *keys.Matcher satisfies it structurally via
	// its existing Cancel() method. Nil-safe: contexts must nil-check.
	Matcher MatcherCanceller

	// SaveBuffer is invoked by QueryEditorContext.HandleFocusLost when
	// the live *editor.Buffer is Dirty. The orchestrator binds this to
	// a closure that captures Common.Fs + Common.StateDir + g.OnWorker;
	// the closure copies the content string on the MainLoop (cheap —
	// the caller already grabs buf.String() under RLock) and writes
	// the raw `.sql` file from a worker goroutine. Nil-safe: a nil
	// hook is a no-op so HandleFocusLost stays correct in tests.
	SaveBuffer func(connID, uuid, content string)

	// HiddenSchemasForActiveConn returns the runtime-hidden schema names
	// for the currently active connection (AppState.HiddenSchemas[connID]).
	// SchemasContext.renderRows consults this when showHiddenMode == false
	// to skip rows whose name is in the list; when showHiddenMode == true
	// the filter is bypassed so the user can see (and unhide) them.
	// populateSchemasRail intentionally does NOT apply this filter at
	// build-time (see adapters.go) so the toggle flips render output
	// without re-loading the rail. Nil hook → no runtime filtering (all
	// items render regardless of the toggle).
	HiddenSchemasForActiveConn func() []string

	// IsDisconnected reports whether the active session has been marked
	// connection-dead. When true, schema/table/column/index rails render
	// their items dimmed. Nil-safe: nil → not disconnected.
	IsDisconnected func() bool

	// SpinnerFrame returns the live wall-clock spinner frame index used to
	// animate the Active connect-stage row in the CONNECTION_MANAGER modal
	// (T3 AD5/AD6a). The orchestrator binds it to Gui.SpinnerFrame so the
	// staged checklist's "⠙ Loading objects…" glyph cycles in lock-step with
	// the status-bar spinner. Nil-safe: ConnectionManagerContext.body falls
	// back to a static glyph (test fixtures leave it unset).
	SpinnerFrame func() int64
}

// ModeSetter is the minimal surface contexts use to flip / reset modal
// state on focus transitions. *keys.ModeStore satisfies this; tests
// substitute a fake.
type ModeSetter interface {
	Set(ctx ContextKey, mode Mode)
	Reset(ctx ContextKey)
}

// MatcherCanceller is the minimal surface contexts use to drop the
// matcher's pending chord / which-key state on focus transitions.
// *keys.Matcher satisfies this via its existing Cancel() method; tests
// substitute a fake.
type MatcherCanceller interface {
	Cancel()
}
