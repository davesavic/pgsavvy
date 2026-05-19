package ui

import (
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// MessagesSink appends a single line to the messages panel. The default
// impl writes via the GUI driver on the UI thread; tests inject a fake
// that captures lines synchronously. dbsavvy-66p.13.
type MessagesSink interface {
	// Append writes line to the sink. Implementations append a trailing
	// newline; callers must NOT pre-terminate the line.
	Append(line string)
}

// DefaultMessagesSink is the production MessagesSink. It schedules the
// underlying driver.Write through OnUIThreadContentOnly so the messages
// view's content buffer is mutated on the gocui MainLoop — no direct
// goroutine write into view state (DESIGN.md §17). A nil driver is a
// no-op; a nil OnUIThreadContentOnly closure falls back to a
// synchronous driver.Write (test fallback).
type DefaultMessagesSink struct {
	driver                types.GuiDriver
	onUIThreadContentOnly func(func() error)
	viewName              string
}

// NewDefaultMessagesSink builds a sink bound to driver. viewName
// defaults to the MESSAGES context key. onUIThreadContentOnly may be nil
// (synchronous fallback for tests).
func NewDefaultMessagesSink(driver types.GuiDriver, onUIThreadContentOnly func(func() error)) *DefaultMessagesSink {
	return &DefaultMessagesSink{
		driver:                driver,
		onUIThreadContentOnly: onUIThreadContentOnly,
		viewName:              string(types.MESSAGES),
	}
}

// Append schedules a line+"\n" write into the messages view. Nil driver
// makes this a no-op so tests that omit the driver can still
// instantiate a sink without panic.
func (s *DefaultMessagesSink) Append(line string) {
	if s == nil || s.driver == nil {
		return
	}
	payload := []byte(line + "\n")
	write := func() error {
		_, err := s.driver.Write(s.viewName, payload)
		return err
	}
	if s.onUIThreadContentOnly == nil {
		_ = write()
		return
	}
	s.onUIThreadContentOnly(write)
}
