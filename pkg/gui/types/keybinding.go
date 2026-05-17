package types

// KeyBinding is a single key-to-handler mapping. Mode gates the binding
// to one or more editor modes; the zero value (ModeNormal) means the
// binding is always live.
//
// Deprecated: removed in dlp.8c. Controllers now publish *ChordBinding
// (see types.KeybindingsFn); this struct is retained only because a
// handful of test recorder fakes still reference it during the dlp.8a
// shim period.
type KeyBinding struct {
	ViewName    string
	Key         Key
	Mod         Modifier
	Handler     func() error
	Description string
	Mode        Mode
}
