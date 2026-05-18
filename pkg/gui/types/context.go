package types

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
	// EXTRAS_CONTEXT hosts the command_log panel.
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
	// so SetView is never called for them (DESIGN.md §8, D11 resolution
	// in dbsavvy-enn.3).
	STUB
)

// ContextKey is the stable identity of a Context. See DESIGN.md §8 table
// at lines 576-594; LIMIT is added for the terminal-too-small overlay.
type ContextKey string

const (
	CONNECTIONS       ContextKey = "connections"
	SCHEMAS           ContextKey = "schemas"
	TABLES            ContextKey = "tables"
	COLUMNS           ContextKey = "columns"
	INDEXES           ContextKey = "indexes"
	QUERY_EDITOR      ContextKey = "query_editor"
	TABLE_DATA_EDITOR ContextKey = "table_data_editor"
	RESULT_GRID       ContextKey = "result_grid"
	PLAN              ContextKey = "plan"
	LOG               ContextKey = "log"
	MENU              ContextKey = "menu"
	CONFIRMATION      ContextKey = "confirmation"
	PROMPT            ContextKey = "prompt"
	SUGGESTIONS       ContextKey = "suggestions"
	COMMAND_LINE      ContextKey = "command_line"
	HISTORY           ContextKey = "history"
	WHICH_KEY         ContextKey = "which_key"
	GLOBAL            ContextKey = "global"
	LIMIT             ContextKey = "limit"
	CHEATSHEET        ContextKey = "cheatsheet"
)

// IsEditable reports whether the view associated with k receives text
// input through a master gocui.Editor. Today only COMMAND_LINE is
// editable; QUERY_EDITOR / TABLE_DATA_EDITOR flip when their concrete
// contexts ship. Non-editable views receive per-key SetKeybinding
// dispatch into the Matcher (no master Editor installed).
func (k ContextKey) IsEditable() bool {
	return k == COMMAND_LINE
}

// KeybindingsOpts is the (currently empty) bag passed to GetKeybindings
// so Controllers can branch on mode/state without changing the interface
// signature later. Populated incrementally by downstream epics.
type KeybindingsOpts struct{}

// KeybindingsFn is the signature a Controller registers via
// IBaseContext.AddKeybindingsFn to contribute keybindings to a Context.
//
// Returns *ChordBinding (the chord-aware binding shape). The Handler
// and ViewName fields are transitional shims (dlp.8a) that let the
// orchestrator's single-key registration loop keep working while the
// master Editor / commands.Registry dispatch path lands in dlp.8b/c.
type KeybindingsFn func(KeybindingsOpts) []*ChordBinding

// IBaseContext is the lifecycle + identity contract every Context
// satisfies. Signature mirrors DESIGN.md §8 lines 608-630.
type IBaseContext interface {
	GetKey() ContextKey
	GetViewName() string
	GetWindowName() string
	GetKind() ContextKind

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
