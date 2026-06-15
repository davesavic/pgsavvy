// Package context contains the concrete Context implementations the
// pgsavvy TUI focus stack manages: the five side-rail contexts
// (Connections, Schemas, Tables, Columns, Indexes), the four popup
// contexts (Menu, Confirmation, Prompt, Suggestions), the command-log
// EXTRAS context, the GLOBAL context (no view), the LIMIT
// terminal-too-small overlay, and six StubContext placeholders for
// Contexts that ship in later epics (QUERY_EDITOR, TABLE_DATA_EDITOR,
// RESULT_GRID, PLAN, WHICH_KEY, HISTORY).
//
// NewContextTree wires every Context (live + stubs) into a registry the
// gui bootstrap consumes. Cross-cutting hooks (empty-state, popup
// presentation, per-row decoration, limit text) are injected via
// types.ContextTreeDeps so context code stays decoupled from helpers,
// controllers, and the style builder.
//
// No file in this package imports gocui directly. View writes go through
// types.ContextTreeDeps.GuiDriver, which T1 declared.
package context
