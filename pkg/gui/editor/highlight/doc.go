// Package highlight wraps the Chroma v2 PostgreSQL lexer to provide
// SQL syntax highlighting for the query editor. It exposes two public
// functions:
//
//   - Highlight(text) returns ANSI SGR-highlighted text ready for the
//     terminal.
//   - Tokenize(text) returns a classified token stream with rune
//     offsets for downstream rendering logic.
//
// All Chroma imports are confined to this package; no other package in
// the tree should import Chroma directly.
package highlight
