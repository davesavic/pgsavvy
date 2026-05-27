package types

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// Mutexes is the named-mutex bag described in DESIGN.md §17 (Threading
// Model). Each field guards one named concern; controllers and helpers
// take a pointer to the relevant field instead of carrying anonymous
// sync.Mutex values inline. This keeps lock ordering reviewable and
// rules out the "two unrelated paths happen to share a Mutex" footgun.
//
// Downstream tasks (dbsavvy-66p.5, 66p.9, 66p.12, 66p.13, 66p.14) plug
// concrete callers into these fields; this struct is intentionally a
// stub today.
type Mutexes struct {
	// RefreshingMutex serialises refresh passes that mutate cached
	// snapshots (schemas/tables/columns). Held for the duration of a
	// refresh helper invocation.
	RefreshingMutex sync.Mutex

	// PopupMutex serialises popup push/pop transitions to keep the focus
	// stack from racing with the layout pass when several controllers
	// schedule popups concurrently.
	PopupMutex sync.Mutex

	// FetchMutex serialises in-flight fetch workers (history search,
	// row-stream prefetch) so concurrent users of a shared cursor don't
	// interleave reads.
	FetchMutex sync.Mutex
}

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
// file is permitted to import gocui directly (see DESIGN.md §8 and the
// AC fix in dbsavvy-enn.3).
//
// Downstream tasks supply concrete implementations:
//   - T6 (enn.7) provides EmptyStateHook (connections empty state hint).
//   - T8 (enn.9) provides PresentationHook (confirmation popup styling),
//     PerRowDecorationHook (connection-picker rows), and LimitText.
//   - T5 (enn.6) consumes the SchemasContext show-hidden accessors.
type ContextTreeDeps struct {
	// GuiDriver is the runtime seam contexts use to schedule writes via
	// Update / UpdateContentOnly. Nil-safe: contexts must nil-check.
	GuiDriver GuiDriver

	// EmptyStateHook is invoked by ConnectionsContext.HandleRender. When
	// renderEmpty is true the context writes hint instead of the row list.
	EmptyStateHook func(common *common.Common) (renderEmpty bool, hint string)

	// RailEmptyText is the rail-aware empty-state hook for the
	// SCHEMAS/TABLES/COLUMNS/INDEXES side rails (dbsavvy-fow.5 U7). The
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

	// LimitText returns the text rendered by LimitContext for the
	// terminal-too-small overlay (typically Tr.TerminalTooSmall).
	LimitText func() string

	// FirstRunTipText returns the (title, body) pair rendered by
	// FirstRunTipContext on first launch (dbsavvy-56u.2). The
	// orchestrator binds this to Tr.FirstRunTipTitle / Tr.FirstRunTipBody.
	// Nil-safe: FirstRunTipContext.HandleRender is a no-op when nil.
	FirstRunTipText func() (title, body string)

	// WhichKey is the live which-key notifier consumed by WhichKeyContext
	// to fetch visibility + snapshot. Nil at construction time before
	// the orchestrator wires it (dlp.8c). Nil-safe: WhichKeyContext
	// renders a no-op when nil.
	WhichKey WhichKeyState

	// WhichKeyRows resolves the children rendered for a given (scope,
	// prefix) when the popup is visible. Bound by the orchestrator
	// (dlp.8c) to a closure over the live TrieSet + ModeStore. Nil-safe:
	// nil → no rows rendered.
	WhichKeyRows func(scope ContextKey, prefix []ChordKey) []ChildRow

	// CheatsheetRender produces the rendered cheatsheet body for the
	// supplied focused scope. Bound by the orchestrator (dlp.10) to a
	// closure that captures the live TrieSet + commands.Registry +
	// TranslationSet and calls into pkg/cheatsheet. Nil-safe:
	// CheatsheetContext.HandleRender is a no-op when nil.
	CheatsheetRender func(scope ContextKey) string

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
