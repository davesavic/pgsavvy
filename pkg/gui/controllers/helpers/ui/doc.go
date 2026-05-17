// Package ui hosts UI-presentation helpers consumed by the gui
// controllers: confirm/prompt/toast popups, the table double-click stub,
// the first-run tip dismissal, and the boxlayout-driven window
// arrangement. Per dbsavvy-enn D10 the helpers package is split into
// data/ (drivers.Session adapters, persistence wrappers) and ui/ (popup
// pushes, status-line toasts, layout). UI helpers consume the
// types.GuiDriver and the *gui.ContextTree but never the gocui package
// directly — all gocui types are reached via the types-package aliases.
package ui
