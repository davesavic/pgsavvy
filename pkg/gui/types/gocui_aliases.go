package types

import "github.com/jesseduffield/lazygit/pkg/gocui"

// Modifier mirrors gocui.Modifier so KeyBinding can name modifier keys
// without forcing the rest of pkg/gui/types/** to import gocui.
type Modifier = gocui.Modifier

// Key mirrors gocui.Key so KeyBinding and GuiDriver can reference the
// runtime's composite key type without a direct gocui import.
type Key = gocui.Key

// KeyName mirrors gocui.KeyName (a tcell-key enum). ViewMouseBinding.Key
// is a KeyName (NOT a composite Key), so the mouse helper accepts this
// alias to register mouse buttons / wheel events without importing gocui
// directly. Added per dbsavvy-zro T7b mouse helper.
type KeyName = gocui.KeyName

// Mouse button / wheel constants, re-exported so pkg/gui/keys/** and
// pkg/gui/controllers/helpers/** can wire mouse bindings without
// importing gocui (a dbsavvy-zro AC invariant). Names mirror gocui.
var (
	MouseLeft       = gocui.MouseLeft
	MouseRight      = gocui.MouseRight
	MouseMiddle     = gocui.MouseMiddle
	MouseWheelUp    = gocui.MouseWheelUp
	MouseWheelDown  = gocui.MouseWheelDown
	MouseWheelLeft  = gocui.MouseWheelLeft
	MouseWheelRight = gocui.MouseWheelRight
)

// ModNone / ModShift / ModCtrl / ModAlt re-export the gocui modifier
// constants for the same reason.
const (
	ModNone  = gocui.ModNone
	ModShift = gocui.ModShift
	ModCtrl  = gocui.ModCtrl
	ModAlt   = gocui.ModAlt
)

// View is the concrete view handle yielded by the gocui runtime. Aliased
// here so context, controller, and driver code can pass *gocui.View
// instances without each importing gocui directly.
type View = *gocui.View

// ViewMouseBinding mirrors gocui.ViewMouseBinding. IBaseContext returns
// a slice of these from GetMouseKeybindings.
type ViewMouseBinding = gocui.ViewMouseBinding

// MouseBinding is the canonical pointer form returned by
// IBaseContext.GetMouseKeybindings and accepted by GuiDriver.
type MouseBinding = *gocui.ViewMouseBinding

// ViewMouseBindingOpts mirrors gocui.ViewMouseBindingOpts, the payload
// passed to mouse handlers.
type ViewMouseBindingOpts = gocui.ViewMouseBindingOpts

// Manager mirrors gocui.Manager so GuiDriver.SetManager can accept the
// same variadic shape the runtime expects.
type Manager = gocui.Manager
