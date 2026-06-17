package types

import "strings"

// ContextKind classifies a Context by its layout/lifecycle role. See
// DESIGN.md §8 lines 561-571.
type ContextKind int

const (
	// SIDE_CONTEXT is a left-rail entry. Pushing a SIDE_CONTEXT wipes the
	// focus stack.
	SIDE_CONTEXT ContextKind = iota
	// MAIN_CONTEXT is one half of the right-side main pair (top/bottom).
	// Pushing a MAIN_CONTEXT removes any other MAIN_CONTEXT from the stack
	// before pushing, preserving popups above it.
	MAIN_CONTEXT
	// PERSISTENT_POPUP is a popup whose identity survives subsequent
	// pushes (no auto-pop on next push).
	PERSISTENT_POPUP
	// TEMPORARY_POPUP is a popup discarded automatically the next time
	// another popup is pushed on top of it.
	TEMPORARY_POPUP
	// EXTRAS_CONTEXT is a bottom-rail extras slot. It currently hosts no
	// context (the messages panel was removed); the kind survives for the
	// layout window box and future reuse.
	EXTRAS_CONTEXT
	// GLOBAL_CONTEXT has no view; it exists only to host global
	// keybindings (leader prefix, ":" command line, etc.).
	GLOBAL_CONTEXT
	// DISPLAY_CONTEXT is a pure render target with no input focus
	// semantics (e.g. transient which-key popup).
	DISPLAY_CONTEXT
	// STUB is a placeholder Context kind used by StubContext to keep
	// ContextTree iteration safe for deferred contexts (QUERY_EDITOR,
	// TABLE_DATA_EDITOR, RESULT_GRID, PLAN, WHICH_KEY, HISTORY) that ship
	// later. The layout manager filters views whose context Kind == STUB
	// so SetView is never called for them (DESIGN.md §8, D11 resolution).
	STUB
)

// PopupSizeKind is the size policy a popup context declares for its
// SetView rectangle. The orchestrator switches on this enum (not on the
// ContextKey) to derive the actual rect, keeping pixel math in the
// orchestrator while the per-context declaration lives in the wiring
// table (pkg/gui/context/setup.go). The zero value PopupSizeNone means
// the context has no Tier-3 popup rect (non-popup contexts, plus LIMIT
// and WHICH_KEY which render via dedicated overlay paths).
type PopupSizeKind int

const (
	// PopupSizeNone: no popupRectFor rect (zero value / default).
	PopupSizeNone PopupSizeKind = iota
	// PopupSizeCentered: a fractional centred rect; WidthFrac/HeightFrac
	// give the fraction of the popup-overlay canvas to occupy.
	PopupSizeCentered
	// PopupSizeCommandLine: full-width single-line strip at the canvas
	// bottom (vim-style ex command line).
	PopupSizeCommandLine
	// PopupSizeCheatsheet: centred, capped to fixed max cols×rows
	// (orchestrator-owned cheatsheet constants); falls back to the full
	// terminal canvas when popup-overlay dims are absent.
	PopupSizeCheatsheet
	// PopupSizeCellEditor: centred, height-bounded edit popup whose max
	// width is derived from the live canvas width by the orchestrator.
	PopupSizeCellEditor
	// PopupSizePrompt: centred, capped to a small fixed max cols×rows
	// (orchestrator-owned prompt constants) — a single-field prompt popup
	// sized to its label+input rather than a screen fraction. Clamped to
	// the canvas at render time so small terminals don't overflow.
	PopupSizePrompt
	// PopupSizeAnchored: a cursor-anchored dropdown (the completion
	// SUGGESTIONS popup). The geometry is NOT computed by popupRectFor —
	// it needs the live editor view handle (Dimensions/Origin) and the
	// context's anchor Position, both of which are only in scope at the
	// orchestrator call site. popupRectFor returns no rect for this kind;
	// the call site reads the editor view and the SuggestionsContext
	// anchor to place the popup below the cursor (flipping above near the
	// editor's bottom edge), falling back to a centred rect when the
	// editor view handle is unavailable.
	PopupSizeAnchored
	// PopupSizeDocked: a right-anchored, full-height sidebar overlay
	// covering the rightmost fraction of the result-grid area
	// (dims["secondary"]). Used by the RELATIONSHIP_PANEL. WidthFrac gives
	// the fraction of the secondary canvas width the panel occupies; the
	// panel always spans the full secondary height. Falls back to no rect
	// when the secondary canvas is absent.
	PopupSizeDocked
)

