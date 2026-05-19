package editor

// JumpList is the bounded ring buffer of recent cursor positions
// (vim's `<C-o>` / `<C-i>` stack). wwd.2 ships this empty shell so
// Buffer.Jumps can hold a *JumpList pointer; wwd.3 fills the
// push-only semantics with a 100-entry cap. Bidirectional walk
// (`<C-o>` / `<C-i>`) is deferred per epic Architecture Decision 9.
type JumpList struct{}
