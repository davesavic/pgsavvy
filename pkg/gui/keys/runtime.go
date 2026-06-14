package keys

import (
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
)

// Runtime is the aggregate that bundles every keybinding-system
// collaborator. Constructed once at orchestrator wireWithDriver time and
// handed to controllers via HelperBag.KbRuntime so they share one
// commands.Registry / Matcher / ModeStore / WhichKey / ExRegistry view of
// the world.
//
// Fields are pointers and may be nil during tests that only need a
// subset of the surface; controllers MUST nil-check before use.
type Runtime struct {
	Commands   *commands.Registry
	Matcher    *Matcher
	ModeStore  *ModeStore
	WhichKey   *WhichKey
	ExCommands *ExRegistry
}

// NewRuntime builds a Runtime aggregate from the supplied collaborators.
// Any field may be nil; the orchestrator wires non-nil values.
func NewRuntime(c *commands.Registry, m *Matcher, ms *ModeStore, wk *WhichKey, ex *ExRegistry) *Runtime {
	return &Runtime{
		Commands:   c,
		Matcher:    m,
		ModeStore:  ms,
		WhichKey:   wk,
		ExCommands: ex,
	}
}