// PopupRectSpec is the per-context popup-rect descriptor carried as data
// in the wiring table. Kind selects the size policy; WidthFrac/HeightFrac
// parametrise PopupSizeCentered (ignored by other kinds). Pixel math
// (the actual SetView rectangle) is computed by the orchestrator.
type PopupRectSpec struct {
	Kind       PopupSizeKind
	WidthFrac  float64
	HeightFrac float64
}

// ContextKey is the stable identity of a Context. See DESIGN.md §8 table
// at lines 576-594; LIMIT is added for the terminal-too-small overlay.
type ContextKey string

const (
	SCHEMAS ContextKey = "schemas"
	TABLES  ContextKey = "tables"
	// SCHEMA_RAIL is the SIDE_CONTEXT container that multiplexes the SCHEMAS
	// and TABLES leaves into the single "schemas-tables" view (BREAKING token
	// consumed by the rail keybinding re-scoping in .5). The container owns
	// the active-tab index; the leaves carry inFlatten=false and render only
	// when the container calls them.
	SCHEMA_RAIL       ContextKey = "schema-rail"
	COLUMNS           ContextKey = "columns"
	INDEXES           ContextKey = "indexes"
	QUERY_EDITOR      ContextKey = "query_editor"
	TABLE_DATA_EDITOR ContextKey = "table_data_editor"
	RESULT_GRID       ContextKey = "result_grid"
	PLAN              ContextKey = "plan"
	MENU              ContextKey = "menu"
	CONFIRMATION      ContextKey = "confirmation"
	PROMPT            ContextKey = "prompt"
	SELECTION         ContextKey = "selection"
	SUGGESTIONS       ContextKey = "suggestions"
	COMMAND_LINE      ContextKey = "command_line"
	// SEARCH_LINE is the dedicated bottom-anchored single-line in-grid
	// search input. Mirrors COMMAND_LINE geometry
	// (PopupSizeCommandLine) but renders a "/" prefix and fires an
	// onChange seam per keystroke. TEMPORARY_POPUP, editable.
	SEARCH_LINE ContextKey = "search_line"
	HISTORY     ContextKey = "history"
	// SAVED_QUERY is the <leader>o saved-query picker popup. TEMPORARY_POPUP
	// kind; lists named queries from queries.yml, <cr> inserts the selected
	// SQL at the editor cursor, dd deletes.
	SAVED_QUERY ContextKey = "saved_query"
	WHICH_KEY   ContextKey = "which_key"
	GLOBAL      ContextKey = "global"
	LIMIT       ContextKey = "limit"
	CHEATSHEET  ContextKey = "cheatsheet"
	// HIDE_OVERLAY is the in-grid column-visibility overlay opened by
	// <leader>gH on the active result tab.
	HIDE_OVERLAY ContextKey = "hide_overlay"
	// EXPORT_MENU is the <leader>oe export-result menu opened from the
	// result-grid context. TEMPORARY_POPUP kind.
	EXPORT_MENU ContextKey = "export_menu"
	// FIRST_RUN_TIP is the welcome popup shown above CONNECTIONS on the
	// user's first launch. PERSISTENT_POPUP kind so subsequent popup
	// pushes do not auto-evict it (AD-1).
	FIRST_RUN_TIP ContextKey = "first_run_tip"
	// TABLE_INSPECT is the tabbed popup that replaces the columns/indexes
	// side rails. Non-editable; sized larger than the
	// generic 50% × 50% popup to fit table metadata.
	TABLE_INSPECT ContextKey = "table_inspect"
	// CELL_EDITOR is the in-grid cell mini-buffer (TEMPORARY_POPUP).
	CELL_EDITOR ContextKey = "cell_editor"
	// COMMIT_DIALOG is the pending-edit commit dialog (TEMPORARY_POPUP).
	COMMIT_DIALOG ContextKey = "commit_dialog"
	// CONFLICT_DIALOG is the per-conflict refresh/overwrite dialog (TEMPORARY_POPUP).
	CONFLICT_DIALOG ContextKey = "conflict_dialog"
	// FK_REVERSE_PICKER is the reverse-FK referencing-table picker (TEMPORARY_POPUP).
	FK_REVERSE_PICKER ContextKey = "fk_reverse_picker"
	// CONNECTION_MANAGER is the centered modal connection-manager box.
	// MAIN_CONTEXT kind: when top of the focus stack it
	// renders a centered bordered box over a blank background, suppressing
	// both the side rails and the QUERY_EDITOR for the frame.
	CONNECTION_MANAGER ContextKey = "connection_manager"
	// RELATIONSHIP_PANEL is the <leader>gr right-docked FK-exploration
	// sidebar. DISPLAY_CONTEXT kind: it renders a right-anchored overlay
	// over the result grid (PopupSizeDocked) and live-follows the focused
	// grid row WITHOUT stealing input focus (the grid keeps j/k); the
	// orchestrator's Tier-4 focus pass retains the underlying tab view
	// while this key is top of the stack.
	RELATIONSHIP_PANEL ContextKey = "relationship_panel"
)

