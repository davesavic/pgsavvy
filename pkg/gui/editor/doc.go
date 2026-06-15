// Package editor hosts the canonical *Buffer + UndoTree state for one
// QUERY_EDITOR pane and the per-context gocui.Editor implementations
// pgsavvy ships outside the COMMAND_LINE master editor.
//
// VimEditor is the gocui.Editor bound to the QUERY_EDITOR view:
// it routes keystrokes through keys.Matcher under the QUERY_EDITOR
// scope and, on Insert-mode Passthrough, mutates the *Buffer directly
// instead of delegating to gocui.DefaultEditor.
//
// Statement splitting (SplitStatements / StatementAt / StatementRangeAt)
// uses Chroma-token-aware splitting: semicolons inside string literals,
// dollar-quoted blocks, and comments are correctly ignored.
package editor
