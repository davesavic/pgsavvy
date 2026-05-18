// Package editor hosts the per-context gocui.Editor implementations
// dbsavvy ships outside the COMMAND_LINE master editor.
//
// dbsavvy-66p.11 introduces NaiveEditor: a multi-line editor that wraps
// gocui.DefaultEditor without vim modes (every keystroke writes
// directly to the view's TextArea). The full vim-style editor ships in
// epic dbsavvy-wwd (E9).
//
// Statement splitting (SplitStatements / StatementAt) is a naive
// ;-split with a documented limitation around string literals — the
// SQL-aware splitter lands with the vim editor in E9.
package editor
