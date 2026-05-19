// Package editor hosts the canonical *Buffer + UndoTree state for one
// QUERY_EDITOR pane and the per-context gocui.Editor implementations
// dbsavvy ships outside the COMMAND_LINE master editor.
//
// VimEditor (wwd.4) is the gocui.Editor bound to the QUERY_EDITOR view:
// it routes keystrokes through keys.Matcher under the QUERY_EDITOR
// scope and, on Insert-mode Passthrough, mutates the *Buffer directly
// instead of delegating to gocui.DefaultEditor.
//
// Statement splitting (SplitStatements / StatementAt) is a naive
// ;-split with a documented limitation around string literals — the
// SQL-aware splitter lands later in epic dbsavvy-wwd.
package editor