// AllContextKeys returns every ContextKey constant declared above.
//
// MUST contain every ContextKey constant; the wiring invariant test
// (orchestrator/wiring_invariant_test.go) enumerates this slice to assert
// that each key is wired (present in the ContextTree, has a popupRectFor
// case when it renders as a popup, and provides a non-no-op HandleRender
// when it is a renderable context). Adding a new ContextKey without adding
// it here — and without wiring it — makes that test fail.
func AllContextKeys() []ContextKey {
	return []ContextKey{
		SCHEMAS,
		TABLES,
		SCHEMA_RAIL,
		COLUMNS,
		INDEXES,
		QUERY_EDITOR,
		TABLE_DATA_EDITOR,
		RESULT_GRID,
		PLAN,
		MENU,
		CONFIRMATION,
		PROMPT,
		SELECTION,
		SUGGESTIONS,
		COMMAND_LINE,
		SEARCH_LINE,
		HISTORY,
		SAVED_QUERY,
		WHICH_KEY,
		GLOBAL,
		LIMIT,
		CHEATSHEET,
		HIDE_OVERLAY,
		EXPORT_MENU,
		FIRST_RUN_TIP,
		TABLE_INSPECT,
		CELL_EDITOR,
		COMMIT_DIALOG,
		CONFLICT_DIALOG,
		FK_REVERSE_PICKER,
		CONNECTION_MANAGER,
		RELATIONSHIP_PANEL,
	}
}

// IsEditable reports whether the view associated with k receives text
// input through a master gocui.Editor. COMMAND_LINE, QUERY_EDITOR and
// PROMPT are editable; TABLE_DATA_EDITOR flips when its concrete
// context ships. Non-editable views receive per-key SetKeybinding
// dispatch into the Matcher (no master Editor installed).
//
// PROMPT's flip fixes paste (gocui drops keybindings
// during bracketed-paste on non-editable views) and arrow-key caret
// motion (gocui's matchView rejects char-key keybindings on editable
// views, so a TextArea-backed editor is the only way to receive both
// printable runes AND arrow / Backspace / Delete / Home / End / paste
// uniformly).
//
// QUERY_EDITOR's flip is forward-compat: the
// orchestrator only installs a master Editor on a live (non-STUB)
// context, so flipping here has no runtime effect until the real
// QUERY_EDITOR context lands.
func (k ContextKey) IsEditable() bool {
	return k == COMMAND_LINE || k == QUERY_EDITOR || k == PROMPT || k == CELL_EDITOR || k == SEARCH_LINE
}

// Display humanizes the snake_case key into a readable label for popup
// chrome — the cheatsheet tab bar + scope banner show these instead of
// the raw slug ("query_editor" -> "Query Editor", "global" -> "Global").
func (k ContextKey) Display() string {
	if k == "" {
		return ""
	}
	words := strings.Split(string(k), "_")
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// KeybindingsOpts is the (currently empty) bag passed to GetKeybindings
// so Controllers can branch on mode/state without changing the interface
// signature later. Populated incrementally by downstream epics.
type KeybindingsOpts struct{}

// KeybindingsFn is the signature a Controller registers via
// IBaseContext.AddKeybindingsFn to contribute keybindings to a Context.
//
// Returns *ChordBinding (the chord-aware binding shape). The Handler
// and ViewName fields are transitional shims that let the
// orchestrator's single-key registration loop keep working while the
// master Editor / commands.Registry dispatch path lands.
type KeybindingsFn func(KeybindingsOpts) []*ChordBinding

// IBaseContext is the lifecycle + identity contract every Context
// satisfies. Signature mirrors DESIGN.md §8 lines 608-630.
type IBaseContext interface {
	GetKey() ContextKey
	GetViewName() string
	GetWindowName() string
	GetKind() ContextKind
	// GetTitle returns the heading rendered in the view's frame top edge.
	// Empty string suppresses the title — used by frameless / popup
	// contexts that do not want a heading.
	GetTitle() string

	HandleFocus(opts OnFocusOpts) error
	HandleFocusLost(opts OnFocusLostOpts) error
	HandleRender() error
	HandleRenderToMain() error
	HandleQuit() error

	NeedsRerenderOnHeightChange() bool
	NeedsRerenderOnWidthChange() bool

	AddKeybindingsFn(fn KeybindingsFn)
	GetKeybindings(opts KeybindingsOpts) []*ChordBinding
	GetMouseKeybindings(opts KeybindingsOpts) []MouseBinding
}
