package types

import (
	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/models"
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

	// PresentationHook is invoked by ConfirmationContext.HandleRender to
	// fetch border style and header text for the connection-color popup.
	PresentationHook func(conn *models.Connection) (borderStyle TextStyle, headerText string)

	// PerRowDecorationHook is invoked by ConnectionsContext per row in the
	// picker rendering pass to fetch the icon, label, and color string.
	PerRowDecorationHook func(conn *models.Connection) (icon, label, color string)

	// LimitText returns the text rendered by LimitContext for the
	// terminal-too-small overlay (typically Tr.TerminalTooSmall).
	LimitText func() string

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
}

// ModeSetter is the minimal surface contexts use to flip / reset modal
// state on focus transitions. *keys.ModeStore satisfies this; tests
// substitute a fake.
type ModeSetter interface {
	Set(ctx ContextKey, mode Mode)
	Reset(ctx ContextKey)
}
