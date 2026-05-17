// Package types declares the foundational type system shared by every
// pkg/gui subpackage: ContextKind/ContextKey enums, the IBaseContext
// interface, the Views holder, focus/refresh option structs, the Mode
// bitmask, KeyBinding, the GuiCommon/HelperCommon aggregators, and the
// GuiDriver interface that abstracts the underlying gocui runtime.
//
// The only file in this package permitted to import gocui is
// gocui_aliases.go. All other files MUST use the aliases declared there
// (Modifier, Key, View, ViewMouseBinding, ViewMouseBindingOpts, Manager,
// MouseBinding) so that downstream packages can be unit-tested without
// instantiating a *gocui.Gui.
package types
