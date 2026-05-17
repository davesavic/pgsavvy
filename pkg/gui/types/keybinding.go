package types

// KeyBinding is a single key-to-handler mapping. Mode gates the binding
// to one or more editor modes; the zero value (ModeNormal) means the
// binding is always live. Chord-aware bindings are introduced by epic E5
// as a separate ChordBinding type and are out of scope here.
type KeyBinding struct {
	ViewName    string
	Key         Key
	Mod         Modifier
	Handler     func() error
	Description string
	Mode        Mode
}
